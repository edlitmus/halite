package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/archive"
	"github.com/edlitmus/halite/internal/pillar"
)

const sshUsage = `usage: halite ssh <hosts> <kind> [args] [flags]

hosts:
  web1,web2                comma-separated ssh destinations
  '*' or 'web*'            a glob over the roster (needs -roster)

kinds:
  state.highstate          converge against the local top.sls
  state.apply <sls> ...    apply named sls files
  call <module.fn> [k=v]   run one state function or execution module
  grains                   report the host's facts

flags:
  -roster FILE   one ssh destination per line; # comments allowed
  -o OPTION      passed through to ssh and scp; repeatable
                 (-o ProxyJump=bastion -o Port=2222)
  -binary PATH   the halite binary to push (default: matched from -dist)
  -dist DIR      directory of cross-built binaries (default: ./dist)
  -jobs N        hosts to work on at once (default 8)
  -test          dry run
  -json          machine-readable output
  -root DIR      state tree to ship
  -pillar-root DIR

halite ssh needs no agent and no control plane: it copies one static
binary, ships the state tree, runs it, and collects JSON. Everything goes
through your ssh(1), so ssh_config, agents, and jump hosts all apply.`

// sshHost is one target and the outcome of working on it.
type sshHost struct {
	Dest   string          `json:"host"`
	Ok     bool            `json:"result"`
	Error  string          `json:"error,omitempty"`
	Output json.RawMessage `json:"output,omitempty"`
}

func cmdSSH(args []string) {
	fs := flag.NewFlagSet("ssh", flag.ExitOnError)
	roster := fs.String("roster", "", "file of ssh destinations, one per line")
	binaryFlag := fs.String("binary", "", "halite binary to push")
	distDir := fs.String("dist", "dist", "directory of cross-built halite binaries")
	jobs := fs.Int("jobs", 8, "hosts to work on at once")
	test := fs.Bool("test", false, "dry run")
	asJSON := fs.Bool("json", false, "output as JSON")
	rootFlag := fs.String("root", "", "state tree root to ship")
	pillarRootFlag := fs.String("pillar-root", "", "pillar tree root")
	timeout := fs.Duration("timeout", 10*time.Minute, "per-host time limit")
	var sshOptions stringList
	fs.Var(&sshOptions, "o", "ssh option passed through to ssh and scp (repeatable)")
	rest := parseFlags(fs, args)

	if len(rest) < 2 {
		fmt.Fprintln(os.Stderr, sshUsage)
		os.Exit(2)
	}
	if runtime.GOOS == "windows" {
		fatal("halite ssh needs ssh(1) and scp(1); run it from a unix host")
	}
	hosts, err := resolveHosts(rest[0], *roster)
	if err != nil {
		fatal("%v", err)
	}
	remoteArgs, err := remoteCommand(rest[1:], *test)
	if err != nil {
		fatal("%v\n\n%s", err, sshUsage)
	}

	root := resolveRoot(*rootFlag)
	pillarRoot := resolvePillarRoot(*pillarRootFlag, root)
	needsTree := strings.HasPrefix(remoteArgs[0], "state.") || remoteArgs[0] == "apply"

	if needsTree {
		warnPillarPermissions(pillarRoot)
	}

	ctx, stop := signalContext()
	defer stop()

	runner := &sshRunner{
		binary:     *binaryFlag,
		dist:       *distDir,
		statesRoot: root,
		pillarRoot: pillarRoot,
		needsTree:  needsTree,
		timeout:    *timeout,
		options:    sshOptions,
	}
	results := runner.runAll(ctx, hosts, remoteArgs, *jobs)

	if *asJSON {
		b, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			fatal("encode results: %v", err)
		}
		fmt.Println(string(b))
	} else {
		printSSHResults(results)
	}
	for _, r := range results {
		if !r.Ok {
			os.Exit(1)
		}
	}
}

// resolveHosts turns the host spec into ssh destinations, globbing against
// the roster when one is given.
func resolveHosts(spec, rosterPath string) ([]string, error) {
	var roster []string
	if rosterPath != "" {
		b, err := os.ReadFile(rosterPath)
		if err != nil {
			return nil, fmt.Errorf("read roster: %w", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			roster = append(roster, line)
		}
	}

	if roster == nil {
		hosts := splitList(spec)
		if len(hosts) == 0 {
			return nil, fmt.Errorf("no hosts given (and no -roster to glob)")
		}
		if err := rejectOptionDests(hosts); err != nil {
			return nil, err
		}
		return hosts, nil
	}

	var matched []string
	for _, entry := range roster {
		for _, pattern := range splitList(spec) {
			if ok, _ := filepath.Match(pattern, hostName(entry)); ok {
				matched = append(matched, entry)
				break
			}
		}
	}
	if len(matched) == 0 {
		return nil, fmt.Errorf("no roster entry matches %q", spec)
	}
	if err := rejectOptionDests(matched); err != nil {
		return nil, err
	}
	return matched, nil
}

// rejectOptionDests refuses destinations that ssh and scp would parse as
// options: a roster line like -oProxyCommand=... would otherwise run a
// command on the operator's machine, not connect to a host.
func rejectOptionDests(hosts []string) error {
	for _, h := range hosts {
		if strings.HasPrefix(h, "-") {
			return fmt.Errorf("host %q begins with '-' and would be parsed as an ssh option", h)
		}
	}
	return nil
}

func splitList(spec string) []string {
	var out []string
	for _, item := range strings.Split(spec, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// hostName is the destination without its user@ prefix, which is what
// roster globs match against.
func hostName(dest string) string {
	if _, host, found := strings.Cut(dest, "@"); found {
		return host
	}
	return dest
}

// remoteCommand turns the local command line into the arguments the pushed
// binary will be run with.
func remoteCommand(args []string, test bool) ([]string, error) {
	kind, rest := args[0], args[1:]
	switch kind {
	case "state.highstate":
		if len(rest) > 0 {
			return nil, fmt.Errorf("state.highstate takes no arguments")
		}
		out := []string{"state.highstate", "apply"}
		return appendTest(out, test), nil
	case "state.apply":
		if len(rest) == 0 {
			return nil, fmt.Errorf("state.apply needs at least one sls name")
		}
		return appendTest(append([]string{"state.apply", "apply"}, rest...), test), nil
	case "call":
		if len(rest) == 0 || !strings.Contains(rest[0], ".") {
			return nil, fmt.Errorf("call needs a module.function")
		}
		out := append([]string{"call", "call"}, rest...)
		if test {
			// No -json here: `halite call` prints a human block, which
			// runOne wraps as JSON text. Only the dry-run flag crosses over.
			out = append(out, "-test")
		}
		return out, nil
	case "grains":
		return []string{"grains", "grains", "-json"}, nil
	default:
		return nil, fmt.Errorf("unknown kind %q", kind)
	}
}

func appendTest(args []string, test bool) []string {
	args = append(args, "-json")
	if test {
		args = append(args, "-test")
	}
	return args
}

// sshRunner works on one host at a time, in parallel across hosts.
type sshRunner struct {
	binary     string
	dist       string
	statesRoot string
	pillarRoot string
	needsTree  bool
	timeout    time.Duration
	options    []string
}

// stringList collects a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// sshArgv builds the ssh/scp argument list, putting BatchMode first so an
// operator's -o can override it if they really mean to. The `--` ends option
// parsing, so a destination or path can never be mistaken for an option.
func sshArgv(options []string, tail ...string) []string {
	argv := []string{"-o", "BatchMode=yes"}
	for _, option := range options {
		argv = append(argv, "-o", option)
	}
	argv = append(argv, "--")
	return append(argv, tail...)
}

func (r *sshRunner) runAll(ctx context.Context, hosts, remoteArgs []string, parallel int) []sshHost {
	if parallel < 1 {
		parallel = 1
	}
	results := make([]sshHost, len(hosts))
	slots := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	for i, dest := range hosts {
		wg.Add(1)
		go func(i int, dest string) {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				results[i] = sshHost{Dest: dest, Error: "cancelled"}
				return
			}
			hostCtx, cancel := context.WithTimeout(ctx, r.timeout)
			defer cancel()
			results[i] = r.runOne(hostCtx, dest, remoteArgs)
		}(i, dest)
	}
	wg.Wait()
	return results
}

// runOne pushes the binary and the tree, runs the command, and cleans up.
// The remote work happens in a temporary directory that is always removed,
// so a failed run leaves nothing behind but the cached binary.
func (r *sshRunner) runOne(ctx context.Context, dest string, remoteArgs []string) sshHost {
	result := sshHost{Dest: dest}
	kind, argv := remoteArgs[0], remoteArgs[1:]

	remoteOS, remoteArch, err := r.probe(ctx, dest)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	binary, err := r.binaryFor(remoteOS, remoteArch)
	if err != nil {
		result.Error = err.Error()
		return result
	}

	workDir, err := sshCapture(ctx, r.options, dest, "mktemp -d /tmp/halite.XXXXXX")
	if err != nil {
		result.Error = fmt.Sprintf("create work directory: %v", err)
		return result
	}
	workDir = strings.TrimSpace(workDir)
	defer func() {
		// A best-effort cleanup on a context that is already dead would be a
		// no-op, so give it its own short deadline.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = sshCapture(cleanupCtx, r.options, dest, "rm -rf "+shellQuote(workDir))
	}()

	remoteBinary := workDir + "/halite"
	if err := scpTo(ctx, r.options, binary, dest, remoteBinary); err != nil {
		result.Error = fmt.Sprintf("copy binary: %v", err)
		return result
	}
	if _, err := sshCapture(ctx, r.options, dest, "chmod 0700 "+shellQuote(remoteBinary)); err != nil {
		result.Error = fmt.Sprintf("chmod binary: %v", err)
		return result
	}

	if r.needsTree {
		if err := r.shipTree(ctx, dest, workDir, remoteBinary); err != nil {
			result.Error = err.Error()
			return result
		}
		argv = append(argv,
			"-root", workDir+"/states",
			"-pillar-json", workDir+"/pillar.json")
	}

	command := shellQuote(remoteBinary)
	for _, a := range argv {
		command += " " + shellQuote(a)
	}
	out, err := sshCapture(ctx, r.options, dest, command)
	if err != nil {
		result.Error = strings.TrimSpace(err.Error())
		if trimmed := strings.TrimSpace(out); trimmed != "" {
			result.Output = json.RawMessage(asJSONText(trimmed))
		}
		return result
	}
	result.Ok = true
	result.Output = remoteOutput(kind, out)
	return result
}

// remoteOutput stores what the host printed while keeping the fleet report
// valid JSON: `halite call` prints a human block, and any host can leak stray
// text (a motd, profile noise), so anything that is not JSON is wrapped as a
// JSON string rather than poisoning the whole report.
func remoteOutput(kind, out string) json.RawMessage {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil
	}
	if kind == "call" || !json.Valid([]byte(trimmed)) {
		return json.RawMessage(asJSONText(trimmed))
	}
	return json.RawMessage(trimmed)
}

// shipTree renders this host's pillar locally and copies both it and the
// state tree over. Pillar is rendered here, from the host's own grains, so
// a managed host never receives another host's data.
func (r *sshRunner) shipTree(ctx context.Context, dest, workDir, remoteBinary string) error {
	rawGrains, err := sshCapture(ctx, r.options, dest, shellQuote(remoteBinary)+" grains -json")
	if err != nil {
		return fmt.Errorf("collect grains: %v", err)
	}
	hostGrains := map[string]any{}
	if err := json.Unmarshal([]byte(rawGrains), &hostGrains); err != nil {
		return fmt.Errorf("parse grains: %v", err)
	}
	// Pillar targets the host's own grains, not the roster entry, so pillar
	// top files and state templates agree about who this host is. A roster
	// name is an ssh destination, nothing more.
	data, err := (&pillar.Loader{Root: r.pillarRoot, Grains: hostGrains}).Load()
	if err != nil {
		return fmt.Errorf("render pillar: %v", err)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	// Pillar carries whatever secrets the tree holds. mktemp -d already made
	// the working directory 0700, but the file itself is created under the
	// login shell's umask, so tighten it for this one write.
	writePillar := "umask 077 && cat > " + shellQuote(workDir+"/pillar.json")
	if err := sshFeed(ctx, r.options, dest, writePillar, bytes.NewReader(encoded)); err != nil {
		return fmt.Errorf("copy pillar: %v", err)
	}

	var tree bytes.Buffer
	if err := archive.PackDir(r.statesRoot, &tree); err != nil {
		return fmt.Errorf("pack state tree: %v", err)
	}
	extract := fmt.Sprintf("mkdir -p %s && tar -xzf - -C %s",
		shellQuote(workDir+"/states"), shellQuote(workDir+"/states"))
	if err := sshFeed(ctx, r.options, dest, extract, &tree); err != nil {
		return fmt.Errorf("copy state tree: %v", err)
	}
	return nil
}

// probe reports the remote platform so the right binary gets pushed.
func (r *sshRunner) probe(ctx context.Context, dest string) (goos, goarch string, err error) {
	out, err := sshCapture(ctx, r.options, dest, "uname -s; uname -m")
	if err != nil {
		return "", "", fmt.Errorf("probe: %v", err)
	}
	lines := strings.Fields(out)
	if len(lines) < 2 {
		return "", "", fmt.Errorf("probe: unexpected uname output %q", strings.TrimSpace(out))
	}
	return strings.ToLower(lines[0]), normalizeArch(lines[1]), nil
}

func normalizeArch(machine string) string {
	switch strings.ToLower(machine) {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return strings.ToLower(machine)
	}
}

// binaryFor picks the binary to push: an explicit -binary, a cross-built
// one from -dist, or this very executable when the platform matches.
func (r *sshRunner) binaryFor(goos, goarch string) (string, error) {
	if r.binary != "" {
		return r.binary, nil
	}
	candidate := filepath.Join(r.dist, fmt.Sprintf("halite-%s-%s", goos, goarch))
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		self, err := os.Executable()
		if err == nil {
			return self, nil
		}
	}
	return "", fmt.Errorf("no binary for %s/%s: build one with 'make cross' or pass -binary", goos, goarch)
}

func sshCapture(ctx context.Context, options []string, dest, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", sshArgv(options, dest, command)...)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return out.String(), fmt.Errorf("%s", msg)
		}
		return out.String(), err
	}
	return out.String(), nil
}

// sshFeed runs a remote command with local data on its stdin.
func sshFeed(ctx context.Context, options []string, dest, command string, stdin io.Reader) error {
	cmd := exec.CommandContext(ctx, "ssh", sshArgv(options, dest, command)...)
	cmd.Stdin = stdin
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}

func scpTo(ctx context.Context, options []string, local, dest, remote string) error {
	cmd := exec.CommandContext(ctx, "scp", append([]string{"-q"}, sshArgv(options, local, dest+":"+remote)...)...)
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		return err
	}
	return nil
}

// shellQuote makes a string safe as a single word in the remote sh command
// that ssh runs. Every argument crossing to the remote shell goes through
// it, including paths derived from the roster.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// stripControl removes terminal control characters so a compromised remote
// host cannot drive the operator's terminal with escape sequences. Tabs and
// newlines survive; the callers manage line structure themselves.
func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// asJSONText wraps non-JSON output so the -json form stays valid JSON.
func asJSONText(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

func printSSHResults(results []sshHost) {
	sorted := append([]sshHost(nil), results...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Dest < sorted[j].Dest })

	failed := 0
	for _, r := range sorted {
		fmt.Printf("%s:\n", r.Dest)
		if r.Error != "" {
			fmt.Printf("    error: %s\n", stripControl(r.Error))
			failed++
		}
		if len(r.Output) > 0 {
			printRemoteOutput(r.Output)
		}
		fmt.Println()
	}
	fmt.Printf("%d host(s), %d failed\n", len(sorted), failed)
}

// printRemoteOutput renders a remote `-json` payload as the familiar state
// blocks, falling back to raw text for anything else.
func printRemoteOutput(raw json.RawMessage) {
	var applyOutput struct {
		Results []struct {
			ID       string            `json:"id"`
			Function string            `json:"function"`
			Result   bool              `json:"result"`
			Changed  bool              `json:"changed"`
			Comment  string            `json:"comment"`
			Changes  map[string]string `json:"changes"`
		} `json:"results"`
		Summary map[string]any `json:"summary"`
	}
	if err := json.Unmarshal(raw, &applyOutput); err == nil && len(applyOutput.Results) > 0 {
		for _, r := range applyOutput.Results {
			printResult(r.ID, r.Function, moduleResult(r.Result, r.Changed, r.Comment, r.Changes))
		}
		fmt.Printf("  Summary: succeeded=%v (changed=%v) failed=%v\n",
			applyOutput.Summary["succeeded"], applyOutput.Summary["changed"], applyOutput.Summary["failed"])
		return
	}

	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err == nil {
		printTree(tree, "    ")
		return
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			fmt.Printf("    %s\n", stripControl(line))
		}
		return
	}
	fmt.Printf("    %s\n", stripControl(string(raw)))
}
