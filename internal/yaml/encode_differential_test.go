package yaml

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The block encoder is held to PyYAML's `safe_dump`, for the same reason
// the parser is held to PyYAML's loader in differential_test.go: every
// existing Salt tree was written against that dialect, and a file this
// build writes should be the file Salt wrote.
//
// It had not been. This build indented a sequence under the key that owns
// it, where PyYAML puts it in the same column, and broke the line after a
// dash where PyYAML inlines the first element. Both are valid YAML and
// parse identically; both make every file `file.serialize` manages differ
// on the first run after a migration.
//
// The corpus is written as JSON so that one literal describes the value
// for both sides. It is the shapes that differ between the two styles --
// sequences in mappings, mappings in sequences, and the nestings of
// those -- rather than scalar spelling, which scalarsMatchPyYAML covers.
func TestBlockEncodingMatchesPyYAML(t *testing.T) {
	corpus := []string{
		`{"memusage": [{"percent": "75%"}], "status": [{"interval": 30}]}`,
		`[1, 2, 3]`,
		`[[1, 2], [3]]`,
		`{"a": {"b": {"c": 1}}}`,
		`[{"a": 1, "b": 2}, {"c": 3}]`,
		`{}`,
		`[]`,
		`{"a": {}, "b": []}`,
		`"hello"`,
		`"yes"`,
		`{"msg": "hello world"}`,
		`{"a": null, "b": true, "c": false}`,
		`[{"k": [1, 2]}]`,
		`{"engines": [{"slack": {"token": "abc", "control": true}}]}`,
		`{"top": [{"a": [{"b": 1}]}]}`,
		`{"beacons": {"memusage": [{"percent": "75%"}], "status": [{"interval": 30}]}}`,
		// The quoting boundary. PyYAML reaches for single quotes and this
		// build used to reach for double, so every quoted scalar in every
		// file differed. A string that needs no quoting takes none, even
		// when it holds an apostrophe.
		`"123"`,
		`"3.14"`,
		`"true"`,
		`"null"`,
		`"on"`,
		`""`,
		`"it's fine"`,
		`{"a": "yes", "b": "no", "c": "plain"}`,
		`["0644", "0o644", "1e3"]`,
	}

	want := pyyamlDump(t, corpus)
	for i, src := range corpus {
		var decoded any
		if err := json.Unmarshal([]byte(src), &decoded); err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		got := strings.TrimRight(Encode(toValue(decoded), EncodeOptions{}), "\n")
		if got != want[i] {
			t.Errorf("%s\n  halite:\n%s\n  PyYAML:\n%s",
				src, indentForDiff(got), indentForDiff(want[i]))
		}
	}
}

// toValue turns what encoding/json produced into the value types this
// package encodes, so the corpus can be written as plain JSON.
func toValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		// JSON objects are unordered in Go's decoder, and PyYAML sorts
		// keys by default, so sorting here makes the two agree on order
		// without the corpus having to state it.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortStrings(keys)
		m := value.NewMap(len(keys))
		for _, k := range keys {
			m.Set(k, toValue(t[k]))
		}
		return m
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = toValue(e)
		}
		return out
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	}
	return v
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func indentForDiff(s string) string {
	return "    " + strings.ReplaceAll(s, "\n", "\n    ")
}

// pyyamlDump runs the corpus through PyYAML's safe_dump, or says why it
// could not. It skips loudly rather than passing quietly, as the parser
// differential does.
func pyyamlDump(t *testing.T, corpus []string) []string {
	t.Helper()
	python := os.Getenv("HALITE_PYYAML_PYTHON")
	if python == "" {
		python = "python3"
	}
	path, err := exec.LookPath(python)
	if err != nil {
		t.Skipf("PyYAML differential skipped: %s not found. "+
			"Set HALITE_PYYAML_PYTHON to an interpreter with PyYAML installed.", python)
	}
	if out, err := exec.Command(path, "-c", "import yaml").CombinedOutput(); err != nil {
		t.Skipf("PyYAML differential skipped: %s has no PyYAML (%s). "+
			"Set HALITE_PYYAML_PYTHON to an interpreter that does.", path, strings.TrimSpace(string(out)))
	}

	values := make([]any, len(corpus))
	for i, src := range corpus {
		if err := json.Unmarshal([]byte(src), &values[i]); err != nil {
			t.Fatalf("%s: %v", src, err)
		}
	}
	input, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("testdata", "differential", "pyyaml_dump.py")
	cmd := exec.Command(path, script)
	cmd.Stdin = strings.NewReader(string(input))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running %s: %v: %s", script, err, stderr.String())
	}
	var results []string
	if err := json.Unmarshal(out, &results); err != nil {
		t.Fatalf("decoding %s output: %v", script, err)
	}
	if len(results) != len(corpus) {
		t.Fatalf("%s returned %d results for %d cases", script, len(results), len(corpus))
	}
	return results
}
