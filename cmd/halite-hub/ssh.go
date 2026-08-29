package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
		return 2
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
			path = filepath.Join(h.cfg.String("root", config.DefaultRoot), "roster")
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
	var out []roster.Target
	for _, t := range loaded.Targets {
		if matcher(t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// sshMatcher builds the target matcher for the roster.
//
// The same targeting grammar the fleet uses, against the roster's names
// and the grains it attached — so `-G 'os:FreeBSD'` works on an
// agentless estate exactly as it does on an enrolled one, without a
// second implementation of matching.
func sshMatcher(flag, expression string) (func(roster.Target) bool, error) {
	kind := target.Glob
	if flag != "" {
		parsed, ok := target.KindFromFlag(flag)
		if !ok {
			return nil, fmt.Errorf("-%s is not a target kind", flag)
		}
		kind = parsed
	}
	matcher, err := target.Compile(kind, expression, nil)
	if err != nil {
		return nil, err
	}
	return func(t roster.Target) bool {
		return matcher.Match(target.Node{ID: t.ID, Grains: t.Grains})
	}, nil
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
		pillar, err := inlinePillar(h, t, args)
		if err != nil {
			return nil, err
		}
		if pillar != nil {
			req["pillar"] = pillar
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
func inlinePillar(h *hubContext, t roster.Target, args *cli.Args) (json.RawMessage, error) {
	roots := h.cfg.Roots("pillar_roots")
	if len(roots) == 0 {
		return nil, nil
	}
	env := args.Flag("env", h.cfg.String("env", "base"))
	grains := t.Grains
	if grains == nil {
		grains = value.NewMap(0)
	}
	compiler := &pillar.Compiler{
		Loader: fileserver.NewRoots(roots),
		Config: pillar.Config{
			NodeID: t.ID, Env: env, Grains: grains,
			TrustedGrains: h.cfg.StringSlice("pillar_trusted_grains"),
		},
	}
	compiled := compiler.Compile()
	if err := compiled.Err(); err != nil {
		return nil, fmt.Errorf("compiling pillar for %s: %w", t.ID, err)
	}
	encoded, err := value.EncodeJSON(compiled.Pillar, 0)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

// runAcross runs against every target, bounded.
//
// Concurrent, because agentless mode's cost is the round trip and an
// estate has more than one machine — and bounded, because a hundred
// simultaneous ssh connections is a hundred processes on the hub.
func runAcross(targets []roster.Target, args *cli.Args,
	run func(roster.Target) sshexec.Result) []sshexec.Result {

	limit := 8
	fmt.Sscanf(args.Flag("ssh-concurrency", "8"), "%d", &limit)
	if limit <= 0 {
		limit = 8
	}
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

	indent := 0
	fmt.Sscanf(args.Flag("indent", "0"), "%d", &indent)
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
