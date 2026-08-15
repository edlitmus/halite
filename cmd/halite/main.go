// halite is a masterless, Salt-inspired configuration management tool.
//
//	halite grains [-json]                    show system grains
//	halite apply [-test] [-json] [-root DIR] [target ...]
//	    no target:  highstate from <root>/top.sls
//	    file path:  apply that SLS file
//	    dotted name(s): apply <root>/<name>.sls (or <name>/init.sls)
//	halite call [-test] module.fn k=v        run a single state function
//	halite show [-json] [target ...]         print the compiled plan without
//	                                         running any of it
//	halite pillar [-json]                    show the pillar data for this host
//	halite parse [-root DIR] [PATH]          report what halite can use in an
//	                                         existing Salt state/pillar tree
//	halite key <subcommand>                  manage the fleet CA
//	halite version
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/engine"
	"github.com/edlitmus/halite/internal/extmod"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/sls"
)

const version = "0.10.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "grains":
		cmdGrains(os.Args[2:])
	case "apply":
		cmdApply(os.Args[2:])
	case "call":
		cmdCall(os.Args[2:])
	case "pillar":
		cmdPillar(os.Args[2:])
	case "parse":
		cmdParse(os.Args[2:])
	case "show":
		cmdShow(os.Args[2:])
	case "key":
		cmdKey(os.Args[2:])
	case "master":
		cmdMaster(os.Args[2:])
	case "agent":
		cmdAgent(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "agents":
		cmdAgents(os.Args[2:])
	case "ssh":
		cmdSSH(os.Args[2:])
	case "events":
		cmdEvents(os.Args[2:])
	case "mine":
		cmdMine(os.Args[2:])
	case "orchestrate":
		cmdOrchestrate(os.Args[2:])
	case "version":
		fmt.Println("halite " + version)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  halite grains [-json]                    show system grains
  halite grains set <key>=<value> ...      write static custom grains
  halite grains unset <key> ...            remove static custom grains
  halite apply [-test] [-json] [-root DIR] [target ...]
      no target: highstate from <root>/top.sls
      target:    an SLS file path, or dotted sls name(s) under the root
  halite call [-test] <module.fn> [k=v ...] run a single state function
  halite show [-json] [-root DIR] [target ...]
                                           print the compiled plan, unrun
  halite pillar [-json] [-pillar-root DIR] show the pillar data for this host
  halite parse [-root DIR] [PATH]          check an existing state/pillar tree
      [-pillar-root DIR] [-json] [-errors] for what halite can use as written
  halite key <subcommand>                  manage the fleet CA ('halite key help')
  halite version

fleet mode:
  halite master [-addr :4506] [-root DIR]  run the control plane
      [-returner file:PATH|webhook:URL]    durable result sinks (repeatable)
      [-reactor FILE]                      rules turning events into jobs
      [-orch-root DIR]                     orchestration files
  halite agent -master HOST                run the agent on a managed host
      [-beacons FILE] [-mine FILE]         watches, and facts to publish
      [-schedule FILE]                     work it runs on its own clock
  halite run <target> <kind> [args]        dispatch work and collect results
  halite agents                            list the fleet
  halite events [-tag PATTERN]             tail the event bus
  halite mine [function] [-target PAT]     read the fleet's published facts
  halite orchestrate <name>                run ordered fleet-wide steps

agentless:
  halite ssh <hosts> <kind> [args]         push the binary over ssh and run`)
}

// configDir is where the daemon configuration files live: beside the
// state tree, with the PKI directory and everything else halite owns.
func configDir() string {
	return filepath.Dir(defaultRoot())
}

// loadDaemonConfig reads a daemon's configuration file and applies it to
// the flags that were not given on the command line. `-config` names a
// file; without it the platform path is read, and a missing file there is
// simply no configuration.
func loadDaemonConfig(fs *flag.FlagSet, daemon, flagValue string) {
	path := flagValue
	if path == "" {
		path = config.DefaultPath(daemon, configDir())
	}
	cfg, err := config.Load(path)
	if err != nil {
		fatal("%v", err)
	}
	if err := cfg.Apply(fs); err != nil {
		fatal("%v", err)
	}
}

// defaultRoot is the state tree location when -root and HALITE_ROOT are
// unset.
func defaultRoot() string {
	switch runtime.GOOS {
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return "/usr/local/etc/halite/states"
	case "windows":
		return `C:\ProgramData\halite\states`
	default:
		return "/etc/halite/states"
	}
}

// parseFlags parses args and returns the positional ones. Unlike a plain
// FlagSet.Parse, flags may appear after or between positional arguments, so
// both `halite apply -test web.nginx` and `halite apply web.nginx -test`
// work.
func parseFlags(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		_ = fs.Parse(args) // the flag set is ExitOnError
		if fs.NArg() == 0 {
			return positional
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// resolveRoot returns the state tree root: flag, then $HALITE_ROOT, then the
// platform default.
func resolveRoot(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("HALITE_ROOT"); env != "" {
		return env
	}
	return defaultRoot()
}

// resolvePillarRoot returns the pillar tree root: flag, then
// $HALITE_PILLAR_ROOT, then a "pillar" directory beside the state tree.
func resolvePillarRoot(flagValue, statesRoot string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("HALITE_PILLAR_ROOT"); env != "" {
		return env
	}
	return filepath.Join(filepath.Dir(statesRoot), "pillar")
}

// warnPillarPermissions reports a pillar tree that is readable beyond its
// owner. Pillar confidentiality rests on the directory mode, so this is the
// one check that makes the documented posture verifiable.
func warnPillarPermissions(root string) {
	if warning := pillar.PermissionWarning(root); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
}

// loadPillar reads pillar data from a pre-rendered JSON file when one is
// given, and from the pillar tree otherwise. `halite ssh` renders pillar on
// the operator's machine and ships only the result, so a remote host never
// sees the whole tree.
func loadPillar(jsonPath, pillarRoot string, g map[string]any) (map[string]any, error) {
	if jsonPath == "" {
		return (&pillar.Loader{Root: pillarRoot, Grains: g}).Load()
	}
	b, err := os.ReadFile(jsonPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", jsonPath, err)
	}
	data := map[string]any{}
	if err := json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("parse %s: %w", jsonPath, err)
	}
	return data, nil
}

func cmdPillar(args []string) {
	fs := flag.NewFlagSet("pillar", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	rootFlag := fs.String("root", "", "state tree root (used to locate the pillar tree)")
	pillarRootFlag := fs.String("pillar-root", "", "pillar tree root (default: $HALITE_PILLAR_ROOT or <root>/../pillar)")
	_ = parseFlags(fs, args)

	pillarRoot := resolvePillarRoot(*pillarRootFlag, resolveRoot(*rootFlag))
	warnPillarPermissions(pillarRoot)
	data, err := (&pillar.Loader{Root: pillarRoot, Grains: grains.Collect()}).Load()
	if err != nil {
		fatal("%v", err)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(data, "", "  ")
		fmt.Println(string(b))
		return
	}
	printTree(data, "")
}

// printTree renders nested pillar data as indented key: value lines.
func printTree(data map[string]any, indent string) {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch v := data[k].(type) {
		case map[string]any:
			fmt.Printf("%s%s:\n", indent, stripControl(k))
			printTree(v, indent+"  ")
		case []any:
			fmt.Printf("%s%s:\n", indent, stripControl(k))
			for _, item := range v {
				fmt.Printf("%s  - %s\n", indent, stripControl(fmt.Sprintf("%v", item)))
			}
		default:
			fmt.Printf("%s%s: %s\n", indent, stripControl(k), stripControl(fmt.Sprintf("%v", v)))
		}
	}
}

func cmdGrains(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "set":
			cmdGrainsSet(args[1:])
			return
		case "unset":
			cmdGrainsUnset(args[1:])
			return
		}
	}
	fs := flag.NewFlagSet("grains", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	grainsFile := fs.String("file", "", "static grains file (default: $HALITE_GRAINS or the platform default)")
	_ = fs.Parse(args)
	g := grains.CollectFrom(grainsPath(*grainsFile))
	if *asJSON {
		b, _ := json.MarshalIndent(g, "", "  ")
		fmt.Println(string(b))
		return
	}
	keys := make([]string, 0, len(g))
	for k := range g {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s: %v\n", k, g[k])
	}
}

// grainsPath resolves the static grains file: the flag, then the package
// default ($HALITE_GRAINS or the platform path).
func grainsPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return grains.CustomPath()
}

// cmdGrainsSet writes static grains, the equivalent of Salt's
// grains.setval. The file is plain YAML, so a fleet can also manage it with
// file.managed and skip this entirely.
func cmdGrainsSet(args []string) {
	fs := flag.NewFlagSet("grains set", flag.ExitOnError)
	grainsFile := fs.String("file", "", "static grains file (default: $HALITE_GRAINS or the platform default)")
	pairs := parseFlags(fs, args)
	if len(pairs) == 0 {
		fatal("usage: halite grains set <key>=<value> [<key>=<value> ...]")
	}
	path := grainsPath(*grainsFile)
	data, err := grains.LoadCustom(path)
	if err != nil {
		fatal("%v", err)
	}
	if data == nil {
		data = map[string]any{}
	}
	for _, pair := range pairs {
		k, v, ok := strings.Cut(pair, "=")
		if !ok || k == "" {
			fatal("argument %q is not key=value", pair)
		}
		data[k] = v
	}
	if err := grains.SaveCustom(path, data); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("wrote %s\n", path)
}

// cmdGrainsUnset removes static grains by name.
func cmdGrainsUnset(args []string) {
	fs := flag.NewFlagSet("grains unset", flag.ExitOnError)
	grainsFile := fs.String("file", "", "static grains file (default: $HALITE_GRAINS or the platform default)")
	names := parseFlags(fs, args)
	if len(names) == 0 {
		fatal("usage: halite grains unset <key> [<key> ...]")
	}
	path := grainsPath(*grainsFile)
	data, err := grains.LoadCustom(path)
	if err != nil {
		fatal("%v", err)
	}
	for _, name := range names {
		if _, ok := data[name]; !ok {
			fatal("%s is not a static grain in %s", name, path)
		}
		delete(data, name)
	}
	if err := grains.SaveCustom(path, data); err != nil {
		fatal("%v", err)
	}
	fmt.Printf("wrote %s\n", path)
}

// parseCallArgs splits a `halite call` command line into the function name,
// the -test flag (which may appear anywhere, like every other subcommand),
// and the key=value arguments.
func parseCallArgs(args []string) (name string, test bool, callArgs map[string]any, err error) {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	testFlag := fs.Bool("test", false, "dry run: report changes without applying")
	rest := parseFlags(fs, args)
	if len(rest) < 1 {
		return "", false, nil, fmt.Errorf("usage: halite call [-test] <module.fn> [key=value ...]")
	}
	callArgs = map[string]any{}
	for _, kv := range rest[1:] {
		var k, v string
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				k, v = kv[:i], kv[i+1:]
				break
			}
		}
		if k == "" {
			return "", false, nil, fmt.Errorf("argument %q is not key=value", kv)
		}
		callArgs[k] = v
	}
	return rest[0], *testFlag, callArgs, nil
}

func cmdCall(args []string) {
	name, test, callArgs, err := parseCallArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	root := resolveRoot("")
	fn, isState := extmod.Lookup(filepath.Join(root, extmod.DirName))(name)
	execFn, isExec := modules.ExecRegistry[name]
	if !isState && !isExec {
		fmt.Fprintf(os.Stderr, "unknown function %q (execution modules: %s)\n",
			name, strings.Join(modules.ExecNames(), ", "))
		os.Exit(2)
	}
	g := grains.Collect()
	p, err := (&pillar.Loader{Root: resolvePillarRoot("", root), Grains: g}).Load()
	if err != nil {
		fatal("%v", err)
	}
	ctx := &modules.Ctx{Test: test, Grains: g, Pillar: p}
	if isExec {
		// Execution modules only read, so -test changes nothing here.
		data, err := execFn(ctx, callArgs)
		if err != nil {
			fatal("%v", err)
		}
		printTree(data, "")
		return
	}

	var r modules.Result
	if comment, gated := modules.CheckGates(callArgs); gated {
		r = modules.Result{Ok: true, Comment: comment}
	} else {
		r = fn(ctx, name, callArgs)
	}
	printResult(name, name, r)
	if !r.Ok {
		os.Exit(1)
	}
}

// rootIsExplicit reports whether the operator named the state tree, rather
// than falling back to the platform default.
func rootIsExplicit(flagValue string) bool {
	return flagValue != "" || os.Getenv("HALITE_ROOT") != ""
}

// loadPlan compiles the states a command's positional targets name, under
// the rules every such command shares: no target is a highstate, a single
// file path is that file, and anything else is dotted SLS names under the
// root.
func loadPlan(targets []string, root string, rootExplicit bool,
	g, p map[string]any) ([]sls.State, error) {
	switch {
	case len(targets) == 0:
		return (&sls.Loader{Root: root, Grains: g, Pillar: p}).LoadTop()
	case len(targets) == 1 && isFile(targets[0]):
		fileRoot := root
		if !rootExplicit {
			// Single-file apply: includes resolve next to the file unless a
			// root was given explicitly.
			fileRoot = filepath.Dir(targets[0])
		}
		return (&sls.Loader{Root: fileRoot, Grains: g, Pillar: p}).LoadPath(targets[0])
	default:
		for _, t := range targets {
			if isFile(t) {
				return nil, fmt.Errorf("mixing file paths and sls names is not supported (got %q)", t)
			}
		}
		return (&sls.Loader{Root: root, Grains: g, Pillar: p}).LoadNames(targets)
	}
}

func cmdApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	test := fs.Bool("test", false, "dry run: report changes without applying")
	asJSON := fs.Bool("json", false, "output results as JSON")
	rootFlag := fs.String("root", "", "state tree root (default: $HALITE_ROOT or the platform default)")
	pillarRootFlag := fs.String("pillar-root", "", "pillar tree root (default: $HALITE_PILLAR_ROOT or <root>/../pillar)")
	pillarJSON := fs.String("pillar-json", "", "read pillar from a JSON file instead of a tree (used by 'halite ssh')")
	targets := parseFlags(fs, args)

	root := resolveRoot(*rootFlag)
	pillarRoot := resolvePillarRoot(*pillarRootFlag, root)
	if *pillarJSON == "" {
		warnPillarPermissions(pillarRoot)
	}

	g := grains.Collect()
	p, err := loadPillar(*pillarJSON, pillarRoot, g)
	if err != nil {
		fatal("%v", err)
	}

	states, err := loadPlan(targets, root, rootIsExplicit(*rootFlag), g, p)
	if err != nil {
		fatal("%v", err)
	}

	ctx := &modules.Ctx{Test: *test, Grains: g, Pillar: p}
	results := engine.RunWith(ctx, states, extmod.Lookup(filepath.Join(root, extmod.DirName)))

	succeeded, failed, changed := 0, 0, 0
	var jsonResults []map[string]any
	for _, r := range results {
		if !*asJSON {
			printResult(r.ID, r.Fn, r.Res)
		} else {
			jsonResults = append(jsonResults, map[string]any{
				"id": r.ID, "function": r.Fn, "result": r.Res.Ok,
				"changed": r.Res.Changed, "comment": r.Res.Comment, "changes": r.Res.Changes,
			})
		}
		if r.Res.Ok {
			succeeded++
			if r.Res.Changed {
				changed++
			}
		} else {
			failed++
		}
	}

	if *asJSON {
		out := map[string]any{
			"results": jsonResults,
			"summary": map[string]any{
				"succeeded": succeeded, "failed": failed,
				"changed": changed, "total": len(states), "test": *test,
			},
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		if failed > 0 {
			os.Exit(1)
		}
		return
	}

	fmt.Println("\nSummary")
	fmt.Println("------------")
	fmt.Printf("Succeeded: %d (changed=%d)\n", succeeded, changed)
	fmt.Printf("Failed:    %d\n", failed)
	fmt.Println("------------")
	fmt.Printf("Total states run: %d\n", len(states))
	if *test {
		fmt.Println("(test mode: no changes were made)")
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func printResult(id, fn string, r modules.Result) {
	b := func(v bool) string {
		if v {
			return "True"
		}
		return "False"
	}
	// Comments and changes can carry remote output; keep the terminal safe.
	fmt.Println("----------")
	fmt.Printf("      ID: %s\n", stripControl(id))
	fmt.Printf("Function: %s\n", stripControl(fn))
	fmt.Printf("  Result: %s\n", b(r.Ok))
	fmt.Printf(" Comment: %s\n", stripControl(r.Comment))
	if len(r.Changes) > 0 {
		fmt.Println(" Changes:")
		keys := make([]string, 0, len(r.Changes))
		for k := range r.Changes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := stripControl(r.Changes[k])
			if strings.Contains(v, "\n") {
				fmt.Printf("   %s: |\n", stripControl(k))
				for _, l := range strings.Split(v, "\n") {
					fmt.Printf("     %s\n", l)
				}
				continue
			}
			fmt.Printf("   %s: %s\n", stripControl(k), v)
		}
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
