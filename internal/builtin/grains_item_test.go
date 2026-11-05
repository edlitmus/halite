package builtin

import (
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// `grains.item` takes any number of grain names, as Salt's does
// (`grains.item(*args)`): `halite-hub run '*' grains.item os os_family`
// is the command docs/operations.md gives for checking an estate's FIPS
// state. It was declared with one List parameter, so the second name
// was a second positional argument and the call failed on every node
// with "takes at most 1 positional arguments, got 3". Found running the
// documented command against an arm64 node in kernel FIPS mode for
// issue 306.
func TestGrainsItemTakesSeveralNames(t *testing.T) {
	r := New()
	cases := []struct {
		name   string
		args   []any
		kwargs *value.Map
		want   []string
	}{
		{"one name", []any{"os"}, nil, []string{"os"}},
		{"several names", []any{"os", "os_family", "haliteversion"}, nil,
			[]string{"os", "os_family", "haliteversion"}},
		// A list passed as one argument, which is what --legacy-arg-parse
		// makes of `'[os, os_family]'` and what a state's YAML passes.
		{"a list as one argument", []any{[]any{"os", "os_family"}}, nil,
			[]string{"os", "os_family"}},
		{"by keyword", nil, value.MapOf("names", []any{"os", "os_family"}),
			[]string{"os", "os_family"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := r.Exec.CallPositional(newCtx(false), "grains.item", tc.args, tc.kwargs)
			if err != nil {
				t.Fatalf("grains.item: %v", err)
			}
			m, ok := out.(*value.Map)
			if !ok {
				t.Fatalf("returned %T, want a mapping", out)
			}
			if m.Len() != len(tc.want) {
				t.Errorf("returned %d grains, want %d: %v", m.Len(), len(tc.want), m)
			}
			for _, name := range tc.want {
				if v, ok := m.Get(name); !ok || v == nil {
					t.Errorf("%s missing from %v", name, m)
				}
			}
		})
	}
}
