package pillar

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// `__replace__`, `__overwrite__` and `__aggregate__` are ordinary keys,
// because Salt has no such directives. SPEC 12.3 said Salt honoured
// them, and the merge consumed them: `__replace__: True` discarded what
// an earlier file had put under that key, and the key itself vanished.
// testdata/salt-merge-directives holds two pillar files and what Salt
// 3006.28 compiled from them under each strategy, captured with
// salt-call; neither 3006.28's nor 3008.3's source has the strings.
// DIVERGENCE 5.468.
//
// `aggregate` is captured but not compared. Salt's is yamlex
// aggregation, which merges the top level and, below it, only values
// tagged `!aggregate`; halite's deep merges and concatenates. That is a
// divergence of its own, recorded in the same entry and not changed.
func TestMergeDirectivesAreKeysAsInSalt(t *testing.T) {
	dir := filepath.Join("testdata", "salt-merge-directives")
	files := map[string]string{}
	for _, name := range []string{"top", "a", "b"} {
		src, err := os.ReadFile(filepath.Join(dir, name+".sls"))
		if err != nil {
			t.Fatal(err)
		}
		files["base|"+name] = string(src)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "salt-3006.28.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Captured string                    `json:"captured"`
		Results  map[string]map[string]any `json:"results"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}

	for name, cfg := range map[string]Config{
		"smart":                     {Strategy: value.Smart},
		"recurse":                   {Strategy: value.Recurse},
		"overwrite":                 {Strategy: value.Overwrite},
		"smart, pillar_merge_lists": {Strategy: value.Smart, MergeLists: true},
	} {
		t.Run(name, func(t *testing.T) {
			want, ok := fixture.Results[name]
			if !ok {
				t.Fatalf("the fixture has no %q result", name)
			}
			out := mustCompile(t, files, cfg)
			encoded, err := value.EncodeJSON(out.Pillar, 0)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			g, _ := json.Marshal(got)
			w, _ := json.Marshal(want)
			if string(g) != string(w) {
				t.Errorf("halite compiled\n  %s\nSalt compiled (%s)\n  %s", g, fixture.Captured, w)
			}
		})
	}
}
