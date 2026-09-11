package rendersandbox

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

// describeValue renders a parsed value as its types and its contents, so
// that a comparison fails on `0644` becoming the integer 420 as loudly
// as it fails on the characters changing.
func describeValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		return fmt.Sprintf("bool:%t", t)
	case int64:
		return fmt.Sprintf("int:%d", t)
	case float64:
		return "float:" + formatFloat(t)
	case string:
		return fmt.Sprintf("str:%q", t)
	case []byte:
		return fmt.Sprintf("bin:%x", t)
	case time.Time:
		return "ts:" + t.Format(time.RFC3339Nano)
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = describeValue(item)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case *value.Map:
		parts := make([]string, 0, t.Len())
		for _, e := range t.Entries() {
			parts = append(parts, describeValue(e.Key)+"="+describeValue(e.Val))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return fmt.Sprintf("unknown:%T", v)
}

// roundTrip encodes and decodes a value the way the boundary does.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	f := newFiles()
	node, err := encodeValue(v, f)
	if err != nil {
		t.Fatalf("encoding %T: %v", v, err)
	}
	// Through JSON as well, because that is what actually crosses: a
	// value that survives the structs and not the marshalling would pass
	// a lazier test and fail in production.
	body, err := json.Marshal(struct {
		Files []string `json:"files"`
		Node  wireNode `json:"node"`
	}{f.names, node})
	if err != nil {
		t.Fatalf("marshalling %T: %v", v, err)
	}
	var back struct {
		Files []string `json:"files"`
		Node  wireNode `json:"node"`
	}
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	out, err := decodeValue(back.Node, restoreFiles(back.Files))
	if err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return out
}

func TestEveryTypeInTheModelSurvivesTheBoundary(t *testing.T) {
	stamp := time.Date(2026, 9, 11, 17, 4, 5, 123456789, time.FixedZone("", -5*3600))

	cases := []any{
		nil,
		true, false,
		int64(0), int64(-1), int64(math.MaxInt64), int64(math.MinInt64),
		// The three JSON has no room for, which is why numbers travel as
		// text.
		math.NaN(), math.Inf(1), math.Inf(-1),
		1.5, math.MaxFloat64, math.SmallestNonzeroFloat64,
		"", "plain", "café ☃", strings.Repeat("x", 1000),
		// Not valid UTF-8. encoding/json replaces these bytes with
		// U+FFFD, and `cmd.run` output reaches a template.
		string([]byte{0xff, 0xfe, 0x00, 0x41}),
		[]byte{0, 1, 2, 255},
		stamp,
		[]any{int64(1), "two", nil, []any{true}},
	}
	for _, c := range cases {
		got := roundTrip(t, c)
		if describeValue(got) != describeValue(c) {
			t.Errorf("%s came back as %s", describeValue(c), describeValue(got))
		}
	}

	// NaN is not equal to itself, so it is checked by classification
	// rather than by comparison.
	if f, ok := roundTrip(t, math.NaN()).(float64); !ok || !math.IsNaN(f) {
		t.Errorf("NaN came back as %#v", f)
	}
}

func TestAMappingKeepsItsOrderKeysAndPositions(t *testing.T) {
	src := []byte("zebra: 1\napple: 2\nnested:\n  inner: true\n  list:\n    - a: 1\n1: an integer key\ntrue: a boolean key\n")
	parsed, _, err := yaml.Parse(src, yaml.DefaultOptions("order.sls"))
	if err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}

	got := roundTrip(t, parsed)
	if describeValue(got) != describeValue(parsed) {
		t.Fatalf("the tree changed:\n  got  %s\n  want %s", describeValue(got), describeValue(parsed))
	}

	want := positionsOf(t, parsed)
	have := positionsOf(t, got)
	if len(want) == 0 {
		t.Fatal("the fixture carries no positions, so this test checks nothing")
	}
	if strings.Join(have, "\n") != strings.Join(want, "\n") {
		t.Errorf("positions changed:\n  got\n%s\n  want\n%s", strings.Join(have, "\n"), strings.Join(want, "\n"))
	}
}

// The file table is the one part of the encoding that is shared state
// between nodes, so a tree naming several files is worth its own case.
func TestPositionsFromSeveralFilesAllComeBack(t *testing.T) {
	m := value.NewMap(2)
	m.Pos = value.Pos{File: "a.sls", Line: 1, Col: 1}
	m.SetAt("from_a", int64(1), value.Pos{File: "a.sls", Line: 2, Col: 3}, value.Pos{File: "a.sls", Line: 2, Col: 9})
	m.SetAt("from_b", int64(2), value.Pos{File: "b.sls", Line: 7, Col: 1}, value.Pos{File: "c.sls", Line: 8, Col: 2})

	got, ok := roundTrip(t, m).(*value.Map)
	if !ok {
		t.Fatalf("a mapping came back as %T", got)
	}
	e, _ := got.Entry("from_b")
	if e.KeyPos.File != "b.sls" || e.ValPos.File != "c.sls" {
		t.Errorf("the positions came back as key %s and value %s", e.KeyPos, e.ValPos)
	}
	if got.Pos.File != "a.sls" {
		t.Errorf("the mapping's own position came back as %s", got.Pos)
	}
}

func TestATypeOutsideTheModelIsRefusedRatherThanMangled(t *testing.T) {
	type notInTheModel struct{ A int }
	if _, err := encodeValue(notInTheModel{1}, newFiles()); err == nil {
		t.Fatal("a struct was accepted by the codec")
	}
	if _, err := encodeValue(map[string]any{"a": 1}, newFiles()); err == nil {
		t.Fatal("a Go map was accepted; parsed data is always a *value.Map and a plain map is a caller's mistake")
	}
}

func TestADamagedFileTableIsRefused(t *testing.T) {
	node := wireNode{K: kindMap, P: &wirePos{F: 3, L: 1, C: 1}}
	if _, err := decodeValue(node, restoreFiles([]string{"only.sls"})); err == nil {
		t.Fatal("a position naming a file outside the table was accepted")
	}
	if _, err := decodeValue(wireNode{K: "?"}, newFiles()); err == nil {
		t.Fatal("an unknown kind tag was accepted")
	}
}

// FuzzCodec holds the round trip to being exact over whatever the parser
// produces. The corpus is documents rather than values, so the fuzzer
// explores the shapes a real tree can take rather than shapes only this
// encoder can.
func FuzzCodec(f *testing.F) {
	for _, seed := range []string{
		"a: 1\n",
		"a: !!binary aGk=\n",
		"when: 2001-12-14T21:59:43Z\n",
		"big: 12345678901234567\nneg: -0.5\ninf: .inf\nnan: .nan\n",
		"nested:\n  - one: 1\n  - two: [1, 2]\n",
		"text: |\n  keep\n  the lines\n",
		"1: integer key\nyes: bool key\n~: null key\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, doc string) {
		parsed, _, err := yaml.Parse([]byte(doc), yaml.DefaultOptions("fuzz.sls"))
		if err != nil {
			return
		}
		files := newFiles()
		node, err := encodeValue(parsed, files)
		if err != nil {
			t.Fatalf("the parser produced %s and the codec refused it: %v", describeValue(parsed), err)
		}
		body, err := json.Marshal(node)
		if err != nil {
			t.Fatalf("the encoded form does not marshal: %v", err)
		}
		var back wireNode
		if err := json.Unmarshal(body, &back); err != nil {
			t.Fatalf("the encoded form does not unmarshal: %v", err)
		}
		out, err := decodeValue(back, restoreFiles(files.names))
		if err != nil {
			t.Fatalf("decoding what was just encoded: %v", err)
		}
		if got, want := describeValue(out), describeValue(parsed); got != want {
			t.Fatalf("the value changed across the boundary:\n  got  %s\n  want %s", got, want)
		}
		if got, want := positionsIn(out), positionsIn(parsed); !sameStrings(got, want) {
			t.Fatalf("positions changed:\n  got  %v\n  want %v", got, want)
		}
	})
}

// positionsIn is the fuzz target's own position walk, sorted, since the
// fuzzer has no *testing.T to hand to the helper the tests use.
func positionsIn(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case *value.Map:
			out = append(out, "m"+n.Pos.String())
			for _, e := range n.Entries() {
				out = append(out, "k"+e.KeyPos.String(), "v"+e.ValPos.String())
				walk(e.Val)
			}
		case []any:
			for _, item := range n {
				walk(item)
			}
		}
	}
	walk(v)
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
