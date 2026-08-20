package yaml

import (
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// SPEC section 31 requires continuous fuzzing of the parser and calls a
// panic a release blocker. A `.sls` file is not hostile input in the usual
// sense, but it is frequently machine-generated and frequently malformed,
// and a panic in the parser takes the whole node's run with it.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"key: value\n",
		"a:\n  b:\n    - 1\n    - 2\n",
		"anchors: &a {x: 1}\nuse: *a\n",
		"merge:\n  <<: *a\n  y: 2\n",
		"block: |\n  line one\n  line two\n",
		"folded: >-\n  wrapped\n  text\n",
		"flow: {a: 1, b: [2, 3]}\n",
		"quoted: \"a \\u00e9 b\"\n",
		"single: 'it''s'\n",
		"---\ndoc: 1\n---\ndoc: 2\n",
		"# only a comment\n",
		"tags: !!str 123\n",
		"empty:\n",
		"- 1\n- 2\n",
		"deep: " + strings.Repeat("[", 40) + strings.Repeat("]", 40) + "\n",
		"tabs:\n\tbad: 1\n",
		": novalue\n",
		"dup: 1\ndup: 2\n",
		"1.1: {yes: on, no: off}\n",
		"\x00\x01\x02",
		strings.Repeat("a: &x\n", 20),
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, src []byte) {
		// A parse either succeeds or reports an error. Neither outcome may
		// be a panic, and neither may hang: the node budget bounds alias
		// expansion, so an alias bomb has to come back as an error.
		v, warnings, err := Parse(src, Options{File: "fuzz.sls"})
		if err != nil {
			if v != nil {
				t.Errorf("a failed parse returned a value as well as %v", err)
			}
			return
		}
		for _, w := range warnings {
			if w.String() == "" {
				t.Error("a warning rendered as the empty string, which tells an operator nothing")
			}
		}
		// Whatever came back must be one of the nine types, since the rest
		// of halite type-switches on exactly those.
		assertNineTypes(t, v, 0)

		// Encoding it must not panic either, and the result must reparse.
		out := Encode(v, EncodeOptions{})
		again, _, err := Parse([]byte(out), Options{File: "reencoded.sls"})
		if err != nil {
			t.Errorf("the encoder produced YAML its own parser rejects: %v\n---\n%s\n---", err, out)
		}
		_ = again
	})
}

func FuzzParseStream(f *testing.F) {
	f.Add([]byte("---\na: 1\n---\nb: 2\n"))
	f.Add([]byte("---\n---\n---\n"))
	f.Add([]byte("a: 1\n...\n"))
	f.Fuzz(func(t *testing.T, src []byte) {
		docs, _, err := ParseStream(src, Options{File: "fuzz.sls"})
		if err != nil {
			return
		}
		for _, d := range docs {
			assertNineTypes(t, d, 0)
		}
	})
}

func FuzzEncodeScalar(f *testing.F) {
	for _, s := range []string{"", "plain", "with space", "yes", "1.0", "#hash", "a: b", "\n", "\x00", "*alias", "&anchor", "- item", "!!tag", "'quoted'", strings.Repeat("x", 200)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// A scalar must survive the encoder and come back as itself, or
		// an encoded pillar value silently changes meaning.
		out := EncodeScalar(s)
		v, _, err := Parse([]byte("k: "+out+"\n"), Options{File: "fuzz.sls"})
		if err != nil {
			t.Fatalf("EncodeScalar(%q) = %q, which does not parse: %v", s, out, err)
		}
		m, ok := v.(*value.Map)
		if !ok {
			t.Fatalf("expected a mapping, got %T", v)
		}
		got, _ := m.Get("k")
		if got != any(s) {
			t.Errorf("EncodeScalar(%q) = %q, which parsed back as %#v", s, out, got)
		}
	})
}

// assertNineTypes walks a parsed document and fails on anything outside
// the nine-type model of SPEC section 6.4. The rest of halite type-switches
// on exactly those, so a tenth type reaches a default branch somewhere and
// is silently rendered with %v.
func assertNineTypes(t *testing.T, v any, depth int) {
	t.Helper()
	if depth > 200 {
		t.Fatal("the parsed document is nested past any plausible depth; the depth bound did not hold")
	}
	switch x := v.(type) {
	case nil, bool, int64, float64, string, []byte, time.Time:
	case []any:
		for _, item := range x {
			assertNineTypes(t, item, depth+1)
		}
	case *value.Map:
		for _, e := range x.Entries() {
			switch e.Key.(type) {
			case nil, bool, int64, float64, string:
			default:
				t.Fatalf("a mapping key has type %T, which is not a key type", e.Key)
			}
			assertNineTypes(t, e.Val, depth+1)
		}
	default:
		t.Fatalf("the parser produced a %T, which is outside the nine-type model", v)
	}
}
