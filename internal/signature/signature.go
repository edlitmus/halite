// Package signature is the machine-readable description of every module
// function: its parameters, their types, whether it changes the system,
// whether it honours test mode, which platforms it applies to, and what
// privilege it needs.
//
// Salt derives this by Python introspection at runtime. Here it is
// declared at build time, which is what lets the state compiler validate a
// whole tree without executing anything, and lets `halite-hub state.compile`
// gate a tree in CI without touching a node. SPEC section 15.6.
package signature

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// Type is a parameter's declared type. The state compiler uses it to
// reject a bad argument at compile time rather than at run time, and the
// CLI uses it instead of Salt's habit of YAML-parsing every argument.
type Type int

const (
	// Any accepts whatever the caller passes.
	Any Type = iota
	String
	Int
	Float
	Bool
	List
	Map
	// Path is a string that names a filesystem path.
	Path
	// Mode is a file mode, which must be written as a string so that 0644
	// does not become the decimal 420.
	Mode
	// Duration is a Go duration string or a bare number of seconds.
	Duration
)

var typeNames = map[Type]string{
	Any: "any", String: "string", Int: "int", Float: "float", Bool: "bool",
	List: "list", Map: "map", Path: "path", Mode: "mode", Duration: "duration",
}

func (t Type) String() string {
	if n, ok := typeNames[t]; ok {
		return n
	}
	return "unknown"
}

// TestMode states how honestly a function behaves under test=True.
type TestMode int

const (
	// TestReliable means the function makes no change, returns a nil
	// result when it would change something, and predicts the change
	// accurately. Every state module must reach this; SPEC section 11.6.
	TestReliable TestMode = iota
	// TestUnreliable means the function cannot honestly predict its
	// changes. Using it as a prereq target is a compilation warning.
	TestUnreliable
	// TestNotApplicable is for functions that change nothing anyway.
	TestNotApplicable
)

func (t TestMode) String() string {
	switch t {
	case TestUnreliable:
		return "unreliable"
	case TestNotApplicable:
		return "not_applicable"
	default:
		return "reliable"
	}
}

// Param is one parameter of a function.
type Param struct {
	Name     string
	Type     Type
	Required bool
	Default  any
	Doc      string
	// Variadic marks the parameter that soaks up remaining positional
	// arguments, as `cmd.run`'s argument vector does.
	Variadic bool
	// KeywordOnly means the parameter cannot be passed positionally.
	KeywordOnly bool
	// Choices restricts a string parameter to a fixed set.
	Choices []string
}

// Signature describes one `module.function`.
type Signature struct {
	Module   string
	Function string
	Doc      string
	Params   []Param

	// Mutates records that the function changes the system, which the
	// state compiler needs in order to know that a bare execution module
	// call is not read-only.
	Mutates bool
	// TestMode is the honesty of this function under test=True.
	TestMode TestMode
	// Platforms restricts the function; empty means every platform.
	Platforms []string
	// Privileges names what the function needs, such as "root".
	Privileges []string

	// ArbitraryCode marks a function that can run whatever the caller
	// asks. These are never granted by a wildcard in the RBAC policy;
	// granting them requires naming them. SPEC section 23.5.
	ArbitraryCode bool

	// Returns describes the shape of the return, for `sys.doc`.
	Returns string
	// Section names the part of SPEC that defines the function.
	Section string
}

// Name is the dotted `module.function` form.
func (s Signature) Name() string { return s.Module + "." + s.Function }

// Param finds a parameter by name.
func (s Signature) Param(name string) (Param, bool) {
	for _, p := range s.Params {
		if p.Name == name {
			return p, true
		}
	}
	return Param{}, false
}

// Registry holds the signatures a build ships.
type Registry struct {
	byName map[string]Signature
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{byName: map[string]Signature{}} }

// Add registers a signature, replacing any earlier one with the same name.
func (r *Registry) Add(sigs ...Signature) {
	for _, s := range sigs {
		r.byName[s.Name()] = s
	}
}

// Lookup finds a signature by `module.function`.
func (r *Registry) Lookup(name string) (Signature, bool) {
	s, ok := r.byName[name]
	return s, ok
}

// Has reports whether a name is registered.
func (r *Registry) Has(name string) bool { _, ok := r.byName[name]; return ok }

// Names lists every registered function, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Modules lists the distinct module names, sorted.
func (r *Registry) Modules() []string {
	seen := map[string]bool{}
	for _, s := range r.byName {
		seen[s.Module] = true
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Functions lists the functions of one module, sorted.
func (r *Registry) Functions(module string) []Signature {
	var out []Signature
	for _, s := range r.byName {
		if s.Module == module {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Function < out[j].Function })
	return out
}

// ArgError is a rejected argument, naming the parameter and why.
type ArgError struct {
	Function string
	Param    string
	Msg      string
}

func (e *ArgError) Error() string {
	if e.Param == "" {
		return fmt.Sprintf("%s: %s", e.Function, e.Msg)
	}
	return fmt.Sprintf("%s: argument %q: %s", e.Function, e.Param, e.Msg)
}

// Bind matches positional and keyword arguments against a signature and
// returns the resolved keyword mapping, with defaults filled in.
//
// Every error is collected rather than the first being returned, because
// fixing a large tree one error per run is the grind SPEC section 11.2
// step 10 sets out to remove.
func (s Signature) Bind(args []any, kwargs *value.Map) (*value.Map, []error) {
	out := value.NewMap(len(s.Params))
	var errs []error

	positional := make([]Param, 0, len(s.Params))
	for _, p := range s.Params {
		if !p.KeywordOnly {
			positional = append(positional, p)
		}
	}

	used := map[string]bool{}
	for i, a := range args {
		if i >= len(positional) {
			last := len(positional) - 1
			if last >= 0 && positional[last].Variadic {
				appendVariadic(out, positional[last].Name, a)
				continue
			}
			errs = append(errs, &ArgError{
				Function: s.Name(),
				Msg:      fmt.Sprintf("takes at most %d positional arguments, got %d", len(positional), len(args)),
			})
			break
		}
		p := positional[i]
		if p.Variadic {
			appendVariadic(out, p.Name, a)
			continue
		}
		v, err := coerce(s, p, a)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out.Set(p.Name, v)
		used[p.Name] = true
	}

	if kwargs != nil {
		for _, e := range kwargs.Entries() {
			name := value.KeyString(e.Key)
			p, ok := s.Param(name)
			if !ok {
				errs = append(errs, &ArgError{
					Function: s.Name(), Param: name,
					Msg: "is not a parameter of this function",
				})
				continue
			}
			if used[name] {
				errs = append(errs, &ArgError{
					Function: s.Name(), Param: name,
					Msg: "was given both positionally and by keyword",
				})
				continue
			}
			v, err := coerce(s, p, e.Val)
			if err != nil {
				errs = append(errs, err)
				continue
			}
			out.Set(name, v)
			used[name] = true
		}
	}

	for _, p := range s.Params {
		if used[p.Name] || out.Has(p.Name) {
			continue
		}
		if p.Required {
			errs = append(errs, &ArgError{
				Function: s.Name(), Param: p.Name, Msg: "is required",
			})
			continue
		}
		if p.Default != nil {
			out.Set(p.Name, p.Default)
		}
	}

	return out, errs
}

func appendVariadic(out *value.Map, name string, v any) {
	cur, _ := out.Get(name)
	list, _ := cur.([]any)
	out.Set(name, append(list, v))
}

// coerce checks a value against a declared type.
//
// The coercion is narrow on purpose. Salt YAML-parses every command line
// argument, which is why a package version 1.0 becomes a float and NO
// becomes a boolean; here an argument is what its type says it is, and a
// mismatch is an error rather than a surprise. SPEC section 9.2.
func coerce(s Signature, p Param, v any) (any, error) {
	fail := func(want string) error {
		return &ArgError{
			Function: s.Name(), Param: p.Name,
			Msg: fmt.Sprintf("must be %s, found %s", want, value.TypeName(v)),
		}
	}
	if v == nil {
		return nil, nil
	}

	switch p.Type {
	case Any:
		return v, nil

	case String, Path:
		if str, ok := v.(string); ok {
			if len(p.Choices) > 0 && !contains(p.Choices, str) {
				return nil, &ArgError{
					Function: s.Name(), Param: p.Name,
					Msg: fmt.Sprintf("must be one of %s, found %q", strings.Join(p.Choices, ", "), str),
				}
			}
			return str, nil
		}
		// Salt YAML-parses every argument, so a tree written against it
		// says `user: 0` and means the string. Refusing the integer here
		// stops a tree that Salt compiles, and the only thing an integer
		// could mean in a string parameter is its decimal spelling.
		// `mode` is deliberately not part of this: see the case below.
		if n, ok := v.(int64); ok {
			return strconv.FormatInt(n, 10), nil
		}
		return nil, fail("a string")

	case Mode:
		// A mode must be a string. An integer here means the YAML parser
		// read an unquoted 0644 as octal, and silently accepting it is how
		// a file ends up with mode 0644 in the tree and 420 on disk.
		if str, ok := v.(string); ok {
			return str, nil
		}
		if n, ok := v.(int64); ok {
			return nil, &ArgError{
				Function: s.Name(), Param: p.Name,
				Msg: fmt.Sprintf("must be a quoted string, found the integer %d; write it as '%04o'", n, n),
			}
		}
		return nil, fail("a quoted file mode such as '0644'")

	case Int:
		switch t := v.(type) {
		case int64:
			return t, nil
		case float64:
			if t == float64(int64(t)) {
				return int64(t), nil
			}
		case string:
			// A command line argument arrives as a string, because SPEC
			// section 9.2 refuses to guess at its type. The declared type
			// is what converts it, and this is where that happens: a
			// parameter declared Int takes `days=30` from the command
			// line, while one declared String keeps `version=1.0` a
			// string. Without this every numeric parameter was unusable
			// from `halite-node call`.
			if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
				return n, nil
			}
		}
		return nil, fail("an integer")

	case Float:
		switch t := v.(type) {
		case float64:
			return t, nil
		case int64:
			return float64(t), nil
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
				return f, nil
			}
		}
		return nil, fail("a number")

	case Bool:
		switch t := v.(type) {
		case bool:
			return t, nil
		case string:
			// The spellings a command line and a YAML 1.1 tree both use.
			switch strings.ToLower(strings.TrimSpace(t)) {
			case "true", "yes", "on", "1":
				return true, nil
			case "false", "no", "off", "0":
				return false, nil
			}
		}
		return nil, fail("a boolean")

	case List:
		if l, ok := v.([]any); ok {
			return l, nil
		}
		// A bare scalar where a list is expected is accepted as a
		// one-item list, which is how Salt trees are written.
		return []any{v}, nil

	case Map:
		if m, ok := v.(*value.Map); ok {
			return m, nil
		}
		return nil, fail("a mapping")

	case Duration:
		switch v.(type) {
		case string, int64, float64:
			return v, nil
		}
		return nil, fail("a duration")
	}
	return v, nil
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// Describe renders a signature the way `sys.doc` prints it.
func (s Signature) Describe() string {
	var b strings.Builder
	b.WriteString(s.Name())
	b.WriteByte('(')
	for i, p := range s.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		if p.Variadic {
			b.WriteByte('*')
		}
		b.WriteString(p.Name)
		b.WriteString(": ")
		b.WriteString(p.Type.String())
		if !p.Required && p.Default != nil {
			fmt.Fprintf(&b, " = %v", p.Default)
		}
	}
	b.WriteByte(')')
	if s.Doc != "" {
		b.WriteString("\n  ")
		b.WriteString(s.Doc)
	}
	var flags []string
	if s.Mutates {
		flags = append(flags, "changes the system")
	}
	if s.ArbitraryCode {
		flags = append(flags, "runs arbitrary code; must be granted by name")
	}
	if s.TestMode == TestUnreliable {
		flags = append(flags, "test mode is unreliable")
	}
	if len(s.Platforms) > 0 {
		flags = append(flags, "platforms: "+strings.Join(s.Platforms, ", "))
	}
	if len(s.Privileges) > 0 {
		flags = append(flags, "needs: "+strings.Join(s.Privileges, ", "))
	}
	for _, f := range flags {
		b.WriteString("\n  - ")
		b.WriteString(f)
	}
	return b.String()
}

// JSON renders the registry for the API's schema endpoint. The shape is
// stable, because downstream tooling depends on it.
func (r *Registry) JSON() *value.Map {
	out := value.NewMap(len(r.byName))
	for _, name := range r.Names() {
		out.Set(name, r.byName[name].JSON())
	}
	return out
}

// JSON renders one signature.
func (s Signature) JSON() *value.Map {
	params := make([]any, 0, len(s.Params))
	for _, p := range s.Params {
		pm := value.MapOf(
			"name", p.Name,
			"type", p.Type.String(),
			"required", p.Required,
		)
		if p.Default != nil {
			pm.Set("default", p.Default)
		}
		if p.Doc != "" {
			pm.Set("doc", p.Doc)
		}
		if p.Variadic {
			pm.Set("variadic", true)
		}
		if p.KeywordOnly {
			pm.Set("keyword_only", true)
		}
		if len(p.Choices) > 0 {
			choices := make([]any, len(p.Choices))
			for i, c := range p.Choices {
				choices[i] = c
			}
			pm.Set("choices", choices)
		}
		params = append(params, pm)
	}

	m := value.MapOf(
		"module", s.Module,
		"function", s.Function,
		"params", params,
		"mutates", s.Mutates,
		"test_mode", s.TestMode.String(),
		"arbitrary_code", s.ArbitraryCode,
	)
	if s.Doc != "" {
		m.Set("doc", s.Doc)
	}
	if s.Returns != "" {
		m.Set("returns", s.Returns)
	}
	if len(s.Platforms) > 0 {
		plats := make([]any, len(s.Platforms))
		for i, p := range s.Platforms {
			plats[i] = p
		}
		m.Set("platforms", plats)
	}
	if len(s.Privileges) > 0 {
		privs := make([]any, len(s.Privileges))
		for i, p := range s.Privileges {
			privs[i] = p
		}
		m.Set("privileges", privs)
	}
	if s.Section != "" {
		m.Set("spec_section", s.Section)
	}
	return m
}
