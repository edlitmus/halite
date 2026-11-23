package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/atomicfile"
	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/orchplan"
	"github.com/edlitmus/halite/internal/treesign"
	"github.com/edlitmus/halite/internal/value"
)

// defaultPlanTTL and defaultPlanMaxTTL are an hour: the default
// `job_signature_max_ttl` (cmd/halite-node DefaultSignatureMaxTTL), which
// is the longest a node accepts a signed job for unless it is told
// otherwise. A plan's window has to hold the whole orchestration, so the
// default is the longest a default node allows rather than the fifteen
// minutes a single signed job gets.
const (
	defaultPlanTTL    = time.Hour
	defaultPlanMaxTTL = time.Hour
)

// orchPlan is `halite-hub orch plan`: issue 765's off-hub compile and
// signature of an orchestration, for nodes that require signed jobs.
//
// It does not contact the hub and does not open the hub's state. It runs
// on the operator's machine, against their own checkout, with their own
// signer, and that is what keeps the authority off the hub: the hub never
// asks for a signature and holds nothing that can make one.
func orchPlan(args *cli.Args) int {
	if len(args.Positional) >= 2 && args.Positional[1] == "show" {
		return orchPlanShow(args)
	}
	if len(args.Positional) < 2 {
		cli.Usagef("orch plan needs an orchestration SLS")
	}
	sls := strings.Split(strings.Join(args.Positional[1:], ","), ",")
	dir := args.Flag("tree", "")
	if dir == "" {
		cli.Usagef("orch plan needs --tree, the checkout of the state tree to compile from")
	}
	out := args.Flag("out", "")
	if out == "" {
		cli.Usagef("orch plan needs --out, the plan file to write")
	}
	keyPath, extPath := args.Flag("sign-key", ""), args.Flag("sign-extension", "")
	if (keyPath == "") == (extPath == "") {
		cli.Usagef("orch plan takes one of --sign-key or --sign-extension")
	}
	ttl := planDuration("ttl", args.Flag("ttl", ""), defaultPlanTTL)
	maxTTL := planDuration("max-ttl", args.Flag("max-ttl", ""), defaultPlanMaxTTL)
	if err := checkPlanTTL(ttl, maxTTL); err != nil {
		cli.Usagef("%v", err)
	}
	env := args.Flag("env", "base")
	var pillar *value.Map
	if raw := args.Flag("pillar", ""); raw != "" {
		decoded, err := value.DecodeJSON([]byte(raw))
		if err != nil {
			cli.Usagef("--pillar %q is not JSON: %v", raw, err)
		}
		m, ok := decoded.(*value.Map)
		if !ok {
			cli.Usagef("--pillar is a JSON object, such as '{\"version\":\"1.4.2\"}'")
		}
		pillar = m
	}

	roots, _ := treeRoots(args, env, dir)
	revision, err := checkoutRevision(roots, env)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	plan, payloads, err := hub.NewPlan(roots, hub.PlanOptions{
		SLS: sls, Env: env, Pillar: pillar, Test: args.Bool("test", false),
		ResumeOf: args.Flag("resume-of", ""), ResumeFrom: args.Flag("from", ""),
		TreeRevision: revision, Created: time.Now(), TTL: ttl,
	})
	if err != nil {
		cli.Fatalf("%v", err)
	}
	signatures, err := signPayloads(keyPath, extPath, payloads)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	for i := range plan.Steps {
		plan.Steps[i].Signature = signatures[i]
	}
	encoded, err := orchplan.Encode(plan)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	// Read back as the hub will read it, so that a plan this build cannot
	// parse is never written.
	if _, err := orchplan.Parse(encoded); err != nil {
		cli.Fatalf("the plan this wrote does not parse: %v", err)
	}
	if err := atomicfile.Write(out, encoded, 0o644); err != nil {
		cli.Fatalf("%v", err)
	}

	fmt.Printf("plan      %s\n", out)
	fmt.Printf("digest    %s\n", orchplan.Digest(encoded))
	fmt.Printf("sls       %s (%s)\n", strings.Join(plan.SLS, ","), plan.Env)
	fmt.Printf("signed    %d fleet step(s); %d hub step(s) listed, not signed\n", len(plan.Steps), len(plan.HubSteps))
	fmt.Printf("expires   %s (%s from now)\n", plan.Expires, ttl)
	if revision != "" {
		fmt.Printf("revision  %s\n", revision)
	} else {
		fmt.Println("revision  none: the checkout carries no _halite/manifest.json")
	}
	fmt.Println()
	fmt.Println("The whole run, every batch and wait included, must finish before the plan expires.")
	fmt.Println("The plan authorises these jobs, not their order: keep steps whose order matters in one step.")
	return 0
}

// checkPlanTTL is the expiry budget of issue 765: every step of a plan is
// signed at one moment and run at another, and a node refuses a signed job
// whose expiry lies further ahead than its `job_signature_max_ttl`.
func checkPlanTTL(ttl, maxTTL time.Duration) error {
	if ttl <= maxTTL {
		return nil
	}
	return fmt.Errorf("--ttl %s is longer than the %s a node accepts a signed job for (--max-ttl, the nodes' "+
		"job_signature_max_ttl, an hour by default), so every node would refuse every step. "+
		"Split a long orchestration into plans signed one after another, or raise job_signature_max_ttl "+
		"on the nodes it runs on and pass the same value as --max-ttl; raising it lengthens the life of "+
		"every signed job those nodes accept, not only this orchestration's", ttl, maxTTL)
}

func planDuration(name, raw string, def time.Duration) time.Duration {
	if raw == "" {
		return def
	}
	d, err := value.ParseDuration(raw)
	if err != nil || d <= 0 {
		cli.Usagef("--%s %q is not a positive duration", name, raw)
	}
	return d
}

// checkoutRevision is the issue 764 manifest id of the checkout, when it
// carries a manifest, after checking that the checkout is what the
// manifest describes: recording the id of a manifest the files have moved
// away from would claim a binding that is not there.
//
// The signature is not checked. The plan records which revision was
// compiled; whether that revision is one the nodes trust is theirs to
// decide (`halite-hub tree verify` answers it here).
func checkoutRevision(roots *fileserver.Roots, env string) (string, error) {
	raw, _, err := roots.Read(env, treesign.ManifestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("reading %s: %w", treesign.ManifestPath, err)
	}
	m, err := treesign.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", treesign.ManifestPath, err)
	}
	if m.Env != env {
		return "", fmt.Errorf("the checkout's manifest is for %s and the plan compiles %s", m.Env, env)
	}
	served, err := roots.TreeEntries(env, m.HashType)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, e := range served {
		seen[e.Path] = true
		if want, ok := m.Lookup(e.Path); !ok || want.Hash != e.Hash {
			return "", fmt.Errorf("%s differs from the checkout's %s, so the plan cannot say which revision "+
				"it was compiled from; run `halite-hub tree manifest` again or plan from a clean checkout",
				e.Path, treesign.ManifestPath)
		}
	}
	for _, e := range m.Files {
		if !seen[e.Path] {
			return "", fmt.Errorf("%s is in the checkout's %s and not in the checkout", e.Path, treesign.ManifestPath)
		}
	}
	return treesign.ID(raw), nil
}

// signPayloads signs every step's payload with the one signer given.
func signPayloads(keyPath, extPath string, payloads []jobsign.Payload) ([]string, error) {
	out := make([]string, 0, len(payloads))
	if keyPath != "" {
		pem, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("--sign-key %s: %w", keyPath, err)
		}
		key, err := jobsign.DecodePrivateKey(pem)
		if err != nil {
			return nil, fmt.Errorf("--sign-key %s: %w", keyPath, err)
		}
		for _, p := range payloads {
			sig, err := jobsign.Sign(key, p)
			if err != nil {
				return nil, err
			}
			out = append(out, sig)
		}
		return out, nil
	}
	if len(payloads) == 0 {
		return out, nil
	}
	digests := make([][32]byte, len(payloads))
	for i, p := range payloads {
		digests[i] = jobsign.Digest(p)
	}
	ders, err := signDigestsWithExtension(context.Background(), extPath, digests)
	if err != nil {
		return nil, fmt.Errorf("--sign-extension %s: %w", extPath, err)
	}
	for _, der := range ders {
		out = append(out, encodeSignature(der))
	}
	return out, nil
}

// orchPlanShow prints a plan the way an operator reviews one before
// running it: every job, its target, arguments and expiry, in step order.
func orchPlanShow(args *cli.Args) int {
	if len(args.Positional) < 3 {
		cli.Usagef("orch plan show needs a plan file")
	}
	raw, err := os.ReadFile(args.Positional[2])
	if err != nil {
		cli.Fatalf("%v", err)
	}
	plan, err := orchplan.Parse(raw)
	if err != nil {
		cli.Fatalf("%s: %v", args.Positional[2], err)
	}
	fmt.Printf("plan      %s\n", orchplan.Digest(raw))
	fmt.Printf("sls       %s (%s)\n", strings.Join(plan.SLS, ","), plan.Env)
	if plan.Test {
		fmt.Println("test      yes: every state step is signed with test set, and it runs only under --test")
	}
	if plan.ResumeOf != "" {
		fmt.Printf("resumes   %s from %s\n", plan.ResumeOf, plan.ResumeFrom)
	}
	fmt.Printf("created   %s\n", plan.Created)
	fmt.Printf("expires   %s\n", plan.Expires)
	if plan.TreeRevision != "" {
		fmt.Printf("revision  %s\n", plan.TreeRevision)
	}
	fmt.Println("\nSigned jobs:")
	for _, s := range plan.Steps {
		fmt.Printf("  %-24s %-15s job %s\n", s.ID, s.Step, s.Job.JID)
		fmt.Printf("      %s on %q (%s), env %s\n", s.Job.Fun, s.Job.Target, s.Job.TargetKind, s.Job.Env)
		if len(s.Job.Arg) > 0 {
			fmt.Printf("      arg    %s\n", compactJSON(s.Job.Arg))
		}
		if len(s.Job.Kwarg) > 0 {
			fmt.Printf("      kwarg  %s\n", compactJSON(s.Job.Kwarg))
		}
	}
	if len(plan.HubSteps) > 0 {
		fmt.Println("\nRun on the hub, not signed (a compromised hub runs these whatever the plan says):")
		for _, h := range plan.HubSteps {
			fmt.Printf("  %-24s %-15s %s\n", h.ID, h.Step, h.Name)
		}
	}
	fmt.Println("\nThe plan authorises these jobs, not their order, and not batch, subset or run_parallel.")
	return 0
}

func compactJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

// planArgument reads `--plan <file>` for `orch run` and `orch resume`, as
// the text the hub parses. Sent as the file's bytes, so the digest the
// hub records is the digest of the file the operator holds.
func planArgument(args *cli.Args, kwargs map[string]any) {
	path := args.Flag("plan", "")
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		cli.Fatalf("--plan %s: %v", path, err)
	}
	if _, err := orchplan.Parse(raw); err != nil {
		cli.Fatalf("--plan %s: %v", path, err)
	}
	kwargs["plan"] = string(raw)
}
