package builtin

import (
	"sort"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerSys installs the introspection module.
//
// Salt derives all of this by Python introspection at runtime. Here it
// reads the build-time signature registry, so `sys.doc` answers without
// executing anything. SPEC section 15.6.
func registerSys(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "list_modules",
				Doc:      "List the execution modules this build ships.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.6",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return toAnyList(r.Exec.Signatures().Modules()), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "list_aliases",
				Doc: "List the per-platform module names of SPEC 15.3 that resolve to a virtual module, and what each stands for.",
				// Listed apart from list_modules rather than mixed into
				// it. An alias is a second name for functions that are
				// already counted once, and folding it in would make
				// the module list disagree with the function list for a
				// reason that reads as a discrepancy.
				TestMode: signature.TestNotApplicable,
				Section:  "15.6",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				aliases := r.Exec.Aliases()
				out := value.NewMap(len(aliases))
				for _, name := range sortedAliasNames(aliases) {
					a := aliases[name]
					entry := value.NewMap(3)
					entry.Set("module", a.Module)
					entry.Set("provider", a.Provider)
					// Whether this node is one where the name means
					// anything, which is the question an operator has
					// when they are looking at the list.
					if a.Usable != nil {
						if err := a.Usable(c); err != nil {
							entry.Set("usable_here", false)
							entry.Set("why_not", err.Error())
						} else {
							entry.Set("usable_here", true)
						}
					}
					out.Set(name, entry)
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "list_extensions",
				Doc:      "List the signed extensions this node has loaded, and what confines each.",
				TestMode: signature.TestNotApplicable,
				Section:  "24.4",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				// A node with no extensions answers with an empty list
				// rather than an error: "none" is the normal case and
				// the common one, and an error would make a check for
				// "is anything unsigned here" fail on every ordinary
				// node.
				if c.Extensions == nil {
					return []any{}, nil
				}
				out := make([]any, 0)
				for _, described := range c.Extensions() {
					entry := value.NewMap(len(described))
					for _, key := range sortedKeys(described) {
						entry.Set(key, described[key])
					}
					out = append(out, entry)
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "list_functions",
				Doc: "List the execution module functions this build ships.",
				Params: []signature.Param{
					opt("module", signature.String, "", "Restrict the list to one module."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.6",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if m := states.Str(args, "module", ""); m != "" {
					var out []string
					for _, s := range r.Exec.Signatures().Functions(m) {
						out = append(out, s.Name())
					}
					return toAnyList(out), nil
				}
				return toAnyList(r.Exec.Names()), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "list_state_modules",
				Doc:      "List the state modules this build ships.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.6",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return toAnyList(r.States.Modules()), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "list_state_functions",
				Doc:      "List the state functions this build ships.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.6",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return toAnyList(r.States.Names()), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "doc",
				Doc: "Return the documentation for a module or a function.",
				Params: []signature.Param{
					opt("name", signature.String, "", "A module or a module.function."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.6",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return docFor(r.Exec.Signatures(), states.Str(args, "name", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "state_doc",
				Doc: "Return the documentation for a state module or function.",
				Params: []signature.Param{
					opt("name", signature.String, "", "A module or a module.function."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.6",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return docFor(r.States.Signatures(), states.Str(args, "name", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "argspec",
				Doc: "Return the machine-readable signature of a function.",
				Params: []signature.Param{
					opt("name", signature.String, "", "A module or a module.function."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.6",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return argspecFor(r.Exec.Signatures(), states.Str(args, "name", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "sys", Function: "state_argspec",
				Doc: "Return the machine-readable signature of a state function.",
				Params: []signature.Param{
					opt("name", signature.String, "", "A module or a module.function."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.6",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return argspecFor(r.States.Signatures(), states.Str(args, "name", "")), nil
			},
		},
	)
}

// docFor renders documentation for one name, one module, or everything.
func docFor(reg *signature.Registry, name string) *value.Map {
	out := value.NewMap(16)
	if sig, ok := reg.Lookup(name); ok {
		out.Set(sig.Name(), sig.Describe())
		return out
	}
	for _, n := range reg.Names() {
		if name != "" && !matchesModule(n, name) {
			continue
		}
		sig, _ := reg.Lookup(n)
		out.Set(n, sig.Describe())
	}
	return out
}

func argspecFor(reg *signature.Registry, name string) *value.Map {
	out := value.NewMap(16)
	if sig, ok := reg.Lookup(name); ok {
		out.Set(sig.Name(), sig.JSON())
		return out
	}
	for _, n := range reg.Names() {
		if name != "" && !matchesModule(n, name) {
			continue
		}
		sig, _ := reg.Lookup(n)
		out.Set(n, sig.JSON())
	}
	return out
}

func matchesModule(functionName, module string) bool {
	return len(functionName) > len(module) &&
		functionName[:len(module)] == module &&
		functionName[len(module)] == '.'
}

func toAnyList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// sortedKeys keeps `sys.list_extensions` rendering the same way twice,
// which matters because an operator compares two nodes' output.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedAliasNames(m map[string]exec.Alias) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
