package migrate

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// Skeleton is a generated Go bridge for one Salt custom module.
type Skeleton struct {
	// Module is what it was generated from.
	Module PyModule
	// Kind is the extension kind — `module` for `_modules/`,
	// `returner` for `_returners/`, and so on.
	Kind string
	// Path is where to write it, relative to wherever the operator
	// asked for the skeletons.
	Path string
	// Source is the Go file.
	Source string
}

// kindForDir maps a Salt extension directory to an extension kind of
// SPEC 24.2.
//
// A directory with no corresponding kind gets none, and the report says
// so rather than generating a bridge for something that cannot be one:
// `_utils` is a Python import target, not an extension point, and a
// skeleton for it would be a file nobody could use.
var kindForDir = map[string]string{
	"_modules":    "module",
	"_states":     "state",
	"_grains":     "grain",
	"_beacons":    "beacon",
	"_returners":  "returner",
	"_pillar":     "pillar",
	"_runners":    "runner",
	"_renderers":  "renderer",
	"_roster":     "roster",
	"_auth":       "auth",
	"_fileserver": "fileserver",
}

// KindForDir answers with the extension kind a Salt directory maps to.
func KindForDir(dir string) (string, bool) {
	kind, ok := kindForDir[strings.TrimPrefix(dir, "_")]
	if ok {
		return kind, true
	}
	kind, ok = kindForDir[dir]
	return kind, ok
}

// GenerateSkeleton writes a Go bridge for one Python module.
//
// SPEC 24.6: this turns an unbounded porting problem into a bounded
// one. It is a starting point rather than a translation — every
// function body is a `TODO` that returns an error, so a skeleton that
// was compiled and shipped without being finished fails loudly instead
// of returning nothing and looking like it worked.
func GenerateSkeleton(module PyModule, kind string) Skeleton {
	var b strings.Builder

	fmt.Fprintf(&b, `// Command %s is a bridge extension generated from %s.
//
// It is a skeleton. Every function returns an error until it is
// written, so that a bridge which was generated and forgotten fails
// loudly rather than answering nothing and looking as though it worked.
//
// What the original did is in the Python; what it must do here is
// declared below. The signatures were read from the source and are
// starting points: a Salt module takes whatever Python allows, and this
// declares types so the caller is validated.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/edlitmus/halite/internal/bridge"
)

func main() {
	// Applies the resource limits the host asked for.
	bridge.Confine()

	ext := &bridge.Extension{
		Name:    %q,
		Version: "0.1.0",
		Kind:    %q,
		// Nothing is granted that is not declared here, and a
		// declaration this build does not understand is refused. The
		// choices are "root" and "network".
		Declares: nil,
		Functions: []json.RawMessage{
`, bridgeCommandName(module.Name), module.File, module.Name, kind)

	for _, fn := range module.Functions {
		// A quoted literal rather than a raw one: a docstring may hold
		// a backtick, and a raw string would end there — producing Go
		// that does not compile, from a formula that is perfectly
		// ordinary.
		fmt.Fprintf(&b, "\t\t\tjson.RawMessage(%s),\n",
			strconv.Quote(signatureJSON(module.Name, fn)))
	}

	fmt.Fprintf(&b, `		},
		Handler: handle,
	}
	if err := ext.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, %q, err)
		os.Exit(1)
	}
}

func handle(call bridge.Call) (any, error) {
	var kwargs map[string]any
	if len(call.Kwargs) > 0 {
		if err := json.Unmarshal(call.Kwargs, &kwargs); err != nil {
			return nil, err
		}
	}

	switch call.Function {
`, module.Name+":")

	for _, fn := range module.Functions {
		fmt.Fprintf(&b, "\tcase %q:\n", fn.Name)
		if fn.Doc != "" {
			fmt.Fprintf(&b, "\t\t// %s\n", oneLine(fn.Doc))
		}
		fmt.Fprintf(&b, "\t\t// From %s:%d\n", module.File, fn.Line)
		for _, param := range fn.Params {
			fmt.Fprintf(&b, "\t\t//   %s\n", describeParam(param))
		}
		fmt.Fprintf(&b, "\t\treturn nil, fmt.Errorf(\"%s.%s is not written yet\")\n",
			module.Name, fn.Name)
		if fn.Name != module.Functions[len(module.Functions)-1].Name {
			b.WriteString("\n")
		}
	}

	fmt.Fprintf(&b, `	}
	return nil, fmt.Errorf("%%s has no function %%q", %q, call.Function)
}
`, module.Name)

	if module.HasVirtual {
		fmt.Fprintf(&b, `
// The original declared __virtual__, which decides at runtime whether
// the module loads at all — usually by checking the platform or whether
// a binary is present. A bridge cannot infer what it checked.
//
// Two ways to carry it over: refuse the calls that do not apply, with a
// message saying why, or declare the platforms in each function's
// signature so the host will not route to it. The second is better when
// the condition is the platform, because the caller is told before
// anything runs.
`)
	}
	return Skeleton{
		Module: module, Kind: kind,
		Path:   bridgeCommandName(module.Name) + "/main.go",
		Source: b.String(),
	}
}

// signatureJSON renders one function's signature in the shape of
// section 15.6, which is what the extension sends at handshake.
func signatureJSON(module string, fn PyFunction) string {
	var params []string
	for _, p := range fn.Params {
		if p.Variadic || p.Keywords {
			// `*args` and `**kwargs` are not parameters the host can
			// validate. A function that needs them declares its real
			// arguments; one that genuinely takes anything is a
			// judgement the porter has to make.
			continue
		}
		params = append(params, fmt.Sprintf(
			`{"name":%q,"type":"any","required":%t,"doc":%q}`,
			p.Name, !p.HasDefault, describeDefault(p)))
	}
	doc := fn.Doc
	if doc == "" {
		doc = "TODO: describe " + fn.Name + "."
	}
	return fmt.Sprintf(`{"module":%q,"function":%q,"doc":%q,"mutates":true,"params":[%s]}`,
		module, fn.Name, doc, strings.Join(params, ","))
}

func describeDefault(p PyParam) string {
	if !p.HasDefault {
		return ""
	}
	return "Python default: " + p.Default
}

// describeParam is the comment beside a stub, so whoever writes the
// body can see what the original took without opening the Python.
func describeParam(p PyParam) string {
	switch {
	case p.Keywords:
		return "**" + p.Name + " — arbitrary keyword arguments; declare the ones that are used"
	case p.Variadic:
		return "*" + p.Name + " — arbitrary positional arguments"
	case p.HasDefault:
		return p.Name + " = " + p.Default
	}
	return p.Name + " (required)"
}

// bridgeCommandName is a directory name for the generated command.
func bridgeCommandName(module string) string {
	var b strings.Builder
	for _, r := range module {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "extension"
	}
	return "halite-ext-" + name
}

// Skeletons generates a bridge for every custom module in a report,
// in path order so two runs produce the same set.
func Skeletons(modules []PyModule, kinds map[string]string) []Skeleton {
	out := make([]Skeleton, 0, len(modules))
	for _, module := range modules {
		kind := kinds[module.File]
		if kind == "" {
			continue
		}
		out = append(out, GenerateSkeleton(module, kind))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// oneLine flattens text that is about to become a line comment. The
// docstring reader already takes a single line; this is the belt to its
// braces, because a newline here would comment out the code below it.
func oneLine(v string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(v))
}
