package fnmatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fnmatch is held to CPython's fnmatch.fnmatchcase, run over every pair
// in testdata/fnmatch_cpython.json. The answers were captured by running
// Python (testdata/fnmatch_cpython.py), not written from its
// documentation: the sets, the ranges that run backwards, the unclosed
// `[` and the literal backslash are exactly the places a reading of the
// documentation gets wrong. DIVERGENCE 5.295.
func TestFnmatchAgreesWithCPython(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fnmatch_cpython.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Captured string  `json:"captured"`
		Rows     [][]any `json:"rows"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Rows) < 1000 {
		t.Fatalf("the fixture holds %d rows; it was captured with 1584", len(fixture.Rows))
	}
	for _, row := range fixture.Rows {
		pattern, name, want := row[0].(string), row[1].(string), row[2].(bool)
		if got := Match(pattern, name); got != want {
			t.Errorf("fnmatch(%q, %q) = %v; CPython says %v (%s)", pattern, name, got, want, fixture.Captured)
		}
	}
}
