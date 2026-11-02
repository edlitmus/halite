package extension

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A pin that is misspelt, or that pins nothing, is refused by name: a
// `rot:` for `root:` pinned the version and silently not the digest.
// DIVERGENCE 5.555.
func TestAMisspeltOrEmptyPinIsRefused(t *testing.T) {
	good, err := ParsePins(value.MapOf(
		"a", "1.0.0",
		"b", value.MapOf("version", "2.0.0", "root", "abc123"),
		"c", value.MapOf("root", "def456"),
	))
	if err != nil {
		t.Fatal(err)
	}
	if good["a"] != (Pin{Version: "1.0.0"}) || good["b"] != (Pin{Version: "2.0.0", Root: "abc123"}) ||
		good["c"] != (Pin{Root: "def456"}) {
		t.Errorf("pins read as %+v", good)
	}
	if none, err := ParsePins(nil); err != nil || none != nil {
		t.Errorf("no pins: %v, %v", none, err)
	}

	for name, raw := range map[string]struct {
		pins any
		want string
	}{
		"a misspelt root":      {value.MapOf("x", value.MapOf("version", "1.0.0", "rot", "abc")), `"rot"`},
		"a misspelt version":   {value.MapOf("x", value.MapOf("vesion", "1.0.0", "root", "abc")), `"vesion"`},
		"an empty mapping":     {value.MapOf("x", value.NewMap(0)), "pins nothing"},
		"an empty version":     {value.MapOf("x", ""), "pins nothing"},
		"a list for a pin":     {value.MapOf("x", []any{"1.0.0"}), "a pin is a version or a mapping"},
		"pins that are a list": {[]any{"x"}, "is a mapping"},
	} {
		_, err := ParsePins(raw.pins)
		if err == nil || !strings.Contains(err.Error(), raw.want) {
			t.Errorf("%s: %v, want an error naming %s", name, err, raw.want)
		}
	}
}
