package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/runner"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/value"
)

// runCall is `halite-node call <module.function> [args...]`, the old
// salt-call.
//
// `state.apply` and its neighbours are recognised here as well as under
// the `state` subcommand, because that is the spelling operator muscle
// memory has.
func runCall(args *cli.Args) int {
	if len(args.Positional) == 0 {
		cli.Fatalf("call needs a module.function")
	}
	fn := args.Positional[0]
	rest := args.Positional[1:]

	if module, _, ok := strings.Cut(fn, "."); ok && module == "state" {
		return runStateFunction(args, strings.TrimPrefix(fn, "state."), rest)
	}

	n := setup(args)
	p := n.compilePillar()
	ctx := n.context(p)

	positional := make([]any, len(rest))
	for i, a := range rest {
		positional[i] = a
	}
	out, err := n.registry.Exec.CallPositional(ctx, fn, positional, args.Kwargs)
	if err != nil {
		cli.Fatalf("%v", err)
	}
	n.out(value.MapOf(n.nodeID, out))
	return 0
}

// runState is `halite-node state <subcommand>`.
func runState(args *cli.Args) int {
	if len(args.Positional) == 0 {
		cli.Fatalf("state needs a subcommand; try `state apply`")
	}
	return runStateFunction(args, args.Positional[0], args.Positional[1:])
}

func runStateFunction(args *cli.Args, fn string, rest []string) int {
	n := setup(args)
	p := n.compilePillar()

	compiler := &state.Compiler{
		Loader:   n.files,
		Registry: n.registry.States.Signatures(),
		Config: state.Config{
			// `salt['pillar.get']` and its neighbours are ordinary in an
			// SLS file. The compiler has always passed this through to
			// the renderer and nothing ever set it, so every one of them
			// was undefined.
			Salt:             exec.TemplateDispatcher{Registry: n.registry.Exec, Context: n.context(p)},
			Env:              n.env,
			PillarEnv:        n.env,
			NodeID:           n.nodeID,
			JobID:            newJobID(),
			Grains:           n.grains,
			Pillar:           p,
			ConfigValues:     n.cfg.Redacted(),
			Undefined:        n.undef,
			TopMergeStrategy: n.cfg.String("top_file_merging_strategy", "merge"),
			StateAllowlist:   n.cfg.StringSlice("state_allowlist"),
			StateDenylist:    n.cfg.StringSlice("state_denylist"),
			Test:             n.test,
			GPG:              n.gpgOptions(),
		},
	}

	compile := func(names []string) *state.Compiled {
		if len(names) == 0 {
			return compiler.CompileHighstate()
		}
		return compiler.CompileSLS(names)
	}

	switch fn {
	case "apply", "highstate", "sls":
		out := compile(rest)
		reportCompilation(out)
		if err := out.Err(); err != nil {
			cli.Fatalf("%v", err)
		}
		return applyStates(n, p, out)

	case "show_top":
		out := compile(nil)
		reportCompilation(out)
		if err := out.Err(); err != nil {
			cli.Fatalf("%v", err)
		}
		names := make([]any, len(out.SLS))
		for i, s := range out.SLS {
			names[i] = s
		}
		n.out(value.MapOf(n.env, names))
		return 0

	case "show_highstate", "show_sls":
		out := compile(rest)
		reportCompilation(out)
		if err := out.Err(); err != nil {
			cli.Fatalf("%v", err)
		}
		n.out(renderHigh(out))
		return 0

	case "show_lowstate":
		out := compile(rest)
		reportCompilation(out)
		if err := out.Err(); err != nil {
			cli.Fatalf("%v", err)
		}
		n.out(renderLow(out))
		return 0

	case "show_states":
		out := compile(rest)
		reportCompilation(out)
		if err := out.Err(); err != nil {
			cli.Fatalf("%v", err)
		}
		ids := make([]any, 0, len(out.Low))
		seen := map[string]bool{}
		for _, ch := range out.Low {
			if seen[ch.ID] {
				continue
			}
			seen[ch.ID] = true
			ids = append(ids, ch.SLS+"."+ch.ID)
		}
		n.out(ids)
		return 0
	}
	cli.Fatalf("state has no subcommand %q; try apply, sls, show_top, show_highstate, show_lowstate, or show_states", fn)
	return 2
}

// applyStates runs the compiled low state and prints the result.
func applyStates(n *node, p *value.Map, compiled *state.Compiled) int {
	ctx := n.context(p)
	r := &runner.Runner{
		States:   n.registry.States,
		Exec:     n.registry.Exec,
		Ctx:      ctx,
		FailHard: n.cfg.Bool("failhard", false),
	}
	result := r.Run(compiled.Low)

	switch n.format {
	case cli.Nested:
		fmt.Print(result.Nested(false))
	case cli.Quiet:
	default:
		n.out(result.Returns())
	}
	return result.RetCode()
}

// reportCompilation prints the warnings a compilation produced. They go to
// stderr so that piping the output gives only the data.
func reportCompilation(out *state.Compiled) {
	for _, w := range out.RenderWarnings {
		fmt.Fprintln(os.Stderr, "halite-node: "+w.String())
	}
	for _, d := range out.Diags.Warnings().Sorted() {
		fmt.Fprintln(os.Stderr, "halite-node: "+d.String())
	}
}

// renderHigh prints the assembled high state.
func renderHigh(out *state.Compiled) *value.Map {
	high := value.NewMap(out.High.Len())
	for _, d := range out.High.Decls() {
		body := value.NewMap(len(d.Funcs) + 2)
		for _, f := range d.Funcs {
			args := make([]any, 0, f.Args.Len())
			for _, e := range f.Args.Entries() {
				args = append(args, value.MapOf(value.KeyString(e.Key), e.Val))
			}
			body.Set(f.Name(), args)
		}
		body.Set("__sls__", d.SLS)
		body.Set("__env__", d.Env)
		high.Set(d.ID, body)
	}
	return high
}

// renderLow prints the ordered low state, which is the fastest way to see
// what a tree will actually do and in what order.
func renderLow(out *state.Compiled) []any {
	low := make([]any, 0, len(out.Low))
	for _, ch := range out.Low {
		m := value.MapOf(
			"__id__", ch.ID,
			"__sls__", ch.SLS,
			"__env__", ch.Env,
			"__run_num__", int64(ch.RunNum),
			"state", ch.State,
			"fun", ch.Fun,
			"name", ch.Name,
		)
		for _, e := range ch.Args.Entries() {
			if value.KeyString(e.Key) == "name" {
				continue
			}
			m.Set(e.Key, e.Val)
		}
		if len(ch.Reqs) > 0 {
			reqs := make([]any, 0, len(ch.Reqs))
			for _, req := range ch.Reqs {
				reqs = append(reqs, value.MapOf(req.Kind.String(), req.Describe()))
			}
			m.Set("__requisites__", reqs)
		}
		low = append(low, m)
	}
	return low
}

// runGrains is `halite-node grains items|item|get <key>`.
func runGrains(args *cli.Args) int {
	n := setup(args)
	sub := "items"
	if len(args.Positional) > 0 {
		sub = args.Positional[0]
	}
	switch sub {
	case "items":
		n.out(value.MapOf(n.nodeID, n.grains))
	case "get":
		// `get` is one key and the bare value; `item` is any number of
		// them and a mapping. Taking a second key here and answering
		// about only the first is how a caller reads the wrong grain.
		if len(args.Positional) != 2 {
			cli.Fatalf("grains get takes exactly one key; use `grains item` for several")
		}
		v, ok := value.Traverse(n.grains, args.Positional[1], ":")
		if !ok {
			v = ""
		}
		n.out(value.MapOf(n.nodeID, v))
	case "item":
		if len(args.Positional) < 2 {
			cli.Fatalf("grains item needs a key")
		}
		n.out(value.MapOf(n.nodeID, traverseAll(n.grains, args.Positional[1:])))
	case "ls", "keys":
		names := make([]any, 0, n.grains.Len())
		for _, k := range n.grains.SortedKeys() {
			names = append(names, k)
		}
		n.out(value.MapOf(n.nodeID, names))
	default:
		cli.Fatalf("grains has no subcommand %q; try items, item, get, or ls", sub)
	}
	return 0
}

// traverseAll resolves several colon-separated keys against one mapping,
// answering with the empty string for a key that is not there, as Salt's
// grains.item and pillar.item do.
func traverseAll(m *value.Map, keys []string) *value.Map {
	out := value.NewMap(len(keys))
	for _, key := range keys {
		v, ok := value.Traverse(m, key, ":")
		if !ok {
			v = ""
		}
		out.Set(key, v)
	}
	return out
}

// runPillar is `halite-node pillar items|item|get <key>`.
func runPillar(args *cli.Args) int {
	n := setup(args)
	p := n.compilePillar()
	sub := "items"
	if len(args.Positional) > 0 {
		sub = args.Positional[0]
	}
	switch sub {
	case "items":
		n.out(value.MapOf(n.nodeID, p))
	case "get":
		if len(args.Positional) != 2 {
			cli.Fatalf("pillar get takes exactly one key; use `pillar item` for several")
		}
		v, ok := value.Traverse(p, args.Positional[1], ":")
		if !ok {
			v = ""
		}
		n.out(value.MapOf(n.nodeID, v))
	case "item":
		if len(args.Positional) < 2 {
			cli.Fatalf("pillar item needs a key")
		}
		n.out(value.MapOf(n.nodeID, traverseAll(p, args.Positional[1:])))
	default:
		cli.Fatalf("pillar has no subcommand %q; try items, item, or get", sub)
	}
	return 0
}

// runLint renders and parses without executing, and reports unsupported
// constructs, YAML 1.1 coercions, and duplicate keys. SPEC section 10.1.4.
func runLint(args *cli.Args) int {
	if len(args.Positional) == 0 {
		cli.Fatalf("lint needs a path")
	}
	n := setup(args)
	p := n.compilePillar()

	problems := 0
	for _, path := range args.Positional {
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			problems++
			continue
		}
		res, err := render.Render(src, render.Options{
			File:      path,
			SLS:       strings.TrimSuffix(path, ".sls"),
			Env:       n.env,
			NodeID:    n.nodeID,
			Grains:    n.grains,
			Pillar:    p,
			Config:    n.cfg.Redacted(),
			Loader:    n.files.Templates(n.env),
			Undefined: n.undef,
		})
		for _, w := range res.Warnings {
			fmt.Println(w.String())
			problems++
		}
		if err != nil {
			fmt.Println(err.Error())
			problems++
			continue
		}
		fmt.Printf("%s: renders and parses; pipeline %s\n", path, strings.Join(res.Pipeline, "|"))
	}
	if problems > 0 {
		return 1
	}
	return 0
}
