package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// pyFill gives what Python's textwrap.fill gives, for the arguments
// Salt's highstate outputter passes it. The expected outputs in
// testdata/textwrap.json were made by Python's textwrap itself, not
// written from its documentation: hyphenated words, numeric ranges that
// do not break, an em-dash, long words with and without hyphens,
// whitespace of every kind, and non-ASCII letters. DIVERGENCE 5.274.
func TestFillMatchesPythonsTextwrap(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "textwrap.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Python    string
		Cases     []struct{ In, Out string }
		Generated []struct{ In, Out string }
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	pad := "              "
	for _, c := range fixture.Cases {
		if got := pyFill(c.In, 80, pad, pad); got != c.Out {
			t.Errorf("fill(%q):\n got %q\nwant %q (Python %s)", c.In, got, c.Out, fixture.Python)
		}
	}
	// And 400 generated from a fixed seed -- random mixes of hyphenated
	// words, numbers, dash runs, punctuation, long words and whitespace --
	// wrapped by Python too, because a hand-written splitter is checked
	// by the cases nobody thought to write.
	failed := 0
	for _, c := range fixture.Generated {
		if got := pyFill(c.In, 80, pad, pad); got != c.Out {
			if failed++; failed <= 5 {
				t.Errorf("fill(%q):\n got %q\nwant %q", c.In, got, c.Out)
			}
		}
	}
	if failed > 0 {
		t.Errorf("%d of %d generated cases differ from Python", failed, len(fixture.Generated))
	}
	if len(fixture.Cases) < 10 || len(fixture.Generated) < 400 {
		t.Fatalf("the fixture holds %d cases; it is meant to hold the corpus", len(fixture.Cases))
	}
}
