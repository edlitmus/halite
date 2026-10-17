package yaml

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **SingleQuote is PyYAML's single-quoted scalar** (issue 321, section
// 5). It only doubled the apostrophe, but a single-quoted scalar folds its
// line breaks, so "a\nb" written that way reads back as "a b": a PEM key or
// any multi-line secret through yaml_squote came out mangled, silently.
// PyYAML writes a run of breaks that starts with \n with one more \n in
// front, which is what folding takes away. Every case is PyYAML 6.0.1's
// output, from tools/make-yamlquote-corpus.py.
func TestSingleQuoteMatchesPyYAML(t *testing.T) {
	f, err := os.Open("testdata/squote.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n, bad := 0, 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var c struct{ In, Out string }
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		n++
		if got := SingleQuote(c.In); got != c.Out {
			bad++
			if bad <= 15 {
				t.Errorf("SingleQuote(%q) = %q, PyYAML writes %q", c.In, got, c.Out)
			}
		}
	}
	if n < 2000 {
		t.Fatalf("only %d cases were read", n)
	}
}

// And what it writes reads back as the string it was given, through this
// package's own parser, for the cases a tree meets.
func TestSingleQuoteRoundTrips(t *testing.T) {
	for _, s := range []string{"a\nb", "line1\nline2\n", "-----BEGIN KEY-----\nAAA\nBBB\n-----END KEY-----\n", "it's", "a\n\nb"} {
		doc := "k: " + SingleQuote(s) + "\n"
		v, _, err := Parse([]byte(doc), Options{})
		if err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		got, _ := v.(*value.Map).Get("k")
		if got != s {
			t.Errorf("%q read back as %q", s, got)
		}
	}
}
