package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/bridge"
	"github.com/edlitmus/halite/internal/cli"
)

// `extensions run`, the development loop of SPEC 24.
//
// Without it, seeing whether an extension works means building it,
// bundling it, signing it, publishing it under `_ext/`, syncing it, and
// restarting the hub — six steps for a one-line change, and the reason
// an author stops iterating and starts guessing. This runs the binary
// where it lies: no bundle, no signature, no cache, no restart.
//
// Which is also why it is not how anything reaches production. A run
// here verifies nothing about where the file came from, and says so.

// runExtensionDirectly is `halite-hub extensions run`.
func runExtensionDirectly(args *cli.Args) int {
	// Positional: `run <path> [function]`.
	if len(args.Positional) < 2 {
		fmt.Fprint(os.Stderr, extensionsUsage)
		return 2
	}
	path, err := resolveExtensionPath(args.Positional[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "halite-hub extensions run: %v\n", err)
		return 2
	}
	function := ""
	if len(args.Positional) > 2 {
		function = args.Positional[2]
	}

	opts := bridge.Options{
		Path:    path,
		Kind:    args.Flag("kind", ""),
		Timeout: parseRunTimeout(args),
		OnLog: func(level, message string) {
			fmt.Fprintf(os.Stderr, "  log   [%s] %s\n", level, message)
		},
		OnProgress: func(done, total int, message string) {
			fmt.Fprintf(os.Stderr, "  progress %d/%d %s\n", done, total, message)
		},
		OnEvent: func(tag string, data json.RawMessage) {
			fmt.Fprintf(os.Stderr, "  event %s %s\n", tag, data)
		},
		Stderr: func(line string) {
			fmt.Fprintf(os.Stderr, "  stderr| %s\n", line)
		},
	}

	granted := splitDeclared(args.Flag("declare", ""))
	if args.Bool("sandbox", false) {
		sandbox, err := bridge.From(granted, args.Flag("user", ""), args.Flag("group", ""))
		if err != nil {
			fmt.Fprintf(os.Stderr, "halite-hub extensions run: %v\n", err)
			return 2
		}
		opts.Sandbox = sandbox
	}

	pool := bridge.NewPool(opts, 1)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(context.Background(), parseRunTimeout(args)+10*time.Second)
	defer cancel()

	if err := pool.Warm(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "halite-hub extensions run: %v\n", err)
		return 1
	}
	info, _ := pool.Info()

	quiet := args.Bool("json", false)
	if !quiet {
		describeRun(info, opts.Sandbox, granted)
	}
	problems := checkHandshake(info)
	for _, p := range problems {
		fmt.Fprintf(os.Stderr, "  ! %s\n", p)
	}

	if function == "" {
		if !quiet {
			fmt.Fprintln(os.Stderr, "\nNo function named, so nothing was called.")
		}
		if len(problems) > 0 {
			return 1
		}
		return 0
	}

	kwargs, decodeErr := decodeRunJSON(args.Flag("kwargs", ""))
	if decodeErr != nil {
		fmt.Fprintf(os.Stderr, "halite-hub extensions run: --kwargs: %v\n", decodeErr)
		return 2
	}
	callArgs, decodeErr := decodeRunJSON(args.Flag("args", ""))
	if decodeErr != nil {
		fmt.Fprintf(os.Stderr, "halite-hub extensions run: --args: %v\n", decodeErr)
		return 2
	}

	if !quiet {
		fmt.Fprintf(os.Stderr, "\ncalling %s()\n", function)
	}
	value, err := pool.Call(ctx, function, callArgs, kwargs, &ext.CallContext{
		NodeID: args.Flag("node-id", "dev"),
		Env:    args.Flag("env", "base"),
		Test:   args.Bool("test", false),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  failed: %v\n", err)
		return 1
	}

	// The result to stdout, so it can be piped; everything else has
	// gone to stderr.
	var pretty any
	if jsonErr := json.Unmarshal(value, &pretty); jsonErr == nil {
		encoded, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Println(string(encoded))
	} else {
		fmt.Println(string(value))
	}
	if len(problems) > 0 {
		return 1
	}
	return 0
}

// describeRun reports what answered and what it is running under.
func describeRun(info bridge.Info, sandbox *bridge.Sandbox, granted []string) {
	fmt.Fprintf(os.Stderr, "%s %s (%s)\n", info.Name, info.Version, kindOrUnknown(info))
	if len(info.Declares) > 0 {
		fmt.Fprintf(os.Stderr, "  declares: %s\n", strings.Join(info.Declares, ", "))
	}
	for _, sig := range info.Functions {
		fmt.Fprintf(os.Stderr, "  %s()\n", sig.Function)
	}

	if sandbox == nil {
		// Said every time. An extension that works here and fails on a
		// node usually fails on something this run did not apply.
		fmt.Fprintln(os.Stderr,
			"\n  no sandbox: this runs with your identity, your limits and your network.")
		fmt.Fprintln(os.Stderr,
			"  --sandbox applies what a node would, and --declare says what it grants.")
		return
	}
	fmt.Fprintf(os.Stderr, "\n  sandbox: %s\n", strings.Join(sandbox.Describe(), "; "))
	// The gap that produces "it worked when I ran it".
	for _, declared := range info.Declares {
		if !containsString(granted, declared) {
			fmt.Fprintf(os.Stderr,
				"  ! it declares %q and this run did not grant it; pass --declare %s\n",
				declared, declared)
		}
	}
}

// kindOrUnknown names the kind, which an extension need not send.
func kindOrUnknown(info bridge.Info) string {
	if info.Kind == "" {
		return "kind not declared"
	}
	return info.Kind
}

// checkHandshake reports what an extension got wrong in the one exchange
// this command can see.
//
// Not a conformance suite — that is its own piece of work, and this
// checks the handshake and nothing else. It is here because these are
// the mistakes that surface far from their cause: a signature the host
// silently drops leaves an extension reporting no functions, and the
// operator reading that is four steps from the parameter type that
// caused it.
func checkHandshake(info bridge.Info) []string {
	var out []string
	if info.Name == "" {
		out = append(out, "it did not name itself, and the host keys everything on the name")
	}
	if info.Version == "" {
		out = append(out, "it declared no version, so it cannot be pinned")
	}
	if info.Kind != "" && !ext.ValidKind(info.Kind) {
		out = append(out, fmt.Sprintf("%q is not an extension kind; the kinds are %s",
			info.Kind, strings.Join(ext.Kinds, ", ")))
	}
	if len(info.Functions) == 0 {
		out = append(out, "it announced no functions, so nothing can call it")
	}
	for i, sig := range info.Functions {
		where := fmt.Sprintf("function %d", i+1)
		if sig.Function != "" {
			where = sig.Function + "()"
		}
		if sig.Module == "" || sig.Function == "" {
			out = append(out, where+" declares no module or no function name")
		}
		for _, p := range sig.Params {
			if p.Name == "" {
				out = append(out, where+" has a parameter with no name")
				continue
			}
			if p.Type == "" {
				// The mistake that cost this project a day: a type
				// serialised as a number arrives as no type at all.
				out = append(out, fmt.Sprintf(
					"%s: the %s parameter declares no type; a type is its name, %q and not a number",
					where, p.Name, ext.TypeString))
			}
		}
	}
	sort.Strings(out)
	return out
}

// resolveExtensionPath turns the argument into a path to a file.
//
// `exec` looks a bare name up on PATH, so `extensions run myext` went
// hunting through /usr/bin for it and reported "executable file not
// found in $PATH" about a file sitting in the working directory. The
// argument is documented as a path and is treated as one: resolved,
// and checked for being there and runnable before a process is started,
// so the error names the real problem.
func resolveExtensionPath(arg string) (string, error) {
	abs, err := filepath.Abs(arg)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("%s: %w", arg, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory; name the executable inside the bundle", arg)
	}
	// Advisory on Windows, where the execute bit does not exist and the
	// extension decides. Worth saying on unix, where a bundle checked
	// out without the bit is a common and confusing failure.
	if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable (mode %v); "+
			"a bundle carrying a script needs the bit as well as the shebang", arg, info.Mode())
	}
	return abs, nil
}

func parseRunTimeout(args *cli.Args) time.Duration {
	raw := args.Flag("timeout", "60s")
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 60 * time.Second
	}
	return d
}

// decodeRunJSON reads a --kwargs or --args value, which may be absent.
func decodeRunJSON(raw string) (any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("%q is not JSON: %w", raw, err)
	}
	return out, nil
}

func splitDeclared(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
