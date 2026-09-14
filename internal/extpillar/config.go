package extpillar

import (
	"fmt"

	"github.com/edlitmus/halite/internal/value"
)

// Spec is one entry of `ext_pillar`.
type Spec struct {
	// Name is the source, which is the extension's name.
	Name string
	// Config is the block under it, handed to the extension untouched.
	Config any
	// Ignore is this source's effective `ext_pillar_fail`.
	Ignore bool
}

// ParseList reads the `ext_pillar` setting.
//
// Salt's shape, kept: a list of single-key mappings, the key naming the
// source and the value being that source's own configuration. An
// existing Salt configuration is recognisable, and the hub does not
// interpret the value — it belongs to the extension, which is the only
// thing that knows its schema.
//
// The one key the hub does read is `fail`, which SPEC 12.7 makes a
// per-source setting. A source block that is a mapping may carry it
// alongside its own settings; a source block that is a list — which is
// how Salt writes most of them — carries it as a `- fail: ignore` entry
// among the others. Both are stripped before the block is handed over,
// so an extension never sees a setting that was not meant for it.
func ParseList(raw any, defaultIgnore bool) ([]Spec, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("`ext_pillar` is a list of sources, not %s", value.TypeName(raw))
	}
	var out []Spec
	for _, item := range list {
		m, ok := item.(*value.Map)
		if !ok {
			return nil, fmt.Errorf(
				"`ext_pillar`: an entry is a mapping naming one source, not %s; "+
					"a source that takes no configuration is written as `- my_source: {}`",
				value.TypeName(item))
		}
		if m.Len() == 0 {
			return nil, fmt.Errorf("`ext_pillar`: an entry names no source")
		}
		for _, e := range m.Entries() {
			name := value.KeyString(e.Key)
			if name == "" {
				return nil, fmt.Errorf("`ext_pillar`: an entry names no source")
			}
			block, ignore, err := splitFail(name, e.Val, defaultIgnore)
			if err != nil {
				return nil, err
			}
			out = append(out, Spec{Name: name, Config: block, Ignore: ignore})
		}
	}
	return out, nil
}

// splitFail takes `fail` out of a source's block and reports it.
func splitFail(source string, block any, ignore bool) (any, bool, error) {
	switch t := block.(type) {
	case *value.Map:
		v, ok := t.Get("fail")
		if !ok {
			return block, ignore, nil
		}
		parsed, err := failValue(source, v)
		if err != nil {
			return nil, false, err
		}
		rest := value.NewMap(t.Len())
		for _, e := range t.Entries() {
			if value.KeyString(e.Key) == "fail" {
				continue
			}
			rest.Set(e.Key, e.Val)
		}
		return rest, parsed, nil

	case []any:
		var rest []any
		for _, item := range t {
			entry, ok := item.(*value.Map)
			if ok && entry.Len() == 1 {
				if v, found := entry.Get("fail"); found {
					parsed, err := failValue(source, v)
					if err != nil {
						return nil, false, err
					}
					ignore = parsed
					continue
				}
			}
			rest = append(rest, item)
		}
		return rest, ignore, nil
	}
	return block, ignore, nil
}

func failValue(source string, v any) (bool, error) {
	switch value.KeyString(v) {
	case "ignore":
		return true, nil
	case "hard":
		return false, nil
	}
	return false, fmt.Errorf("`ext_pillar`: %s: `fail` is hard or ignore, not %q",
		source, value.KeyString(v))
}
