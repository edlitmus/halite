package template

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// Salt's `yaml` filter is safe_dump, and a tree reaches for it to put a
// pillar mapping into a configuration file:
//
//	beacons:
//	  {{ beacons | yaml(False) | indent(2) }}
//
// The estate this was found on writes exactly that, twice, and both files
// failed with `unknown filter "yaml"` once the tree compiled far enough
// to render them.
//
// The expectations are Salt's own output, captured from the Salt on this
// project's reference host.
func TestYamlFilterMatchesSalt(t *testing.T) {
	beacons := value.MapOf(
		"memusage", []any{value.MapOf("percent", "75%")},
		"status", []any{value.MapOf("interval", int64(30))},
	)

	for _, tc := range []struct {
		name string
		src  string
		vars map[string]any
		want string
	}{
		{
			name: "flow by default, which is Salt's default",
			src:  `{{ d | yaml }}`,
			vars: map[string]any{"d": beacons},
			want: `{memusage: [{percent: 75%}], status: [{interval: 30}]}`,
		},
		{
			name: "block when the tree passes false",
			src:  `{{ d | yaml(False) }}`,
			vars: map[string]any{"d": beacons},
			want: "memusage:\n- percent: 75%\nstatus:\n- interval: 30",
		},
		{
			name: "the argument may be named, as Salt names it",
			src:  `{{ d | yaml(flow_style=False) }}`,
			vars: map[string]any{"d": beacons},
			want: "memusage:\n- percent: 75%\nstatus:\n- interval: 30",
		},
		{
			name: "a scalar carries no document markers",
			src:  `{{ 'hello' | yaml }}`,
			want: `hello`,
		},
		{
			name: "a list in block style",
			src:  `{{ [1, 2] | yaml(False) }}`,
			want: "- 1\n- 2",
		},
		{
			name: "and the whole line the estate writes",
			src:  "beacons:\n  {{ d | yaml(False) | indent(2) }}",
			vars: map[string]any{"d": beacons},
			want: "beacons:\n  memusage:\n  - percent: 75%\n  status:\n  - interval: 30",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := render(t, tc.src, tc.vars)
			if got != tc.want {
				t.Errorf("%s\n  got:  %q\n  want: %q", tc.src, got, tc.want)
			}
		})
	}
}

// The output is stripped, because the filter's result is going into the
// middle of a line. A trailing newline here puts a blank line in the
// file and, with `indent`, misaligns everything after it.
func TestYamlFilterStripsItsOutput(t *testing.T) {
	got := render(t, `{{ d | yaml(False) }}`, map[string]any{
		"d": value.MapOf("a", int64(1)),
	})
	if strings.HasSuffix(got, "\n") {
		t.Errorf("the filter left a trailing newline: %q", got)
	}
}
