package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/roster"
	"github.com/edlitmus/halite/internal/sshexec"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/value"
)

// OneshotProtocol is the version the pushed binary is spoken to with.
const oneshotProtocol = 1

// runSSH is `halite-hub ssh <target> <fun>`, SPEC section 21.
//
// It replaces `salt-ssh`, and the reason it is simpler is what it
// pushes: one static binary, rather than a Python tarball that then has
// to find a compatible Python on the target.
func runSSH(args *cli.Args) int {
	// The same reading `run` uses, so the two cannot disagree about
	// where a matcher flag puts the target. `-G 'os:FreeBSD'` carries
	// it, and reading the flag as a boolean lost it here too.
	kind, expression, fun, rest, err := resolveTarget(args)
	if err != nil {
		fmt.Fprint(os.Stderr, sshUsage)
		return cli.ExitUsage
	}
	// The flags read after the run, checked before it. runAcross and
	// reportSSH read these again with the same calls, so the two
	// readings cannot disagree; doing it here means a malformed one is
	// a usage error before the hub is opened -- and, for --out, before
	// the command has been run on every target only to fail at the
	// report. DIVERGENCE 5.221.
	cli.IntFlag(args, "ssh-concurrency", 8, 1)
	cli.IntFlag(args, "indent", 0, 0)
	if _, err := cli.ParseFormat(args.Flag("out", "nested")); err != nil {
		cli.Usagef("%v", err)
	}

	h := openHub(args, false)
	targets, err := sshTargets(h, args, kind, expression)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "no target in the roster matched %q\n", expression)
		return 1
	}

	binary := args.Flag("thin", h.cfg.String("ssh_binary", ""))
	if binary == "" {
		// The running program's own directory is where a build puts
		// them side by side, which is the common case and worth not
		// making an operator configure.
		if guess, err := guessNodeBinary(); err == nil {
			binary = guess
		}
	}
	if binary == "" {
		cli.Fatalf("agentless mode pushes a static halite-node binary and none was found; " +
			"set `ssh_binary` or pass --thin")
	}

	runner := &sshexec.Options{
		Binary:  binary,
		SSH:     h.cfg.String("ssh_command", ""),
		SCP:     h.cfg.String("scp_command", ""),
		Options: h.cfg.StringSlice("ssh_options"),
		Timeout: sshTimeout(args, h),
		Clean:   args.Bool("clean", false),
		Env:     os.Environ(),
		Log: func(level, msg string, kv ...any) {
			if level == "warn" || level == "error" {
				h.log.Warn(msg, kv...)
				return
			}
			h.log.Info(msg, kv...)
		},
	}

	kwargs := map[string]any{}
	for _, e := range args.Kwargs.Entries() {
		if k, ok := e.Key.(string); ok {
			kwargs[k] = e.Val
		}
	}
	jid := string(newSSHJobID())

	results := runAcross(targets, args, func(t roster.Target) sshexec.Result {
		body, err := sshRequest(h, t, jid, fun, rest, kwargs, args)
		if err != nil {
			return sshexec.Result{Target: t, Err: err}
		}
		return runner.Run(context.Background(), t, body)
	})

	return reportSSH(args, results)
}

// sshTargets resolves the roster and matches the expression against it.
func sshTargets(h *hubContext, args *cli.Args, kind, expression string) ([]roster.Target, error) {
	backend := args.Flag("roster", h.cfg.String("roster", "flat"))
	if err := roster.CheckBackend(backend); err != nil {
		return nil, err
	}

	var loaded *roster.Roster
	var err error
	switch backend {
	case "flat":
		path := args.Flag("roster-file", h.cfg.String("roster_file", ""))
		if path == "" {
			path = filepath.Join(h.cfg.Root(), "roster")
		}
		loaded, err = roster.ReadFlat(path)
	case "sshconfig":
		loaded, err = roster.ReadSSHConfig(args.Flag("roster-file", ""))
	case "cache":
		loaded, err = rosterFromCache(h)
	case "ansible":
		loaded, err = roster.ReadAnsible(args.Flag("roster-file", h.cfg.String("roster_file", "")))
	default:
		return nil, fmt.Errorf("%q is not a roster backend this build reads", backend)
	}
	if err != nil {
		return nil, err
	}
	for _, warning := range loaded.Warnings {
		fmt.Fprintln(os.Stderr, "roster: "+warning)
	}

	// The targeting grammar of SPEC section 8, against the roster's
	// names and the grains it attached — so `-G 'os:FreeBSD'` works on
	// an agentless estate exactly as it does on an enrolled one.
	matcher, err := sshMatcher(kind, expression)
	if err != nil {
		return nil, err
	}
	// A pillar term reads the pillar each target would be sent, compiled
	// here from the grains its roster entry attached. It read none, so
	// `-I` selected nothing and `not I@...` selected everything,
	// including the targets the expression was written to leave out. A
	// target whose pillar will not compile cannot be decided and the run
	// is refused naming it, for the reasons the enrolled fleet's
	// dispatch gives (internal/hub targetPillar, DIVERGENCE 5.204).
	readsPillar := false
	for _, term := range matcher.Terms() {
		if term.Kind == target.Pillar || term.Kind == target.PillarRegex {
			readsPillar = true
		}
	}
	if readsPillar && len(h.cfg.Roots("pillar_roots")) == 0 {
		return nil, fmt.Errorf("target %q reads pillar, and this hub compiles no pillar "+
			"to target against; set pillar_roots", matcher.Expr())
	}
	env := args.Flag("env", h.cfg.String("env", "base"))
	var out []roster.Target
	var undecided []string
	var undecidedWhy error
	for _, t := range loaded.Targets {
		node := target.Node{ID: t.ID, Grains: t.Grains}
		var failed error
		if readsPillar {
			node.LoadPillar = rosterPillarLoader(h, t, env, &failed)
		}
		hit := matcher.Match(node)
		if failed != nil {
			undecided = append(undecided, t.ID)
			undecidedWhy = failed
			continue
		}
		if hit {
			out = append(out, t)
		}
	}
	if len(undecided) > 0 {
		sort.Strings(undecided)
		return nil, fmt.Errorf(
			"target %q reads pillar, and the pillar of %d roster target(s) (%s) will not "+
				"compile, so whether it matches them is unknown; fix their pillar, or exclude "+
				"them ahead of the pillar term (`not L@%s and ...`): %w",
			matcher.Expr(), len(undecided), strings.Join(undecided, ", "),
			strings.Join(undecided, ","), undecidedWhy)
	}
	return out, nil
}

// rosterPillarLoader compiles one roster target's pillar the first time a
// pillar term asks for it, recording in failed why it could not.
func rosterPillarLoader(h *hubContext, t roster.Target, env string, failed *error) func() *value.Map {
	var (
		done   bool
		loaded *value.Map
	)
	return func() *value.Map {
		if done {
			return loaded
		}
		done = true
		compiled, err := compileRosterPillar(h, t, env)
		if err != nil {
			*failed = err
			return nil
		}
		loaded = compiled
		return loaded
	}
}

// sshMatcher builds the target matcher for the roster.
//
// The same targeting grammar the fleet uses, against the roster's names
// and the grains it attached — so `-G 'os:FreeBSD'` works on an
// agentless estate exactly as it does on an enrolled one, without a
// second implementation of matching.
//
// It returns the compiled matcher rather than a predicate, because the
// caller has to know whether the expression reads pillar and has to
// supply the pillar it reads; a predicate closed over the roster's
// grains alone had no way to, and matched every pillar term against
// nothing.
func sshMatcher(flag, expression string) (*target.Matcher, error) {
	kind := target.Glob
	if flag != "" {
		parsed, ok := target.KindFromFlag(flag)
		if !ok {
			return nil, fmt.Errorf("-%s is not a target kind", flag)
		}
		kind = parsed
	}
	return target.Compile(kind, expression, nil)
}

// sshTimeout is how long one target may take.
func sshTimeout(args *cli.Args, h *hubContext) time.Duration {
	if flag := args.Flag("timeout", ""); flag != "" && flag != "true" {
		if d, err := time.ParseDuration(flag); err == nil {
			return d
		}
	}
	return h.cfg.Duration("ssh_timeout", 5*time.Minute)
}

// sshRequest builds the job one target receives.
func sshRequest(h *hubContext, t roster.Target, jid, fun string,
	arg []string, kwargs map[string]any, args *cli.Args) ([]byte, error) {

	req := map[string]any{
		"protocol": oneshotProtocol,
		"jid":      jid,
		"node_id":  t.ID,
		"fun":      fun,
		"env":      args.Flag("env", h.cfg.String("env", "base")),
		"test":     args.Bool("test", false),
	}
	if len(arg) > 0 {
		req["arg"] = arg
	}
	if len(kwargs) > 0 {
		req["kwarg"] = kwargs
	}
	if t.Grains != nil {
		encoded, err := value.EncodeJSON(t.Grains, 0)
		if err != nil {
			return nil, err
		}
		req["grains"] = json.RawMessage(encoded)
	}
	if t.Timeout > 0 {
		req["timeout_seconds"] = t.Timeout.Seconds()
	}

	// SPEC 21.1: pillar and file server content are compiled on the hub
	// and sent inline. That is what lets a target hold no state tree
	// and no pillar tree — and, more to the point, no other target's
	// secrets, which is the same property SPEC 12.1 gives an enrolled
	// node.
	if needsTree(fun) {
		files, err := inlineTree(h, args)
		if err != nil {
			return nil, err
		}
		if len(files) > 0 {
			req["files"] = files
		}
		pillar, secrets, err := inlinePillar(h, t, args)
		if err != nil {
			return nil, err
		}
		if pillar != nil {
			req["pillar"] = pillar
			// What to redact on the target: the values decrypted here,
			// and no others. DIVERGENCE 5.251.
			req["secrets"] = secrets
		}
	}
	return json.Marshal(req)
}

// needsTree reports whether a function compiles state, and so needs the
// tree sent with it.
func needsTree(fun string) bool {
	return strings.HasPrefix(fun, "state.")
}

// MaxInlineTree bounds what is sent with one job.
//
// SPEC 21.1 says inline is the default for small payloads and a reverse
// tunnel is used above a threshold. The tunnel is not built, so this is
// where an estate finds out: a refusal that names the size is better
// than a job that takes four minutes to transfer a tree on every run,
// against every target.
const MaxInlineTree = 4 << 20

// inlineTree reads the state tree the hub serves for this environment.
func inlineTree(h *hubContext, args *cli.Args) (map[string]string, error) {
	roots := h.cfg.Roots("file_roots")
	if len(roots) == 0 {
		return nil, fmt.Errorf("agentless state runs send the tree with the job, and this hub " +
			"serves none; set file_roots")
	}
	tree := fileserver.NewRoots(roots)
	env := args.Flag("env", h.cfg.String("env", "base"))
	manifest, err := tree.Manifest(env, "", "sha256")
	if err != nil {
		return nil, fmt.Errorf("reading the %s tree: %w", env, err)
	}

	out := map[string]string{}
	var total int64
	for _, entry := range manifest.Files {
		body, _, err := tree.Read(env, entry.Path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", entry.Path, err)
		}
		total += int64(len(body))
		if total > MaxInlineTree {
			return nil, fmt.Errorf("the %s tree is larger than %d bytes, which is more than "+
				"agentless mode sends inline. SPEC 21.1's reverse tunnel is not built; "+
				"serve a smaller tree or use an enrolled node", env, MaxInlineTree)
		}
		out[entry.Path] = string(body)
	}
	return out, nil
}

// inlinePillar compiles this target's pillar on the hub.
//
// Per target, against the grains the roster attached, so two targets
// get different pillar exactly as two enrolled nodes do — and neither
// receives the other's.
func inlinePillar(h *hubContext, t roster.Target, args *cli.Args) (json.RawMessage, []string, error) {
	if len(h.cfg.Roots("pillar_roots")) == 0 {
		return nil, nil, nil
	}
	compiled, secrets, err := compileRosterPillarSecrets(h, t, args.Flag("env", h.cfg.String("env", "base")))
	if err != nil {
		return nil, nil, err
	}
	encoded, err := value.EncodeJSON(compiled, 0)
	if err != nil {
		return nil, nil, err
	}
	return json.RawMessage(encoded), secrets, nil
}

// compileRosterPillar is one roster target's pillar, compiled on the
// hub. One function for what the target is sent and what it is targeted
// by, so the two cannot come to disagree about which pillar it has.
func compileRosterPillar(h *hubContext, t roster.Target, env string) (*value.Map, error) {
	compiled, _, err := compileRosterPillarSecrets(h, t, env)
	return compiled, err
}

// compileRosterPillarSecrets is compileRosterPillar, and the values the
// compile decrypted besides, which the target is told to redact.
func compileRosterPillarSecrets(h *hubContext, t roster.Target, env string) (*value.Map, []string, error) {
	secrets := []string{}
	seen := map[string]bool{}
	onSecret := func(v string) {
		h.secrets.Add(v)
		if !seen[v] {
			seen[v] = true
			secrets = append(secrets, v)
		}
	}
	roots := h.cfg.Roots("pillar_roots")
	grains := t.Grains
	if grains == nil {
		grains = value.NewMap(0)
	}
	compiler := &pillar.Compiler{
		Loader: fileserver.NewRoots(roots),
		Config: pillar.Config{
			NodeID: t.ID, Env: env, Grains: grains,
			TrustedGrains: h.cfg.StringSlice("pillar_trusted_grains"),
			// Agentless pillar is compiled here, so it is decrypted
			// here: without the hub's keyring an encrypted pillar fails
			// to compile, and without the redactor the values it
			// decrypts reach this process's own output unhidden.
			// DIVERGENCE 5.110.
			GPG:      gpgOptionsFor(h.cfg),
			OnSecret: onSecret,
		},
	}
	compiled := compiler.Compile()
	if err := compiled.Err(); err != nil {
		return nil, nil, fmt.Errorf("compiling pillar for %s: %w", t.ID, err)
	}
	return compiled.Pillar, secrets, nil
}

// runAcross runs against every target, bounded.
//
// Concurrent, because agentless mode's cost is the round trip and an
// estate has more than one machine — and bounded, because a hundred
// simultaneous ssh connections is a hundred processes on the hub.
func runAcross(targets []roster.Target, args *cli.Args,
	run func(roster.Target) sshexec.Result) []sshexec.Result {

	limit := cli.IntFlag(args, "ssh-concurrency", 8, 1)
	results := make([]sshexec.Result, len(targets))
	tokens := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i, t := range targets {
		wg.Add(1)
		go func(i int, t roster.Target) {
			defer wg.Done()
			tokens <- struct{}{}
			defer func() { <-tokens }()
			results[i] = run(t)
		}(i, t)
	}
	wg.Wait()
	return results
}

// reportSSH prints what each target answered.
func reportSSH(args *cli.Args, results []sshexec.Result) int {
	format, err := cli.ParseFormat(args.Flag("out", "nested"))
	if err != nil {
		cli.Fatalf("%v", err)
	}

	out := value.NewMap(len(results))
	failed := 0
	for _, result := range results {
		if result.Err != nil {
			failed++
			out.Set(result.Target.ID, result.Err.Error())
			continue
		}
		var ret job.Return
		if err := json.Unmarshal(result.Return, &ret); err != nil {
			failed++
			out.Set(result.Target.ID, "the return is not readable: "+err.Error())
			continue
		}
		if !ret.Success {
			failed++
		}
		decoded, err := value.DecodeJSON(ret.Return)
		if err != nil {
			out.Set(result.Target.ID, string(ret.Return))
			continue
		}
		out.Set(result.Target.ID, decoded)
	}

	indent := cli.IntFlag(args, "indent", 0, 0)
	if err := cli.Write(os.Stdout, out, format, indent); err != nil {
		cli.Fatalf("%v", err)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// guessNodeBinary looks beside this program.
func guessNodeBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(filepath.Dir(self), "halite-node")
	if _, err := os.Stat(candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

var sshUsage = `halite-hub ssh — run a function on a machine with no agent

Usage:
  halite-hub ssh <target> <module.function> [args...]

Flags:
  --roster <backend>   flat (default), sshconfig, cache, or ansible
  --roster-file <path> the roster, for flat, sshconfig, and ansible
  --thin <path>        the halite-node binary to push
  --clean              remove the cached binary before and after
  --ssh-concurrency <n>  how many targets at once, default 8
  --timeout <dur>      how long one target may take
  --env <name>         the environment
  --test               run every state in test mode
  -L -E -G -P -I -J -S -N -C   the target kinds of SPEC section 8
`

var sshClock job.Clock

func newSSHJobID() job.ID { return sshClock.Next() }

// rosterFromCache builds a roster from the nodes the hub has heard
// from.
//
// The use is a fleet where most machines run the agent and a few
// cannot: the same names, targeted the same way, reached over ssh.
func rosterFromCache(h *hubContext) (*roster.Roster, error) {
	cache, err := hub.OpenNodeCache(filepath.Join(
		h.cfg.String("state_dir", config.DefaultStateDir), "nodes"))
	if err != nil {
		return nil, fmt.Errorf("reading the node cache: %w", err)
	}
	names, err := cache.Known()
	if err != nil {
		return nil, err
	}
	known := make([]roster.KnownNode, 0, len(names))
	for _, name := range names {
		// The matchable form, which is what the hub already builds for
		// targeting — so an agentless run matches on exactly the
		// grains a fleet run would.
		node, err := cache.Matchable(name)
		if err != nil {
			known = append(known, roster.KnownNode{ID: name})
			continue
		}
		known = append(known, roster.KnownNode{ID: node.ID, Grains: node.Grains})
	}
	return roster.FromCache(known), nil
}
