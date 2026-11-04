package value

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestDecodeJSONDepthMatchesEncodingJSON holds DecodeJSON's nesting bound
// at the one json.Unmarshal enforces, from both sides and for both kinds
// of container. decodeValue recurses once per level and nothing else
// bounded it, so a few megabytes of `[` recursed until the Go stack
// overflowed, which is fatal rather than an error (DIVERGENCE 5.312).
// Measured under the then-pinned go1.26.6: the old code decoded 10,001
// levels. The proof that the bound refuses nothing the standard decoder
// accepts is json.Valid agreeing at both edges.
//
// The bound itself is exercised through tokenSource, not DecodeJSON.
// From go1.27 encoding/json is json/v2 underneath, and its Decoder.Token
// refuses the 10,001st level with "exceeded max depth" before
// decodeValue sees it, so a test that went through DecodeJSON alone
// passed with the bound deleted: measured under go1.27.3 by changing the
// check to `if false && …`. unboundedTokens keeps no depth, which is what
// the go1.26 Decoder was, so only decodeValue's own check can refuse.
func TestDecodeJSONDepthMatchesEncodingJSON(t *testing.T) {
	shapes := map[string]struct {
		text   func(n int) string
		tokens func(n int) []json.Token
	}{
		"arrays": {
			text: func(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) },
			tokens: func(n int) []json.Token {
				out := make([]json.Token, 0, 2*n)
				for range n {
					out = append(out, json.Delim('['))
				}
				for range n {
					out = append(out, json.Delim(']'))
				}
				return out
			},
		},
		"objects": {
			text: func(n int) string { return strings.Repeat(`{"a":`, n) + "1" + strings.Repeat("}", n) },
			tokens: func(n int) []json.Token {
				out := make([]json.Token, 0, 3*n+1)
				for range n {
					out = append(out, json.Delim('{'), "a")
				}
				out = append(out, json.Number("1"))
				for range n {
					out = append(out, json.Delim('}'))
				}
				return out
			},
		},
	}
	for name, shape := range shapes {
		t.Run(name, func(t *testing.T) {
			if !json.Valid([]byte(shape.text(maxJSONDepth))) {
				t.Fatalf("encoding/json refuses %d levels; the bound no longer matches it", maxJSONDepth)
			}
			if json.Valid([]byte(shape.text(maxJSONDepth + 1))) {
				t.Fatalf("encoding/json accepts %d levels; the bound no longer matches it", maxJSONDepth+1)
			}

			if _, err := decodeValue(&unboundedTokens{toks: shape.tokens(maxJSONDepth)}, 0); err != nil {
				t.Fatalf("at the limit (%d): %v", maxJSONDepth, err)
			}
			_, err := decodeValue(&unboundedTokens{toks: shape.tokens(maxJSONDepth + 1)}, 0)
			if err == nil || !strings.Contains(err.Error(), "nests deeper than") {
				t.Fatalf("one past the limit: want decodeValue's own nesting error, got %v", err)
			}

			// End to end the document must still be refused. Which check
			// does it depends on the toolchain, as above; either is a
			// refusal, and the subtest above is what holds halite's.
			if _, err := DecodeJSON([]byte(shape.text(maxJSONDepth))); err != nil {
				t.Fatalf("DecodeJSON at the limit (%d): %v", maxJSONDepth, err)
			}
			if _, err := DecodeJSON([]byte(shape.text(maxJSONDepth + 1))); err == nil {
				t.Fatalf("DecodeJSON accepted %d levels", maxJSONDepth+1)
			}
		})
	}
}

// unboundedTokens replays a token list with no depth limit of its own, as
// the go1.26 Decoder.Token did. More reports whether the next token is
// anything but a closing delimiter, which is all decodeValue asks of it.
type unboundedTokens struct {
	toks []json.Token
	pos  int
}

func (u *unboundedTokens) Token() (json.Token, error) {
	if u.pos >= len(u.toks) {
		return nil, errUnexpectedEnd
	}
	tok := u.toks[u.pos]
	u.pos++
	return tok, nil
}

func (u *unboundedTokens) More() bool {
	if u.pos >= len(u.toks) {
		return false
	}
	d, ok := u.toks[u.pos].(json.Delim)
	return !ok || (d != ']' && d != '}')
}

var errUnexpectedEnd = errors.New("unexpected end of tokens")
