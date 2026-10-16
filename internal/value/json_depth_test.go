package value

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDecodeJSONDepthMatchesEncodingJSON holds DecodeJSON's nesting bound
// at the one json.Unmarshal enforces, from both sides and for both kinds
// of container. decodeValue recurses once per level and nothing else
// bounded it, so a few megabytes of `[` recursed until the Go stack
// overflowed, which is fatal rather than an error (DIVERGENCE 5.312).
// Measured under the pinned go1.26.6: the old code decoded 10,001 levels.
// The proof that the bound refuses nothing the standard decoder accepts
// is json.Valid agreeing at both edges.
func TestDecodeJSONDepthMatchesEncodingJSON(t *testing.T) {
	shapes := map[string]func(n int) string{
		"arrays":  func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) },
		"objects": func(n int) string { return strings.Repeat(`{"a":`, n) + "1" + strings.Repeat("}", n) },
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			at := []byte(shape(maxJSONDepth))
			if !json.Valid(at) {
				t.Fatalf("encoding/json refuses %d levels; the bound no longer matches it", maxJSONDepth)
			}
			if _, err := DecodeJSON(at); err != nil {
				t.Fatalf("at the limit (%d): %v", maxJSONDepth, err)
			}
			past := []byte(shape(maxJSONDepth + 1))
			if json.Valid(past) {
				t.Fatalf("encoding/json accepts %d levels; the bound no longer matches it", maxJSONDepth+1)
			}
			// Which error arrives depends on the toolchain. Under the
			// pinned go1.26.6, Decoder.Token keeps no depth and the old
			// code accepted this; from go1.27, encoding/json is built on
			// json/v2, whose Token refuses it first with "exceeded max
			// depth". Either is the refusal this test is for.
			if _, err := DecodeJSON(past); err == nil ||
				!(strings.Contains(err.Error(), "nests deeper than") || strings.Contains(err.Error(), "max depth")) {
				t.Fatalf("one past the limit: want a nesting error, got %v", err)
			}
		})
	}
}
