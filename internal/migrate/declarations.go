package migrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/signature"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// auditDeclarations checks every `module.function` a state file declares
// against the state modules this build ships, and every argument against
// that function's parameters.
//
// The audit exists to answer "what will break", and until this it did
// not look at the states at all: a tree with twenty-seven compilation
// errors was reported clean because none of them was a renderer or a
// YAML hazard. Every one of them was a state declaration.
//
// It reads the templating-stripped body, as the YAML audit does, so that
// a tree whose pillar is not available is still checked. That costs the
// declarations that only exist after rendering, which is the right trade:
// a missed finding is a surprise later, and a false one is a surprise now
// about something that is fine.
func auditDeclarations(rep *Report, opts Options, rel, body string) {
	if opts.StateRegistry == nil {
		return
	}
	yopts := yaml.DefaultOptions(rel)
	yopts.AllowDuplicateKeys = true
	parsed, _, err := yaml.Parse([]byte(stripTemplating(body)), yopts)
	if err != nil {
		// The YAML audit already reported this. A file that does not
		// parse has no declarations to check.
		return
	}
	top, ok := parsed.(*value.Map)
	if !ok {
		return
	}

	for _, decl := range top.Entries() {
		id := value.KeyString(decl.Key)
		if isStateKeyword(id) {
			continue
		}
		switch body := decl.Val.(type) {
		case string:
			// Salt's short declaration: the function with no arguments.
			checkStateFunction(rep, opts, rel, id, body, nil, decl.ValPos)
		case *value.Map:
			for _, fn := range body.Entries() {
				name := value.KeyString(fn.Key)
				if strings.HasPrefix(name, "__") {
					continue
				}
				checkStateFunction(rep, opts, rel, id, name, declaredArgs(fn.Val), fn.KeyPos)
			}
		}
	}
}

// isStateKeyword skips the top-level keys that are not state IDs.
func isStateKeyword(id string) bool {
	switch id {
	case "include", "exclude", "extend":
		return true
	}
	return false
}

// declaredArgs collects one declaration body's arguments, which is a
// list of single-entry mappings with bare strings among them for the
// function name and the flags.
func declaredArgs(v any) []value.Entry {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	var args []value.Entry
	for _, item := range items {
		m, ok := item.(*value.Map)
		if !ok {
			continue
		}
		args = append(args, m.Entries()...)
	}
	return args
}

func checkStateFunction(rep *Report, opts Options, rel, id, name string, args []value.Entry, pos value.Pos) {
	if !strings.Contains(name, ".") {
		return
	}
	rep.Modules[name]++

	sig, ok := opts.StateRegistry.Lookup(name)
	if !ok {
		module, _, _ := strings.Cut(name, ".")
		rep.Findings = append(rep.Findings, Finding{
			Category: CatState, Severity: Blocking, File: rel, Line: pos.Line, Col: pos.Col,
			Subject: name,
			Msg:     fmt.Sprintf("%s is not a state function this build ships%s", name, siblingsOf(opts, module)),
			Action:  "Check the tier table in SPEC section 15.5, or provide it as a bridged extension.",
		})
		return
	}

	known := map[string]signature.Param{}
	for _, p := range sig.Params {
		known[p.Name] = p
	}
	for _, arg := range args {
		argName := value.KeyString(arg.Key)
		param, ok := known[argName]
		if !ok {
			if isRequisiteOrOption(argName) {
				continue
			}
			rep.Findings = append(rep.Findings, Finding{
				Category: CatState, Severity: Blocking, File: rel, Line: pos.Line, Col: pos.Col,
				Subject: name + "." + argName,
				Msg:     fmt.Sprintf("%q is not an argument of %s", argName, name),
				Action:  "Remove it, or check the module reference for the name this build uses.",
			})
			continue
		}
		if param.Ineffective != "" {
			rep.Findings = append(rep.Findings, Finding{
				Category: CatState, Severity: Note, File: rel, Line: arg.KeyPos.Line, Col: arg.KeyPos.Col,
				Subject: name + "." + argName,
				Msg:     fmt.Sprintf("%q is accepted and has no effect: %s", argName, param.Ineffective),
				Action:  "Nothing breaks. Remove it when convenient.",
			})
			continue
		}

		// A mode that arrived as an integer is the one type error worth
		// reporting from a stripped body: an integer is an integer
		// whatever the templating did, and it is the difference between
		// the mode in the tree and the mode on disk. `mode: 0644` is
		// caught by the YAML audit as an implicit octal; `mode: 640` has
		// no leading zero, so nothing caught it until here.
		if n, isInt := arg.Val.(int64); isInt && param.Type == signature.Mode {
			// `mode: 0644` is already reported by the YAML audit as an
			// implicit octal, at review, because an implicit octal is
			// only a warning in general. On a mode it stops compilation,
			// so that finding is raised rather than a second one added
			// saying the same thing at the same line.
			if raised := raiseFindingAt(rep, rel, arg.ValPos.Line); raised {
				continue
			}
			rep.Findings = append(rep.Findings, Finding{
				Category: CatState, Severity: Blocking, File: rel, Line: arg.KeyPos.Line, Col: arg.KeyPos.Col,
				Subject: name + "." + argName,
				Msg:     fmt.Sprintf("%s is the integer %d; a mode must be a quoted string", argName, n),
				Action:  "Quote it. SPEC section 10.1.3.",
			})
		}
	}
}

// siblingsOf names the functions the module does provide, which is the
// difference between "that is wrong" and "did you mean this".
func siblingsOf(opts Options, module string) string {
	var have []string
	for _, name := range opts.StateRegistry.Names() {
		if m, fn, _ := strings.Cut(name, "."); m == module {
			have = append(have, fn)
		}
	}
	if len(have) == 0 {
		return ""
	}
	sort.Strings(have)
	return "; " + module + " provides " + strings.Join(have, ", ")
}

// isRequisiteOrOption reports whether a name is one of the requisites or
// per-state options every state accepts, rather than a module argument.
func isRequisiteOrOption(name string) bool {
	switch strings.TrimSuffix(strings.TrimSuffix(name, "_in"), "_any") {
	case "require", "watch", "onchanges", "onfail", "prereq", "use", "listen":
		return true
	}
	switch name {
	case "order", "failhard", "onlyif", "unless", "creates", "check_cmd",
		"retry", "timeout", "runas", "umask", "names", "parallel",
		"fire_event", "reload_modules", "aggregate", "saltenv", "env":
		return true
	}
	return false
}

// raiseFindingAt promotes an existing YAML finding on one line to
// blocking, and reports whether it found one.
func raiseFindingAt(rep *Report, file string, line int) bool {
	for i := range rep.Findings {
		f := &rep.Findings[i]
		if f.Category == CatYAML && f.File == file && f.Line == line {
			f.Severity = Blocking
			f.Action = "Quote it. On a file mode this stops compilation. SPEC section 10.1.3."
			return true
		}
	}
	return false
}
