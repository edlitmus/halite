package yaml

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// SPEC 10.1 specifies this parser's dialect as the one PyYAML reads,
// because that is the dialect every existing Salt tree was written
// against. That claim has been argued from documentation; this checks it
// against PyYAML itself.
//
// The comparison is on the resolved *type* as much as the value: the two
// implementations agree on every character of `mode: 0644` and can still
// disagree on whether it is a string or the integer 420, which is the
// difference that writes the wrong permissions on a file.
//
// The test skips, loudly, where PyYAML is not installed. It must never
// pass quietly for that reason:
//
//	python3 -m venv /tmp/pyyaml && /tmp/pyyaml/bin/pip install PyYAML
//	HALITE_PYYAML_PYTHON=/tmp/pyyaml/bin/python go test ./internal/yaml/
//
// Set HALITE_YAML_DIFF_REGEN=1 to print the deviation table.

type diffCase struct {
	name string
	src  string
}

// diffDeviation records one document the two implementations read
// differently. As with the conformance suite, the table is enforced in
// both directions: an unrecorded disagreement fails, and so does a row
// whose disagreement has gone away.
type diffDeviation struct {
	name   string
	reason string
}

const (
	// The first four are deliberate: SPEC 10.1 asks for PyYAML's dialect
	// in general and names these as the exceptions.
	diffSpecDuplicate = "SPEC 10.1.2 makes a duplicate key an error; PyYAML takes the last silently"
	diffSpecTimestamp = "SPEC 10.1.3 keeps a date a string; a date becoming a struct breaks file.managed contents"
	diffSpecSexa      = "SPEC 10.1.3: a YAML 1.1 sexagesimal stays a string, with a lint warning where it would have differed"
	diffInt64         = "the value is outside int64, and Python's integers are unbounded"

	// The rest are gaps. Accepting a document PyYAML refuses is the
	// dangerous direction: a tree Salt would not load must not quietly
	// load here and mean something.
	diffGapAccepts = "gap: halite reads a document PyYAML refuses, rather than refusing it too"
)

var diffDeviations = []diffDeviation{
	{"int/190:20:30", diffSpecSexa},
	{"float/190:20:30.15", diffSpecSexa},
	{"ts/20:59:43", diffSpecSexa},

	{"ts/2001-12-14", diffSpecTimestamp},
	{"ts/2001-12-14t21:59:43.10-05:00", diffSpecTimestamp},
	{"ts/2001-12-14 21:59:43.10 -5", diffSpecTimestamp},
	{"ts/2001-12-14T21:59:43Z", diffSpecTimestamp},

	{"key/duplicate", diffSpecDuplicate},
	{"key/duplicate-nested", diffSpecDuplicate},

	{"int/12345678901234567890", diffInt64},

	// `k: -` becomes a one-element sequence, `k: ?` and `k: :` become a
	// mapping of null to null, and `a: b: c` becomes a nested mapping.
	// PyYAML refuses all four.
	{"str/-", diffGapAccepts},
	{"str/?", diffGapAccepts},
	{"str/:", diffGapAccepts},
	{"invalid/two-colons", diffGapAccepts},
}

// diffCorpus is built rather than written out, so that adding a spelling
// to Bool11Spellings puts it under the differential automatically.
func diffCorpus() []diffCase {
	var cases []diffCase
	add := func(name, src string) { cases = append(cases, diffCase{name, src}) }
	scalar := func(group string, values ...string) {
		for _, v := range values {
			add(group+"/"+v, "k: "+v+"\n")
		}
	}

	for _, s := range Bool11Spellings() {
		add("bool/"+s, "k: "+s+"\n")
	}
	scalar("int", "0", "-0", "+1", "3", "-3", "0644", "0o644", "0x1F", "0xff",
		"0b1010", "1_000", "685_230", "190:20:30", "12345678901234567890")
	scalar("float", "1.5", "-1.5", ".5", "1.", "1e3", "1E+3", "1e-3",
		"1.0e3", "1.0e+3", "-1.0e-3", "-2.5e3",
		".inf", "-.inf", ".nan", ".Inf", ".NaN", "1_000.5", "190:20:30.15")
	scalar("null", "~", "null", "Null", "NULL", "", "NULL_")
	scalar("str", "text", "'0644'", `"0644"`, "'yes'", "0644abc", "a b c",
		"a#b", "a:b", "-", "?", ":", "!!str 0644", "!!int '3'", "!!bool 'yes'")
	scalar("ts", "2001-12-14", "2001-12-14t21:59:43.10-05:00",
		"2001-12-14 21:59:43.10 -5", "2001-12-14T21:59:43Z", "20:59:43")
	scalar("binary", "!!binary aGk=")

	add("flow/seq", "k: [1, '2', three]\n")
	add("flow/map", "k: {a: 1, b: '2'}\n")
	add("flow/nested", "k: [{a: [1, 2]}, {b: null}]\n")
	add("flow/empty", "k: []\nj: {}\n")
	add("block/seq", "k:\n  - 1\n  - '2'\n  - three\n")
	add("block/map", "k:\n  a: 1\n  b: '2'\n")
	add("block/deep", "a:\n  b:\n    c:\n      - d: 1\n")
	add("scalar/literal", "k: |\n  one\n  two\n")
	add("scalar/literal-strip", "k: |-\n  one\n  two\n")
	add("scalar/literal-keep", "k: |+\n  one\n\n")
	add("scalar/folded", "k: >\n  one\n  two\n\n  three\n")
	add("scalar/folded-indented", "k: >\n  one\n   two\n  three\n")
	add("anchor/alias", "a: &x 1\nb: *x\n")
	add("anchor/collection", "a: &x {p: 1}\nb: *x\n")
	add("anchor/merge", "a: &x {p: 1, q: 2}\nb:\n  <<: *x\n  q: 3\n")
	add("anchor/merge-list", "a: &x {p: 1}\nc: &y {q: 2}\nb:\n  <<: [*x, *y]\n")
	add("key/int", "1: one\n2: two\n")
	add("key/bool", "yes: 1\nno: 2\n")
	add("key/null", "~: 1\n")
	add("key/quoted-number", "'1': one\n")
	add("key/duplicate", "a: 1\na: 2\n")
	add("key/duplicate-nested", "k:\n  a: 1\n  a: 2\n")
	add("comment/trailing", "k: 1 # comment\n")
	add("comment/only", "# nothing here\n")
	add("doc/explicit", "---\nk: 1\n")
	add("doc/explicit-end", "---\nk: 1\n...\n")
	add("doc/inline-marker", "--- 1\n")
	add("empty/document", "")
	add("empty/value", "k:\n")
	add("string/multiline-plain", "k: one\n  two\n")
	add("string/multiline-quoted", "k: \"one\n  two\"\n")
	add("string/escape", `k: "a\tb\nc\u00e9"`+"\n")
	add("string/single-escape", "k: 'it''s'\n")
	add("string/unicode", "k: café ☃\n")
	add("string/leading-zero-version", "k: 1.2.3\nj: 1.2\n")
	add("salt/mode-unquoted", "/etc/motd:\n  file.managed:\n    - mode: 0644\n")
	add("salt/mode-quoted", "/etc/motd:\n  file.managed:\n    - mode: '0644'\n")
	add("salt/enabled", "svc:\n  service.running:\n    - enable: True\n")

	// Documents PyYAML refuses. Refusing the same ones matters as much as
	// reading the same ones: a tree that Salt would not load must not
	// quietly load here.
	add("invalid/tab-indent", "k:\n\t- 1\n")
	add("invalid/unclosed-flow", "k: [1, 2\n")
	add("invalid/bad-anchor", "k: *missing\n")
	add("invalid/two-colons", "a: b: c\n")
	add("invalid/unclosed-quote", "k: 'abc\n")
	add("invalid/tag-unknown", "k: !!nosuch 1\n")

	return cases
}

// diffShape is the Go side of the shape grammar in pyyaml_shape.py. The
// two must spell identical readings identically.
func diffShape(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "bool:true"
		}
		return "bool:false"
	case int64:
		return "int:" + strconv.FormatInt(t, 10)
	case int:
		return "int:" + strconv.Itoa(t)
	case float64:
		switch {
		case math.IsNaN(t):
			return "float:nan"
		case math.IsInf(t, 1):
			return "float:+inf"
		case math.IsInf(t, -1):
			return "float:-inf"
		}
		return "float:" + strconv.FormatFloat(t, 'g', 17, 64)
	case string:
		return "str:" + t
	case []byte:
		return "bin:" + base64.StdEncoding.EncodeToString(t)
	case time.Time:
		return "ts:" + t.Format(time.RFC3339Nano)
	case []any:
		parts := make([]string, len(t))
		for i, x := range t {
			parts[i] = diffShape(x)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case *value.Map:
		parts := make([]string, 0, t.Len())
		for _, e := range t.Entries() {
			parts = append(parts, diffShape(e.Key)+"="+diffShape(e.Val))
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, ",") + "}"
	}
	return fmt.Sprintf("unknown:%T", v)
}

type pyResult struct {
	Shape string `json:"shape"`
	Err   string `json:"err"`
}

// pyyaml runs the corpus through PyYAML, or reports why it could not.
func pyyaml(t *testing.T, cases []diffCase) []pyResult {
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

	docs := make([]string, len(cases))
	for i, c := range cases {
		docs[i] = c.src
	}
	input, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("testdata", "differential", "pyyaml_shape.py")
	cmd := exec.Command(path, script)
	cmd.Stdin = strings.NewReader(string(input))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running %s: %v: %s", script, err, stderr.String())
	}
	var results []pyResult
	if err := json.Unmarshal(out, &results); err != nil {
		t.Fatalf("decoding the shaper's output: %v", err)
	}
	if len(results) != len(cases) {
		t.Fatalf("the shaper answered %d of %d documents", len(results), len(cases))
	}
	return results
}

func TestPyYAMLDifferential(t *testing.T) {
	cases := diffCorpus()
	theirs := pyyaml(t, cases)

	recorded := map[string]string{}
	for _, d := range diffDeviations {
		if _, seen := recorded[d.name]; seen {
			t.Errorf("%s has two deviation rows", d.name)
		}
		recorded[d.name] = d.reason
	}

	agreed, differed := 0, 0
	var unrecorded []string
	stale := map[string]bool{}
	for name := range recorded {
		stale[name] = true
	}

	for i, c := range cases {
		ours := ""
		v, _, err := Parse([]byte(c.src), DefaultOptions(c.name))
		if err != nil {
			ours = "error"
		} else {
			ours = diffShape(v)
		}
		them := theirs[i].Shape
		if theirs[i].Err != "" {
			them = "error"
		}

		if ours == them {
			agreed++
			if recorded[c.name] != "" {
				t.Errorf("%s has a deviation row and now agrees with PyYAML (%s); remove the row",
					c.name, ours)
			}
			continue
		}
		differed++
		delete(stale, c.name)
		if _, ok := recorded[c.name]; !ok {
			unrecorded = append(unrecorded, fmt.Sprintf("%s\n  document: %q\n  halite:   %s\n  PyYAML:   %s",
				c.name, c.src, ours, them))
		}
	}
	for name := range stale {
		t.Errorf("%s has a deviation row and did not differ", name)
	}
	for _, u := range unrecorded {
		t.Errorf("undocumented disagreement with PyYAML:\n%s", u)
	}

	t.Logf("PyYAML differential: %d of %d agree, %d differ", agreed, len(cases), differed)
	if os.Getenv("HALITE_YAML_DIFF_REGEN") != "" {
		regenDiffTable(t, cases, theirs)
	}
}

func regenDiffTable(t *testing.T, cases []diffCase, theirs []pyResult) {
	t.Helper()
	var b strings.Builder
	b.WriteString("var diffDeviations = []diffDeviation{\n")
	for i, c := range cases {
		ours := "error"
		if v, _, err := Parse([]byte(c.src), DefaultOptions(c.name)); err == nil {
			ours = diffShape(v)
		}
		them := theirs[i].Shape
		if theirs[i].Err != "" {
			them = "error"
		}
		if ours == them {
			continue
		}
		fmt.Fprintf(&b, "\t{%q, %s}, // halite %s, PyYAML %s\n", c.name, "diffGapValue", ours, them)
	}
	b.WriteString("}\n")
	t.Log("\n" + b.String())
}
