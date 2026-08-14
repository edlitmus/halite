package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/sls"
)

// cmdShow prints the compiled plan for a target without running any of it:
// halite's answer to state.show_sls and state.show_highstate.
//
// It is not `apply -test`. A dry run calls every module to ask what it
// would change, which reads the host and takes as long as the run does;
// `show` stops after the compile, so it answers "what did my templates,
// includes, and requisites actually produce, and in what order" — the
// question you have when a highstate does something surprising.
func cmdShow(args []string) {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "output the plan as JSON")
	rootFlag := fs.String("root", "", "state tree root (default: $HALITE_ROOT or the platform default)")
	pillarRootFlag := fs.String("pillar-root", "", "pillar tree root (default: $HALITE_PILLAR_ROOT or <root>/../pillar)")
	targets := parseFlags(fs, args)

	root := resolveRoot(*rootFlag)
	pillarRoot := resolvePillarRoot(*pillarRootFlag, root)
	warnPillarPermissions(pillarRoot)

	g := grains.Collect()
	p, err := loadPillar("", pillarRoot, g)
	if err != nil {
		fatal("%v", err)
	}
	states, err := loadPlan(targets, root, rootIsExplicit(*rootFlag), g, p)
	if err != nil {
		fatal("%v", err)
	}

	if *asJSON {
		b, _ := json.MarshalIndent(planJSON(states, root), "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Print(formatPlan(states, root))
}

// requisiteKinds are the requisite lists a compiled state carries, in the
// order they are worth reading.
var requisiteKinds = []struct {
	name string
	refs func(sls.State) []sls.Ref
}{
	{"require", func(s sls.State) []sls.Ref { return s.Require }},
	{"watch", func(s sls.State) []sls.Ref { return s.Watch }},
	{"onchanges", func(s sls.State) []sls.Ref { return s.OnChanges }},
	{"prereq", func(s sls.State) []sls.Ref { return s.Prereq }},
}

// formatPlan renders the compiled states in the order they would run.
func formatPlan(states []sls.State, root string) string {
	if len(states) == 0 {
		return "no states\n"
	}
	var b strings.Builder
	sources := map[string]bool{}
	width := len(fmt.Sprint(len(states)))

	for i, st := range states {
		source := relativeTo(root, st.Src)
		sources[source] = true
		fmt.Fprintf(&b, "%*d. %s\n", width, i+1, stripControl(st.ID))
		fmt.Fprintf(&b, "%s   %s", strings.Repeat(" ", width), stripControl(st.Name()))
		if source != "" {
			fmt.Fprintf(&b, "  (%s)", stripControl(source))
		}
		b.WriteString("\n")
		writePlanArgs(&b, st, strings.Repeat(" ", width)+"     ")
	}

	fmt.Fprintf(&b, "\n%s from %s, in the order they would run\n",
		plural(len(states), "state"), plural(len(sources), "sls file"))
	return b.String()
}

// writePlanArgs prints a state's arguments and requisites, sorted so that
// two runs of the same tree read the same.
func writePlanArgs(b *strings.Builder, st sls.State, indent string) {
	keys := make([]string, 0, len(st.Args))
	for k := range st.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "%s%s: %s\n", indent, stripControl(k), stripControl(flatValue(st.Args[k])))
	}
	for _, kind := range requisiteKinds {
		refs := kind.refs(st)
		if len(refs) == 0 {
			continue
		}
		names := make([]string, 0, len(refs))
		for _, r := range refs {
			names = append(names, stripControl(refName(r)))
		}
		fmt.Fprintf(b, "%s%s: %s\n", indent, kind.name, strings.Join(names, ", "))
	}
}

// planJSON is the same plan for a script to read.
func planJSON(states []sls.State, root string) []map[string]any {
	out := make([]map[string]any, 0, len(states))
	for i, st := range states {
		entry := map[string]any{
			"order":    i + 1,
			"id":       st.ID,
			"module":   st.Module,
			"function": st.Fn,
			"args":     st.Args,
			"source":   relativeTo(root, st.Src),
		}
		if st.BaseID != "" {
			entry["declared_id"] = st.BaseID
		}
		for _, kind := range requisiteKinds {
			refs := kind.refs(st)
			if len(refs) == 0 {
				continue
			}
			names := make([]string, 0, len(refs))
			for _, r := range refs {
				names = append(names, refName(r))
			}
			entry[kind.name] = names
		}
		out = append(out, entry)
	}
	return out
}

// refName renders a requisite reference the way an SLS file writes it.
func refName(r sls.Ref) string {
	if r.Module == "" {
		return r.ID
	}
	return r.Module + ":" + r.ID
}

// flatValue renders an argument value on one line. The plan is for reading,
// so a long list is summarised rather than wrapped.
func flatValue(v any) string {
	switch t := v.(type) {
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s: %v", k, t[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprintf("%v", v)
}

// relativeTo shortens a source path against the tree root, so a plan reads
// as sls files rather than as absolute paths.
func relativeTo(root, path string) string {
	if path == "" || root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
