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
	// Plain is `secret: false`: what this source returns is ordinary
	// data, to be printed like any other pillar value rather than masked.
	// Inverted so that the zero value is the safe one: a Spec written
	// anywhere without it -- a test, a future caller -- is secret.
	Plain bool
}

// AlwaysSecret are the sources that may not be configured `secret: false`.
// What they return is credentials by construction, so a configuration
// saying otherwise is a mistake that would print them, and the hub
// refuses it rather than obeying. The name is safe to key on: a source is
// a signed extension pinned by name, so `aws_secrets_manager` is the
// Secrets Manager fetcher and not something else that took its name.
var AlwaysSecret = map[string]bool{
	"aws_secrets_manager": true,
}

// ParseList reads the `ext_pillar` setting.
//
// Salt's shape, kept: a list of single-key mappings, the key naming the
// source and the value being that source's own configuration. An
// existing Salt configuration is recognisable, and the hub does not
// interpret the value — it belongs to the extension, which is the only
// thing that knows its schema.
//
// The two keys the hub does read are `fail`, which SPEC 12.7 makes a
// per-source setting, and `secret`, which says whether what the source
// returns is masked in output (DIVERGENCE 5.257). A source block that is
// a mapping may carry them alongside its own settings; a source block
// that is a list — which is how Salt writes most of them — carries each
// as a `- fail: ignore` entry among the others. Both forms are stripped
// before the block is handed over, so an extension never sees a setting
// that was not meant for it.
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
			block, settings, err := splitSettings(name, e.Val)
			if err != nil {
				return nil, err
			}
			spec := Spec{Name: name, Config: block, Ignore: defaultIgnore}
			if v, ok := settings["fail"]; ok {
				if spec.Ignore, err = failValue(name, v); err != nil {
					return nil, err
				}
			}
			if v, ok := settings["secret"]; ok {
				secret, err := secretValue(name, v)
				if err != nil {
					return nil, err
				}
				spec.Plain = !secret
			}
			if spec.Plain && AlwaysSecret[name] {
				return nil, fmt.Errorf("`ext_pillar`: %s: `secret: false` is refused for this source; "+
					"everything it returns is a credential, and marking it plain would print them "+
					"in every state comment and `pillar items` that shows one", name)
			}
			out = append(out, spec)
		}
	}
	return out, nil
}

// hostKeys are the keys of a source's block the hub reads itself and
// does not hand to the extension.
var hostKeys = map[string]bool{"fail": true, "secret": true}

// splitSettings takes the hub's own keys out of a source's block and
// reports them, from either shape a block may have.
func splitSettings(source string, block any) (any, map[string]any, error) {
	found := map[string]any{}
	switch t := block.(type) {
	case *value.Map:
		rest := value.NewMap(t.Len())
		for _, e := range t.Entries() {
			if k := value.KeyString(e.Key); hostKeys[k] {
				found[k] = e.Val
				continue
			}
			rest.Set(e.Key, e.Val)
		}
		if len(found) == 0 {
			return block, found, nil
		}
		return rest, found, nil

	case []any:
		var rest []any
		for _, item := range t {
			if entry, ok := item.(*value.Map); ok && entry.Len() == 1 {
				k := value.KeyString(entry.Entries()[0].Key)
				if hostKeys[k] {
					found[k] = entry.Entries()[0].Val
					continue
				}
			}
			rest = append(rest, item)
		}
		return rest, found, nil
	}
	return block, found, nil
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

func secretValue(source string, v any) (bool, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return false, fmt.Errorf("`ext_pillar`: %s: `secret` is true or false, not %q",
		source, value.KeyString(v))
}
