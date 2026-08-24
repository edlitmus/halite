package builtin

import (
	"fmt"
	"sort"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// registerMine installs the mine functions of SPEC section 19.5.
//
// The mine is how a state on one node learns something about another: a
// load balancer's configuration reads the backend list, and the backends
// are what published it. Everything goes through the hub, because a node
// asking another node directly would be a second authorization surface
// and a connection in the wrong direction (SPEC 5.1).
func registerMine(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "mine", Function: "get",
				Doc: "What the matched nodes published for one function. The hub " +
					"authorizes this node as a `node:` principal, and the publisher's " +
					"own `allow_tgt` decides as well.",
				TestMode: signature.TestNotApplicable,
				Section:  "19.5",
				Params: []signature.Param{
					req("tgt", signature.String, "Which nodes."),
					req("fun", signature.String, "The mine function."),
					opt("tgt_type", signature.String, "glob",
						"The target kind of SPEC section 8, such as G or C."),
				},
			},
			Fn: mineGet,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mine", Function: "send",
				Doc: "Compute one function now and publish it, without waiting for the " +
					"interval.",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "19.5",
				Params: []signature.Param{
					req("name", signature.String, "The mine name to publish under."),
					opt("mine_function", signature.String, "",
						"The function to call; defaults to the name."),
					opt("allow_tgt", signature.String, "", "Which nodes may read it."),
					opt("allow_tgt_type", signature.String, "", "The kind of that target."),
					opt("kwargs", signature.Map, nil, "Arguments for the function."),
				},
			},
			Fn: mineSend,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mine", Function: "update",
				Doc: "Recompute everything `mine_functions` names and publish it, " +
					"replacing what this node published before.",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "19.5",
			},
			Fn: mineUpdate,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mine", Function: "delete",
				Doc:      "Stop publishing one function.",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "19.5",
				Params: []signature.Param{
					req("name", signature.String, "The mine name."),
				},
			},
			Fn: mineDelete,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mine", Function: "flush",
				Doc:      "Stop publishing anything.",
				Mutates:  true,
				TestMode: signature.TestReliable,
				Section:  "19.5",
			},
			Fn: mineFlush,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "mine", Function: "valid",
				Doc:      "The mine functions this node is configured to publish.",
				TestMode: signature.TestNotApplicable,
				Section:  "19.5",
			},
			Fn: mineValid,
		},
	)
}

func mineAccess(c *exec.Context) (exec.MineAccess, error) {
	if c.Mine == nil {
		return nil, fmt.Errorf(
			"this node has no hub, and the mine lives on the hub; `mine.*` needs an enrolled node")
	}
	return c.Mine, nil
}

func mineGet(c *exec.Context, args *value.Map) (any, error) {
	mine, err := mineAccess(c)
	if err != nil {
		return nil, err
	}
	kind := value.KeyString(mustArg(args, "tgt_type"))
	if kind == "glob" {
		kind = ""
	}
	return mine.Fetch(value.KeyString(mustArg(args, "tgt")), kind,
		value.KeyString(mustArg(args, "fun")))
}

func mineSend(c *exec.Context, args *value.Map) (any, error) {
	mine, err := mineAccess(c)
	if err != nil {
		return nil, err
	}
	name := value.KeyString(mustArg(args, "name"))
	fun := value.KeyString(mustArg(args, "mine_function"))
	if fun == "" {
		fun = name
	}
	callArgs := value.NewMap(0)
	if m, ok := mustArg(args, "kwargs").(*value.Map); ok && m != nil {
		callArgs = m
	}
	if c.Test {
		return value.MapOf("would_publish", name, "function", fun), nil
	}

	got, err := c.Call(fun, callArgs)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", fun, err)
	}
	err = mine.Publish(map[string]exec.MineValue{name: {
		Data:         got,
		AllowTgt:     value.KeyString(mustArg(args, "allow_tgt")),
		AllowTgtType: value.KeyString(mustArg(args, "allow_tgt_type")),
	}}, false)
	if err != nil {
		return nil, err
	}
	return true, nil
}

func mineUpdate(c *exec.Context, args *value.Map) (any, error) {
	mine, err := mineAccess(c)
	if err != nil {
		return nil, err
	}
	configured, err := MineFunctions(c)
	if err != nil {
		return nil, err
	}
	if c.Test {
		return value.MapOf("would_publish", int64(len(configured))), nil
	}

	published := map[string]exec.MineValue{}
	for name, spec := range configured {
		got, err := c.Call(spec.Function, spec.Args)
		if err != nil {
			// One function that will not run must not stop the rest:
			// a mine with three of four entries is more use than none,
			// and the failure is reported rather than swallowed.
			c.Logf("warn", "a mine function failed: %s: %v", spec.Function, err)
			continue
		}
		published[name] = exec.MineValue{
			Data: got, AllowTgt: spec.AllowTgt, AllowTgtType: spec.AllowTgtType,
		}
	}
	// Replacing, so a function taken out of `mine_functions` stops
	// being served rather than lingering for ever.
	if err := mine.Publish(published, true); err != nil {
		return nil, err
	}
	return int64(len(published)), nil
}

func mineDelete(c *exec.Context, args *value.Map) (any, error) {
	mine, err := mineAccess(c)
	if err != nil {
		return nil, err
	}
	if c.Test {
		return true, nil
	}
	// Publishing the remaining set is how one entry goes: the node
	// owns its own data, and the hub takes what it is given.
	configured, err := MineFunctions(c)
	if err != nil {
		return nil, err
	}
	delete(configured, value.KeyString(mustArg(args, "name")))
	published := map[string]exec.MineValue{}
	for name, spec := range configured {
		got, err := c.Call(spec.Function, spec.Args)
		if err != nil {
			continue
		}
		published[name] = exec.MineValue{
			Data: got, AllowTgt: spec.AllowTgt, AllowTgtType: spec.AllowTgtType,
		}
	}
	if err := mine.Publish(published, true); err != nil {
		return nil, err
	}
	return true, nil
}

func mineFlush(c *exec.Context, args *value.Map) (any, error) {
	mine, err := mineAccess(c)
	if err != nil {
		return nil, err
	}
	if c.Test {
		return true, nil
	}
	if err := mine.Publish(map[string]exec.MineValue{}, true); err != nil {
		return nil, err
	}
	return true, nil
}

func mineValid(c *exec.Context, args *value.Map) (any, error) {
	configured, err := MineFunctions(c)
	if err != nil {
		return nil, err
	}
	out := value.NewMap(len(configured))
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		spec := configured[name]
		entry := value.NewMap(3)
		entry.Set("function", spec.Function)
		if spec.Args.Len() > 0 {
			entry.Set("args", spec.Args)
		}
		if spec.AllowTgt != "" {
			entry.Set("allow_tgt", spec.AllowTgt)
		}
		out.Set(name, entry)
	}
	return out, nil
}

// MineSpec is one entry of `mine_functions`.
type MineSpec struct {
	Function     string
	Args         *value.Map
	AllowTgt     string
	AllowTgtType string
}

// MineFunctions reads what this node is configured to publish.
//
// Salt's schema, which writes the function as the key and its arguments
// beside it:
//
//	mine_functions:
//	  network.ip_addrs:
//	    - eth0
//	  backends:
//	    mine_function: grains.get
//	    key: roles
//	    allow_tgt: 'lb*'
func MineFunctions(c *exec.Context) (map[string]MineSpec, error) {
	raw, ok := c.Config.Get("mine_functions")
	if !ok || raw == nil {
		return map[string]MineSpec{}, nil
	}
	m, ok := raw.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("`mine_functions` is a mapping of name to what it publishes")
	}

	out := map[string]MineSpec{}
	for _, e := range m.Entries() {
		name := value.KeyString(e.Key)
		spec := MineSpec{Function: name, Args: value.NewMap(0)}
		switch body := e.Val.(type) {
		case nil:
		case []any:
			// Salt's positional form: the key is the function and the
			// list is its arguments.
			for i, item := range body {
				spec.Args.Set(fmt.Sprintf("arg%d", i), item)
			}
		case *value.Map:
			for _, arg := range body.Entries() {
				key := value.KeyString(arg.Key)
				switch key {
				case "mine_function":
					spec.Function = value.KeyString(arg.Val)
				case "allow_tgt":
					spec.AllowTgt = value.KeyString(arg.Val)
				case "allow_tgt_type":
					spec.AllowTgtType = value.KeyString(arg.Val)
				default:
					spec.Args.Set(key, arg.Val)
				}
			}
		default:
			return nil, fmt.Errorf("mine_functions %s: %s is not a list or a mapping",
				name, value.TypeName(e.Val))
		}
		if spec.Function == "" {
			return nil, fmt.Errorf("mine_functions %s names no function", name)
		}
		out[name] = spec
	}
	return out, nil
}
