package builtin

import (
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
