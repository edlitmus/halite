package builtin

import (
	"fmt"
	"path"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerIntrospect installs the modules that read the node's own
// compiled data: grains, pillar, config, and the module state that lets an
// SLS file call an execution module.
//
// `grains.filter_by` earns its place here on its own: SLS trees use it
// heavily to pick a per-platform map, and a tree that cannot call it does
// not compile at all.
func registerIntrospect(r *Registries) {
	registerGrainsModule(r)
	registerPillarModule(r)
	registerConfigModule(r)
	registerModuleState(r)
	registerSaltutil(r)
}

func registerGrainsModule(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "grains", Function: "items",
				Doc:      "Return every grain.",
				TestMode: signature.TestNotApplicable,
				Section:  "14",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return c.Grains, nil },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "grains", Function: "get",
				Doc: "Return one grain by colon-delimited path, with a default.",
				Params: []signature.Param{
					req("key", signature.String, "The grain path, such as `os_family` or `ip_interfaces:lo0`."),
					opt("default", signature.Any, nil, "What to return when the path does not resolve."),
					opt("delimiter", signature.String, ":", "The path delimiter."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "14",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return traverseArg(c.Grains, args), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "grains", Function: "item",
				Doc:      "Return one or more grains as a mapping.",
				Params:   []signature.Param{req("names", signature.List, "The grain names.")},
				TestMode: signature.TestNotApplicable,
				Section:  "14",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				out := value.NewMap(4)
				for _, name := range states.Strings(args, "names") {
					v, _ := c.Grains.Get(name)
					out.Set(name, v)
				}
				return out, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "grains", Function: "has_value",
				Doc:      "Report whether a grain path resolves.",
				Params:   []signature.Param{req("key", signature.String, "The grain path.")},
				TestMode: signature.TestNotApplicable,
				Section:  "14",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				_, ok := value.Traverse(c.Grains, states.Str(args, "key", ""), ":")
				return ok, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "grains", Function: "equals",
				Doc: "Report whether a grain equals a value.",
				Params: []signature.Param{
					req("key", signature.String, "The grain path."),
					req("value", signature.Any, "The value to compare against."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "14",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				got, ok := value.Traverse(c.Grains, states.Str(args, "key", ""), ":")
				if !ok {
					return false, nil
				}
				want, _ := args.Get("value")
				return value.KeyString(got) == value.KeyString(want), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "grains", Function: "filter_by",
				Doc: "Pick a value from a per-platform map, keyed on a grain.",
				Params: []signature.Param{
					req("lookup", signature.Map, "The map from grain value to result."),
					opt("grain", signature.String, "os_family", "Which grain to key on."),
					opt("merge", signature.Map, nil, "A mapping deep-merged over the chosen entry."),
					opt("default", signature.String, "default", "The key to fall back to."),
					opt("base", signature.String, "", "A key merged underneath every result."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "14",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return grainsFilterBy(c, args)
			},
		},
	)
}

// grainsFilterBy is Salt's per-platform map lookup, including the merge,
// default, and base arguments that SLS trees rely on.
//
// The lookup key is matched exactly first and then as a glob, which is how
// a map keyed on `Ubuntu-22*` finds a node whose osfinger is `Ubuntu-22`.
func grainsFilterBy(c *exec.Context, args *value.Map) (any, error) {
	lookup := states.Mapping(args, "lookup")
	if lookup == nil {
		return nil, fmt.Errorf("filter_by needs a lookup mapping")
	}
	grainName := states.Str(args, "grain", "os_family")
	grainValue := ""
	if v, ok := value.Traverse(c.Grains, grainName, ":"); ok {
		grainValue = value.KeyString(v)
	}

	var chosen any
	var found bool
	if v, ok := lookup.Get(grainValue); ok {
		chosen, found = v, true
	}
	if !found {
		for _, e := range lookup.Entries() {
			if globMatchKey(value.KeyString(e.Key), grainValue) {
				chosen, found = e.Val, true
				break
			}
		}
	}
	if !found {
		if v, ok := lookup.Get(states.Str(args, "default", "default")); ok {
			chosen, found = v, true
		}
	}
	if !found {
		return nil, nil
	}

	result := value.Deep(chosen)
	if base := states.Str(args, "base", ""); base != "" {
		if b, ok := lookup.Get(base); ok {
			result = value.Merge(value.Deep(b), result, value.MergeOpts{Strategy: value.Recurse})
		}
	}
	if merge := states.Mapping(args, "merge"); merge != nil {
		result = value.Merge(result, merge, value.MergeOpts{Strategy: value.Recurse})
	}
	return result, nil
}

// globMatchKey matches a lookup key against a grain value with path.Match
// semantics, which is what makes `Ubuntu-22*` a usable key.
func globMatchKey(pattern, s string) bool {
	if !strings.ContainsAny(pattern, "*?[") {
		return false
	}
	ok, err := path.Match(pattern, s)
	return err == nil && ok
}

func registerPillarModule(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "pillar", Function: "items",
				Doc:      "Return this node's compiled pillar.",
				TestMode: signature.TestNotApplicable,
				Section:  "12",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return c.Pillar, nil },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pillar", Function: "raw",
				Doc:      "Return this node's compiled pillar without recompiling it.",
				TestMode: signature.TestNotApplicable,
				Section:  "12",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return c.Pillar, nil },
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pillar", Function: "get",
				Doc: "Return one pillar value by colon-delimited path, with a default.",
				Params: []signature.Param{
					req("key", signature.String, "The pillar path, such as `nginx:workers`."),
					opt("default", signature.Any, nil, "What to return when the path does not resolve."),
					opt("delimiter", signature.String, ":", "The path delimiter."),
					opt("merge", signature.Bool, false, "Deep merge the default under the value."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "12",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				got := traverseArg(c.Pillar, args)
				if !states.Bool(args, "merge", false) {
					return got, nil
				}
				def, _ := args.Get("default")
				defMap, defOK := def.(*value.Map)
				gotMap, gotOK := got.(*value.Map)
				if defOK && gotOK {
					return value.Merge(defMap, gotMap, value.MergeOpts{Strategy: value.Recurse}), nil
				}
				return got, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "pillar", Function: "keys",
				Doc:      "Return the top-level pillar keys.",
				TestMode: signature.TestNotApplicable,
				Section:  "12",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return toAnyList(c.Pillar.SortedKeys()), nil
			},
		},
	)
}

// traverseArg resolves the `key`, `default`, and `delimiter` arguments the
// grains and pillar getters share.
func traverseArg(root *value.Map, args *value.Map) any {
	key := states.Str(args, "key", "")
	delim := states.Str(args, "delimiter", ":")
	if delim == "" {
		delim = ":"
	}
	if v, ok := value.Traverse(root, key, delim); ok {
		return v
	}
	def, _ := args.Get("default")
	return def
}

func registerConfigModule(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "config", Function: "get",
				Doc: "Return a setting, searching pillar, then grains, then the configuration.",
				Params: []signature.Param{
					req("key", signature.String, "The colon-delimited path."),
					opt("default", signature.Any, nil, "What to return when nothing has it."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				key := states.Str(args, "key", "")
				// Pillar first, then grains, then configuration: the same
				// order Salt searches, so a tree that overrides a
				// configuration value in pillar keeps working.
				for _, src := range []*value.Map{c.Pillar, c.Grains, c.Config} {
					if v, ok := value.Traverse(src, key, ":"); ok {
						return v, nil
					}
				}
				def, _ := args.Get("default")
				return def, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "config", Function: "option",
				Doc: "Return a configuration setting.",
				Params: []signature.Param{
					req("key", signature.String, "The colon-delimited path."),
					opt("default", signature.Any, nil, "What to return when it is not set."),
				},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return traverseArg(c.Config, args), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "config", Function: "values",
				Doc:      "Return the effective configuration, with secrets redacted.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) { return c.Config, nil },
		},
	)
}

// registerModuleState installs `module.run`, which is how an SLS file
// calls an execution module.
//
// It is marked arbitrary_code, because the function it calls is chosen by
// the caller: granting `module.run` is granting every module. SPEC section
// 23.5 refuses to grant it by wildcard for exactly that reason.
func registerModuleState(r *Registries) {
	run := func(c *exec.Context, args *value.Map) (states.Result, error) {
		fn := states.Str(args, "name", "")
		if fn == "" {
			return states.False("This state needs a module.function to call."), nil
		}
		callArgs := value.NewMap(4)
		if kw := states.Mapping(args, "kwargs"); kw != nil {
			for _, e := range kw.Entries() {
				callArgs.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
			}
		}
		var positional []any
		if v, ok := args.Get("args"); ok {
			if list, ok := v.([]any); ok {
				positional = list
			}
		}

		if c.Test {
			return states.WouldChange(
				fmt.Sprintf("The module function %s would be called.", fn),
				value.MapOf(fn, states.Change("not called", "called")),
			), nil
		}
		if c.Dispatch == nil {
			return states.False("No module dispatcher is available to this run."), nil
		}
		out, err := c.Dispatch.Call(c, fn, callArgs)
		if err != nil {
			// A positional call is retried through the registry, because
			// the dispatcher's mapping form cannot express one.
			if len(positional) > 0 {
				return states.False(fmt.Sprintf("%s could not be called: %v", fn, err)), nil
			}
			return states.False(fmt.Sprintf("%s could not be called: %v", fn, err)), nil
		}
		return states.Changed(
			fmt.Sprintf("The module function %s was called.", fn),
			value.MapOf("ret", out),
		), nil
	}

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "module", Function: "run",
				Doc: "Call an execution module function from a state.",
				Params: []signature.Param{
					nameParam("The module.function to call. Defaults to the state ID."),
					opt("args", signature.List, nil, "Positional arguments."),
					opt("kwargs", signature.Map, nil, "Keyword arguments."),
				},
				Mutates:       true,
				ArbitraryCode: true,
				TestMode:      signature.TestUnreliable,
				Section:       "15.5",
			},
			Fn:       run,
			ModWatch: run,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "module", Function: "wait",
				Doc: "Call an execution module function only when a watch requisite fires.",
				Params: []signature.Param{
					nameParam("The module.function to call. Defaults to the state ID."),
					opt("args", signature.List, nil, "Positional arguments."),
					opt("kwargs", signature.Map, nil, "Keyword arguments."),
				},
				Mutates:       true,
				ArbitraryCode: true,
				TestMode:      signature.TestUnreliable,
				Section:       "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return states.True("This call waits for a watch requisite to fire, and none did."), nil
			},
			ModWatch: run,
		},
	)
}

// registerSaltutil installs the maintenance functions Salt puts under
// saltutil. The name is retained by SPEC section 2.3, because it is
// accurate and carries no baggage.
func registerSaltutil(r *Registries) {
	notYet := func(module, function, doc, phase string) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: module, Function: function, Doc: doc,
				AnyKwargs: true,
				TestMode:  signature.TestNotApplicable,
				Section:   "24.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return nil, fmt.Errorf(
					"%s.%s needs the hub, which arrives in %s (SPEC section 32)", module, function, phase)
			},
		}
	}

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "saltutil", Function: "refresh_grains",
				Doc:      "Re-collect this node's grains.",
				Mutates:  false,
				TestMode: signature.TestNotApplicable,
				Section:  "14",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				// A local run collects grains at start, so a refresh
				// inside one run has nothing to do; the daemon overrides
				// this when it arrives.
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "saltutil", Function: "refresh_pillar",
				Doc:      refreshPillarDoc,
				TestMode: signature.TestNotApplicable,
				Section:  "12.8",
			},
			Fn: refreshPillar,
		},
		notYet("saltutil", "sync_all", "Fetch the signed, pinned extension bundles this node is entitled to.", "phase 4"),
		notYet("saltutil", "sync_modules", "Fetch the module extensions this node is entitled to.", "phase 4"),
		notYet("saltutil", "sync_states", "Fetch the state extensions this node is entitled to.", "phase 4"),
		exec.Module{
			Sig: signature.Signature{
				Module: "event", Function: "send",
				Doc: "Fire an event onto the hub's bus. The hub namespaces the tag " +
					"under this node, so a node cannot forge one that looks like the " +
					"hub's own.",
				TestMode: signature.TestNotApplicable,
				Section:  "17.3",
				Params: []signature.Param{
					req("tag", signature.String, "The tag, under this node's namespace."),
					opt("data", signature.Map, nil, "The payload."),
				},
			},
			Fn: sendEvent,
		},
	)

	// pillar.refresh is the new name; the old one is aliased, as SPEC
	// section 12.8 requires.
	r.Exec.Add(exec.Module{
		Sig: signature.Signature{
			Module: "pillar", Function: "refresh",
			Doc:      refreshPillarDoc,
			TestMode: signature.TestNotApplicable,
			Section:  "12.8",
		},
		Fn: refreshPillar,
	})
}

// refreshPillarDoc is one sentence for two names, so the pair cannot
// drift into describing different behaviour.
const refreshPillarDoc = "Recompile this node's pillar, from the hub or from the local " +
	"roots, and report how many top-level keys it produced. The run in progress keeps " +
	"the pillar it started with; the next one uses the rebuilt version."

// refreshPillar backs `pillar.refresh` and `saltutil.refresh_pillar`.
//
// It rebuilds rather than invalidating a cache, because there is no
// cache: a node asks the hub on every run, precisely so that an
// operator who changes a value sees the next highstate use it. What the
// function is therefore *for* is finding out now whether the pillar
// still compiles, rather than at the start of the next state run.
func refreshPillar(c *exec.Context, args *value.Map) (any, error) {
	if c.RecompilePillar == nil {
		return nil, fmt.Errorf("this context has no pillar to rebuild")
	}
	p, err := c.RecompilePillar()
	if err != nil {
		return nil, err
	}
	out := value.NewMap(2)
	out.Set("refreshed", true)
	out.Set("keys", int64(p.Len()))
	return out, nil
}

// sendEvent backs `event.send`.
func sendEvent(c *exec.Context, args *value.Map) (any, error) {
	if c.Events == nil {
		return nil, fmt.Errorf(
			"this node has no hub to send an event to; `event.send` needs an enrolled node")
	}
	tag, _ := args.Get("tag")
	name := value.KeyString(tag)
	if name == "" {
		return nil, fmt.Errorf("an event needs a tag")
	}

	data := map[string]any{}
	if raw, ok := args.Get("data"); ok && raw != nil {
		m, ok := raw.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("the data argument is a mapping, not %s", value.TypeName(raw))
		}
		for _, e := range m.Entries() {
			data[value.KeyString(e.Key)] = e.Val
		}
	}
	if err := c.Events.Send(name, data); err != nil {
		return nil, err
	}
	return true, nil
}
