package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerDefaults installs the `defaults` module, which is how a Salt
// formula merges its shipped defaults with the pillar that overrides
// them.
//
// The idiom is one line in every `map.jinja` in the wild:
//
//	{% import_yaml "defaults.yaml" as defaults %}
//	{% set lookup = salt['pillar.get']('salt:lookup', default={}, merge=True) %}
//	{% do salt['defaults.merge'](defaults['salt'], lookup) %}
//
// and the `{% do %}` is the whole difficulty. The call's return value is
// discarded, so the merge has to happen **in the destination mapping**.
// An implementation that returned a new map would leave the template
// with its defaults unmerged and no error anywhere -- the shape of the
// `cloud_grains` defect in 5.78, where a value was computed and dropped.
// So `in_place` is honoured rather than accepted, and it is the default,
// as Salt has it.
//
// `defaults.get` is deliberately absent. It resolves a `defaults.yaml`
// relative to the *formula* being rendered, which is a file-server
// question rather than a data one, and a tree reaching for it wants
// something this does not yet have. Refusing by name beats answering
// with the wrong file.
func registerDefaults(r *Registries) {
	mergeParams := []signature.Param{
		req("dest", signature.Map, "The mapping to merge into."),
		req("src", signature.Map, "The mapping to merge from."),
		opt("merge_lists", signature.Bool, false, "Concatenate lists rather than replacing them."),
		opt("in_place", signature.Bool, true, "Merge into dest itself. False returns a new mapping and leaves dest alone."),
		opt("convert_none", signature.Bool, true, "Treat a null dest or src as an empty mapping."),
	}

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "defaults", Function: "merge",
				Doc:     "Deep-merge one mapping into another, in place by default.",
				Params:  mergeParams,
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return defaultsMerge(args, false)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "defaults", Function: "update",
				Doc: "Merge defaults into each value of a mapping of mappings, in place by default. " +
					"Lists merge unless merge_lists says otherwise, which is the one way it differs from merge.",
				Params:  mergeParams,
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return defaultsMerge(args, true)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "defaults", Function: "deepcopy",
				Doc: "Copy a mapping so that merging into the copy leaves the original alone.",
				Params: []signature.Param{
					req("source", signature.Map, "The mapping to copy."),
				},
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				src := states.Mapping(args, "source")
				if src == nil {
					return value.NewMap(0), nil
				}
				return src.Clone(), nil
			},
		},
	)
}

// defaultsMerge implements both merge and update.
//
// `update` differs from `merge` in exactly two ways, which is why they
// share a body: it merges lists by default, and it applies the defaults
// to each *value* of dest rather than to dest itself, which is what
// makes `{% do salt['defaults.update'](group.nodes, group.defaults) %}`
// give every node the group's settings.
func defaultsMerge(args *value.Map, perValue bool) (any, error) {
	convertNone := states.Bool(args, "convert_none", true)
	inPlace := states.Bool(args, "in_place", true)

	dest, destOK := args.Get("dest")
	src, _ := args.Get("src")

	destMap, _ := dest.(*value.Map)
	srcMap, _ := src.(*value.Map)
	if srcMap == nil {
		if !convertNone && src != nil {
			return nil, fmt.Errorf("defaults: src is %s, not a mapping", value.TypeName(src))
		}
		srcMap = value.NewMap(0)
	}
	if destMap == nil {
		if !destOK || dest == nil {
			if convertNone && inPlace {
				// Salt raises here rather than silently doing nothing,
				// and the reason is worth keeping: there is no mapping
				// to merge into, so an in-place merge cannot have any
				// effect and reporting success would be a lie.
				return nil, fmt.Errorf(
					"defaults: dest is null and in_place is set, so there is nothing to merge into")
			}
			destMap = value.NewMap(0)
		} else {
			return nil, fmt.Errorf("defaults: dest is %s, not a mapping", value.TypeName(dest))
		}
	}

	opts := value.MergeOpts{
		Strategy:   value.Recurse,
		MergeLists: states.Bool(args, "merge_lists", perValue),
	}

	apply := func(into *value.Map) *value.Map {
		merged, _ := value.Merge(into, srcMap, opts).(*value.Map)
		if merged == nil {
			return into
		}
		if !inPlace {
			return merged
		}
		replaceInPlace(into, merged)
		return into
	}

	if !perValue {
		return apply(destMap), nil
	}

	// update walks one level down: each value of dest gets the defaults.
	out := destMap
	if !inPlace {
		out = destMap.Clone()
	}
	for _, e := range out.Entries() {
		if m, ok := e.Val.(*value.Map); ok {
			merged, _ := value.Merge(m, srcMap, opts).(*value.Map)
			if merged == nil {
				continue
			}
			if inPlace {
				replaceInPlace(m, merged)
				continue
			}
			out.Set(e.Key, merged)
		}
	}
	return out, nil
}

// replaceInPlace makes dst hold exactly what merged holds, without
// swapping the pointer.
//
// The pointer is the point: the template holds a reference to the
// mapping it passed in, and `{% do %}` throws the return value away. A
// new map, however correct, is invisible to the caller.
func replaceInPlace(dst, merged *value.Map) {
	if dst == merged {
		return
	}
	for _, k := range dst.Keys() {
		dst.Delete(k)
	}
	for _, e := range merged.Entries() {
		dst.SetAt(e.Key, e.Val, e.KeyPos, e.ValPos)
	}
}
