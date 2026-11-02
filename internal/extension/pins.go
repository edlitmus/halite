package extension

import (
	"fmt"

	"github.com/edlitmus/halite/internal/value"
)

// ParsePins reads `extension_pins`: a mapping of extension name to a version
// (a string) or to a mapping of `version` and `root`.
//
// A key that is neither is refused by name, and so is a pin that pins
// nothing. The two programs each read this with a loop that ignored a key it
// did not know, so `{version: 1.0.0, rot: <digest>}` pinned the version and
// silently not the root, which is the integrity pin (the version is a label
// the publisher controls, 5.333), and `{}` pinned nothing at all: a typo in
// the one setting that exists to make a swapped extension fail. `gitfs_remotes`
// and `s3_buckets` already refuse a misspelt key, for the same reason.
// DIVERGENCE 5.555.
func ParsePins(raw any) (map[string]Pin, error) {
	if raw == nil {
		return nil, nil
	}
	m, ok := raw.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("`extension_pins` is a mapping of extension to version and root, not %s",
			value.TypeName(raw))
	}
	out := map[string]Pin{}
	for _, e := range m.Entries() {
		name := value.KeyString(e.Key)
		switch v := e.Val.(type) {
		case string:
			if v == "" {
				return nil, fmt.Errorf("`extension_pins`: %s pins to an empty version, which pins nothing", name)
			}
			out[name] = Pin{Version: v}
		case *value.Map:
			pin := Pin{}
			for _, entry := range v.Entries() {
				switch key := value.KeyString(entry.Key); key {
				case "version":
					pin.Version = value.KeyString(entry.Val)
				case "root":
					pin.Root = value.KeyString(entry.Val)
				default:
					return nil, fmt.Errorf("`extension_pins`: %s has %q, which is not a pin setting; "+
						"use version and root", name, key)
				}
			}
			if pin.Version == "" && pin.Root == "" {
				return nil, fmt.Errorf("`extension_pins`: %s names neither a version nor a root, which pins nothing", name)
			}
			out[name] = pin
		default:
			return nil, fmt.Errorf("`extension_pins`: %s pins to %s, and a pin is a version or a mapping "+
				"of version and root", name, value.TypeName(e.Val))
		}
	}
	return out, nil
}
