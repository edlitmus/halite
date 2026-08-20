package state

import (
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// Reserved top-level keys in an SLS file, which declare structure rather
// than state.
const (
	keyInclude = "include"
	keyExtend  = "extend"
	keyExclude = "exclude"
)

// FuncDecl is one `module.function` declared under a state ID, with the
// arguments written beneath it.
type FuncDecl struct {
	// State is the state module, such as "file".
	State string
	// Fun is the function, such as "managed".
	Fun string
	// Args are the named arguments, in declaration order.
	Args *value.Map
	// Flags are the bare-string entries in the argument list, which is how
	// Salt writes an option that takes no value, such as
	// `- reload_modules`.
	Flags []string
	Pos   value.Pos
}

// Name is the dotted form.
func (f *FuncDecl) Name() string { return f.State + "." + f.Fun }

// Decl is one state ID and everything declared under it.
//
// An ID is global across the whole compiled state rather than scoped to
// its SLS. That is a design flaw in Salt and it is reproduced here,
// because trees depend on it; a lint rule flags cross-SLS ID references so
// a site can migrate toward `sls:` requisites if it chooses. SPEC section
// 11.3.
type Decl struct {
	ID  string
	SLS string
	Env string
	Pos value.Pos
	// Funcs are the module.function declarations under this ID, in
	// declaration order.
	Funcs []*FuncDecl
	// Order is the position of this declaration in the assembled high
	// state, which is the tiebreak for unconstrained states.
	Order int
}

// Func finds a declaration by state module name.
func (d *Decl) Func(state string) (*FuncDecl, bool) {
	for _, f := range d.Funcs {
		if f.State == state {
			return f, true
		}
	}
	return nil, false
}

// HighState is the assembled declaration structure of a whole run, in
// declaration order.
type HighState struct {
	decls []*Decl
	index map[string]int
	// excludedSLS and excludedIDs are applied after assembly.
	excludedSLS map[string]bool
	excludedIDs map[string]bool
}

// NewHighState returns an empty high state.
func NewHighState() *HighState {
	return &HighState{
		index:       map[string]int{},
		excludedSLS: map[string]bool{},
		excludedIDs: map[string]bool{},
	}
}

// Decls returns the declarations in order.
func (h *HighState) Decls() []*Decl { return h.decls }

// Lookup finds a declaration by ID.
func (h *HighState) Lookup(id string) (*Decl, bool) {
	i, ok := h.index[id]
	if !ok {
		return nil, false
	}
	return h.decls[i], true
}

// Len reports the number of declarations.
func (h *HighState) Len() int { return len(h.decls) }

// add appends a declaration, reporting a duplicate ID as an error naming
// both files, which is what Salt does.
func (h *HighState) add(d *Decl, diags *Diags) {
	if i, ok := h.index[d.ID]; ok {
		prev := h.decls[i]
		diags.AddRelated(d.Pos, d.SLS, d.ID,
			[]Related{{Pos: prev.Pos, Msg: "first declared here, in " + prev.SLS}},
			"duplicate state ID %q; an ID is global across the whole compiled state", d.ID)
		return
	}
	d.Order = len(h.decls)
	h.index[d.ID] = len(h.decls)
	h.decls = append(h.decls, d)
}

// parseSLS turns one rendered SLS document into declarations, plus the
// include, extend, and exclude directives it carries.
type slsContent struct {
	Includes []includeRef
	Extends  *value.Map
	Excludes []excludeRef
	Decls    []*Decl
}

type includeRef struct {
	Name string
	Env  string
	Pos  value.Pos
}

type excludeRef struct {
	// Exactly one of SLS or ID is set.
	SLS string
	ID  string
	Pos value.Pos
}

// parseSLS reads a rendered SLS value into its parts. It reports every
// structural problem it finds rather than stopping at the first.
func parseSLS(v any, sls, env string, diags *Diags) *slsContent {
	out := &slsContent{}
	if v == nil {
		return out
	}
	top, ok := v.(*value.Map)
	if !ok {
		diags.Add(value.Pos{File: sls}, sls, "",
			"an SLS file must hold a mapping of state IDs, found %s", value.TypeName(v))
		return out
	}

	for _, e := range top.Entries() {
		id := value.KeyString(e.Key)
		switch id {
		case keyInclude:
			out.Includes = append(out.Includes, parseIncludes(e.Val, sls, env, e.ValPos, diags)...)
			continue
		case keyExtend:
			m, ok := e.Val.(*value.Map)
			if !ok {
				diags.Add(e.ValPos, sls, "", "extend must hold a mapping of state IDs, found %s", value.TypeName(e.Val))
				continue
			}
			out.Extends = m
			continue
		case keyExclude:
			out.Excludes = append(out.Excludes, parseExcludes(e.Val, sls, e.ValPos, diags)...)
			continue
		}
		// Salt injects these into rendered data; they are not states.
		if strings.HasPrefix(id, "__") && strings.HasSuffix(id, "__") {
			continue
		}

		d := parseDecl(id, e, sls, env, diags)
		if d != nil {
			out.Decls = append(out.Decls, d)
		}
	}
	return out
}

func parseIncludes(v any, sls, env string, pos value.Pos, diags *Diags) []includeRef {
	items, ok := v.([]any)
	if !ok {
		diags.Add(pos, sls, "", "include must hold a list, found %s", value.TypeName(v))
		return nil
	}
	var out []includeRef
	for _, item := range items {
		switch t := item.(type) {
		case string:
			out = append(out, includeRef{Name: resolveRelative(t, sls), Env: env, Pos: pos})
		case *value.Map:
			// `- env: [sls1, sls2]` selects a different environment.
			for _, e := range t.Entries() {
				otherEnv := value.KeyString(e.Key)
				names, ok := e.Val.([]any)
				if !ok {
					diags.Add(e.ValPos, sls, "", "an environment include must hold a list of SLS names")
					continue
				}
				for _, n := range names {
					s, ok := n.(string)
					if !ok {
						diags.Add(e.ValPos, sls, "", "an SLS name must be a string, found %s", value.TypeName(n))
						continue
					}
					out = append(out, includeRef{Name: resolveRelative(s, sls), Env: otherEnv, Pos: e.ValPos})
				}
			}
		default:
			diags.Add(pos, sls, "", "an include entry must be an SLS name, found %s", value.TypeName(item))
		}
	}
	return out
}

// resolveRelative expands Salt's leading-dot relative include, where `.foo`
// inside `web.nginx` means `web.foo`.
func resolveRelative(name, sls string) string {
	if !strings.HasPrefix(name, ".") {
		return name
	}
	parent := sls
	if i := strings.LastIndex(sls, "."); i >= 0 {
		parent = sls[:i]
	} else {
		parent = ""
	}
	// Each extra leading dot climbs one more level.
	rest := strings.TrimLeft(name, ".")
	up := len(name) - len(rest) - 1
	for i := 0; i < up; i++ {
		if j := strings.LastIndex(parent, "."); j >= 0 {
			parent = parent[:j]
			continue
		}
		parent = ""
	}
	if parent == "" {
		return rest
	}
	return parent + "." + rest
}

func parseExcludes(v any, sls string, pos value.Pos, diags *Diags) []excludeRef {
	items, ok := v.([]any)
	if !ok {
		diags.Add(pos, sls, "", "exclude must hold a list, found %s", value.TypeName(v))
		return nil
	}
	var out []excludeRef
	for _, item := range items {
		m, ok := item.(*value.Map)
		if !ok || m.Len() != 1 {
			diags.Add(pos, sls, "", "an exclude entry must be `- sls: name` or `- id: name`")
			continue
		}
		e := m.Entries()[0]
		name, _ := e.Val.(string)
		switch value.KeyString(e.Key) {
		case "sls":
			out = append(out, excludeRef{SLS: name, Pos: e.ValPos})
		case "id":
			out = append(out, excludeRef{ID: name, Pos: e.ValPos})
		default:
			diags.Add(e.KeyPos, sls, "", "an exclude entry must be `- sls: name` or `- id: name`, found %q", value.KeyString(e.Key))
		}
	}
	return out
}

// parseDecl reads one state ID's declarations.
func parseDecl(id string, e value.Entry, sls, env string, diags *Diags) *Decl {
	d := &Decl{ID: id, SLS: sls, Env: env, Pos: e.KeyPos}

	body, ok := e.Val.(*value.Map)
	if !ok {
		diags.Add(e.ValPos, sls, id,
			"a state ID must hold a mapping of module.function declarations, found %s", value.TypeName(e.Val))
		return nil
	}

	for _, fe := range body.Entries() {
		key := value.KeyString(fe.Key)
		// Salt injects these; they are not declarations.
		if strings.HasPrefix(key, "__") && strings.HasSuffix(key, "__") {
			continue
		}
		f := parseFuncDecl(key, fe, sls, id, diags)
		if f != nil {
			d.Funcs = append(d.Funcs, f)
		}
	}
	if len(d.Funcs) == 0 {
		diags.Add(e.ValPos, sls, id, "this state ID declares no module.function")
		return nil
	}
	return d
}

// parseFuncDecl reads one `module.function` block, in any of the three
// spellings real Salt trees use.
func parseFuncDecl(key string, fe value.Entry, sls, id string, diags *Diags) *FuncDecl {
	f := &FuncDecl{Args: value.NewMap(8), Pos: fe.KeyPos}

	if i := strings.Index(key, "."); i >= 0 {
		f.State, f.Fun = key[:i], key[i+1:]
	} else {
		// The split form: the module is the key and the function is the
		// first bare string in the argument list.
		f.State = key
	}

	switch body := fe.Val.(type) {
	case nil:
		// `test.nop:` with no arguments at all.

	case []any:
		for _, item := range body {
			switch arg := item.(type) {
			case string:
				if f.Fun == "" {
					f.Fun = arg
					continue
				}
				f.Flags = append(f.Flags, arg)
			case *value.Map:
				for _, ae := range arg.Entries() {
					name := value.KeyString(ae.Key)
					if f.Args.Has(name) {
						diags.AddRelated(ae.KeyPos, sls, id,
							[]Related{{Pos: mustEntryPos(f.Args, name), Msg: "first given here"}},
							"argument %q is given twice to %s", name, key)
						continue
					}
					f.Args.SetAt(name, ae.Val, ae.KeyPos, ae.ValPos)
				}
			default:
				diags.Add(fe.ValPos, sls, id,
					"an argument to %s must be `- name: value` or a bare option, found %s", key, value.TypeName(item))
			}
		}

	case *value.Map:
		// The shorthand dictionary form.
		for _, ae := range body.Entries() {
			f.Args.SetAt(value.KeyString(ae.Key), ae.Val, ae.KeyPos, ae.ValPos)
		}

	default:
		diags.Add(fe.ValPos, sls, id,
			"the arguments to %s must be a list or a mapping, found %s", key, value.TypeName(fe.Val))
		return nil
	}

	if f.Fun == "" {
		diags.Add(fe.KeyPos, sls, id,
			"%q names a module with no function; write it as module.function", key)
		return nil
	}
	return f
}

func mustEntryPos(m *value.Map, key string) value.Pos {
	if e, ok := m.Entry(key); ok {
		return e.KeyPos
	}
	return value.Pos{}
}
