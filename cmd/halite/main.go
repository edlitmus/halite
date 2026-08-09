// halite is a masterless, Salt-inspired configuration management tool.
//
//	halite grains [-json]        show system grains
//	halite apply [-test] file    apply an SLS state file (like salt-call --local state.apply)
//	halite call module.fn k=v    run a single state function (like salt-call --local)
//	halite version
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/yamlite"
)

const version = "0.2.0"

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
  halite grains [-json]              show system grains
  halite apply [-test] [-json] <file.sls>  apply a state file
  halite call <module.fn> [k=v ...]  run a single state function
  halite version`)
}

func cmdGrains(args []string) {
	fs := flag.NewFlagSet("grains", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output as JSON")
	_ = fs.Parse(args)
	g := grains.Collect()
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

func cmdCall(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: halite call <module.fn> [key=value ...]")
		os.Exit(2)
	}
	name := args[0]
	fn, ok := modules.Registry[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown state function %q\n", name)
		os.Exit(2)
	}
	callArgs := map[string]any{}
	for _, kv := range args[1:] {
		var k, v string
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				k, v = kv[:i], kv[i+1:]
				break
			}
		}
		if k == "" {
			fmt.Fprintf(os.Stderr, "argument %q is not key=value\n", kv)
			os.Exit(2)
		}
		callArgs[k] = v
	}
	ctx := &modules.Ctx{Grains: grains.Collect()}
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

func cmdApply(args []string) {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	test := fs.Bool("test", false, "dry run: report changes without applying")
	asJSON := fs.Bool("json", false, "output results as JSON")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: halite apply [-test] <file.sls>")
		os.Exit(2)
	}
	path := fs.Arg(0)
	src, err := os.ReadFile(path)
	if err != nil {
		fatal("read %s: %v", path, err)
	}
	g := grains.Collect()
	rendered, err := sls.Render(filepath.Base(path), string(src), sls.TemplateData{Grains: g})
	if err != nil {
		fatal("%s: %v", path, err)
	}
	tree, err := yamlite.Parse(rendered)
	if err != nil {
		fatal("%s: %v", path, err)
	}
	states, err := sls.Compile(tree)
	if err != nil {
		fatal("%s: %v", path, err)
	}

	ctx := &modules.Ctx{Test: *test, Grains: g, BaseDir: filepath.Dir(path)}

	type done struct {
		Module, ID string
		Res        modules.Result
	}
	var executed []done
	lookup := func(r sls.Ref) (modules.Result, bool) {
		for _, d := range executed {
			if d.ID == r.ID && (r.Module == "" || r.Module == d.Module) {
				return d.Res, true
			}
		}
		return modules.Result{}, false
	}

	var jsonResults []map[string]any
	succeeded, failed, changed := 0, 0, 0
	for _, st := range states {
		// Skip if any requisite failed.
		blocked := ""
		for _, r := range append(append([]sls.Ref{}, st.Require...), st.Watch...) {
			if res, ok := lookup(r); ok && !res.Ok {
				blocked = fmt.Sprintf("%s:%s", r.Module, r.ID)
				break
			}
		}
		var res modules.Result
		if blocked != "" {
			res = modules.Result{Ok: false, Comment: "one or more requisite failed: " + blocked}
		} else {
			callArgs := make(map[string]any, len(st.Args)+1)
			for k, v := range st.Args {
				callArgs[k] = v
			}
			for _, r := range st.Watch {
				if res, ok := lookup(r); ok && res.Changed {
					callArgs["__watch_changed"] = "true"
					break
				}
			}
			if comment, gated := modules.CheckGates(callArgs); gated {
				res = modules.Result{Ok: true, Comment: comment}
			} else if fn, ok := modules.Registry[st.Name()]; !ok {
				res = modules.Result{Ok: false, Comment: fmt.Sprintf("state function %q not found", st.Name())}
			} else {
				res = fn(ctx, st.ID, callArgs)
			}
		}
		executed = append(executed, done{Module: st.Module, ID: st.ID, Res: res})
		if !*asJSON {
			printResult(st.ID, st.Name(), res)
		} else {
			jsonResults = append(jsonResults, map[string]any{
				"id": st.ID, "function": st.Name(), "result": res.Ok,
				"changed": res.Changed, "comment": res.Comment, "changes": res.Changes,
			})
		}
		if res.Ok {
			succeeded++
			if res.Changed {
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

func printResult(id, fn string, r modules.Result) {
	b := func(v bool) string {
		if v {
			return "True"
		}
		return "False"
	}
	fmt.Println("----------")
	fmt.Printf("      ID: %s\n", id)
	fmt.Printf("Function: %s\n", fn)
	fmt.Printf("  Result: %s\n", b(r.Ok))
	fmt.Printf(" Comment: %s\n", r.Comment)
	if len(r.Changes) > 0 {
		fmt.Println(" Changes:")
		keys := make([]string, 0, len(r.Changes))
		for k := range r.Changes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := r.Changes[k]
			if strings.Contains(v, "\n") {
				fmt.Printf("   %s: |\n", k)
				for _, l := range strings.Split(v, "\n") {
					fmt.Printf("     %s\n", l)
				}
				continue
			}
			fmt.Printf("   %s: %s\n", k, v)
		}
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}
