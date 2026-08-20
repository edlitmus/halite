package yaml

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// SPEC section 31 names "the YAML parser never panics" as a property. The
// fuzzer in this package is the thorough check; this is the one that runs
// on every `go test`, so a regression is caught by the ordinary suite
// rather than waiting for someone to run a fuzz campaign.

// yamlAtoms are the fragments a generated document is assembled from,
// chosen for the constructs that carry parser state across lines:
// indentation, anchors, block scalars, flow nesting, and document markers.
var yamlAtoms = []string{
	"a:", " b:", "  c:", "\t", "- ", "  - ", "---", "...", "&a", "*a",
	"<<:", "|", ">", "|-", ">+", "|2", "#c", " #c", "{", "}", "[", "]",
	",", ":", ": ", "'", "\"", "\\", "!!str", "!!binary", "!!timestamp",
	"?", "yes", "null", "~", "0x1f", "1.2e3", ".inf", ".nan", "2020-01-01",
	"\n", "\n\n", "   ", "%YAML 1.1", "@", "`", "\x00", "\x7f", "é", " ",
}

func generateYAML(rnd *rand.Rand) []byte {
	var b strings.Builder
	for n := rnd.Intn(40); n > 0; n-- {
		b.WriteString(yamlAtoms[rnd.Intn(len(yamlAtoms))])
	}
	return []byte(b.String())
}

func TestParserNeverPanics(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		rnd := rand.New(rand.NewSource(37))
		for i := 0; i < 50000; i++ {
			src := generateYAML(rnd)
			// A panic here fails the test by crashing it, which is the
			// outcome the property forbids. Everything else is fine: an
			// error is a correct answer to malformed input.
			v, _, err := Parse(src, Options{File: "generated.sls"})
			if err == nil {
				// Whatever parsed must also encode, since a value that
				// cannot be rendered cannot be reported or cached.
				Encode(v, EncodeOptions{})
				Encode(v, EncodeOptions{Flow: true})
			}
			ParseStream(src, Options{File: "generated.sls"})
		}
	}()
	select {
	case <-done:
	case <-time.After(120 * time.Second):
		t.Fatal("the parser did not terminate on generated input")
	}
}

// The node budget is what stands between an alias bomb and the node's
// memory. The classic billion-laughs shape must come back as an error
// rather than as a very large value.
func TestAnAliasBombIsRefused(t *testing.T) {
	var b strings.Builder
	b.WriteString("a: &a [x, x, x, x, x, x, x, x, x]\n")
	for i := 0; i < 9; i++ {
		prev := string(rune('a' + i))
		next := string(rune('a' + i + 1))
		b.WriteString(next + ": &" + next + " [")
		for j := 0; j < 9; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString("*" + prev)
		}
		b.WriteString("]\n")
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := Parse([]byte(b.String()), Options{File: "bomb.sls"})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("an alias bomb parsed successfully; the node budget did not hold")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("an alias bomb did not terminate")
	}
}

// Deep nesting must hit a bound rather than the goroutine stack.
func TestDeepNestingIsBounded(t *testing.T) {
	for _, src := range []string{
		strings.Repeat("[", 100000) + strings.Repeat("]", 100000),
		strings.Repeat("{a: ", 100000) + "1" + strings.Repeat("}", 100000),
		strings.Repeat("- ", 100000) + "x",
	} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			Parse([]byte(src), Options{File: "deep.sls"})
		}()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("deeply nested input did not terminate")
		}
	}
}
