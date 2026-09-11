package rendersandbox

import (
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/edlitmus/halite/internal/value"
)

// The codec that carries a parsed tree across the sandbox boundary.
//
// It exists because `value.EncodeJSON` is lossy in the two ways that
// matter here. It drops source positions, and a compiler that has lost
// them reports "this state is wrong" without saying which line -- which
// is most of what internal/value exists to provide. And JSON's number is
// a float64, so an int64 outside 2^53, a `.nan`, and an `.inf` do not
// survive it.
//
// So each value is tagged with its kind and carried in a form that
// round-trips exactly:
//
//   - integers and floats as text, which is what makes NaN, the
//     infinities, and large integers survive;
//   - binary as base64, which is what it is on the wire anyway;
//   - a string as a string when it is valid UTF-8 and as base64 when it
//     is not, because encoding/json silently replaces invalid bytes with
//     U+FFFD and `cmd.run` output reaches a template;
//   - positions as an index into a file table, since every position in
//     one render names one of very few files.
//
// A round-trip property test and a fuzz target hold it to being exact.

// codecVersion is carried in the envelope. The child and the parent are
// the same binary in every supported arrangement, so a mismatch means
// something is very wrong rather than something is merely old, and it is
// refused with that in mind.
const codecVersion = 1

// wireNode is one encoded value.
//
// One struct with optional fields rather than a type per kind: the
// decoder must see what arrived before it can know what it is, which is
// the same reason the bridge protocol uses one frame type.
type wireNode struct {
	K string `json:"k"`
	// B, N and S carry a bool, a number as text, and a string or base64
	// body. Which one is set follows from K.
	B bool        `json:"b,omitempty"`
	N string      `json:"n,omitempty"`
	S string      `json:"s,omitempty"`
	L []wireNode  `json:"l,omitempty"`
	E []wireEntry `json:"e,omitempty"`
	// P is the mapping's own position.
	P *wirePos `json:"p,omitempty"`
}

type wireEntry struct {
	K  wireNode `json:"k"`
	V  wireNode `json:"v"`
	KP *wirePos `json:"kp,omitempty"`
	VP *wirePos `json:"vp,omitempty"`
}

// wirePos is a position with the file interned. F indexes the envelope's
// file table.
type wirePos struct {
	F int `json:"f"`
	L int `json:"l"`
	C int `json:"c"`
}

const (
	kindNull   = "0"
	kindBool   = "b"
	kindInt    = "i"
	kindFloat  = "f"
	kindString = "s"
	// kindRawString is a string that is not valid UTF-8, carried as
	// base64 so that JSON does not replace its bytes.
	kindRawString = "x"
	kindBinary    = "y"
	kindTime      = "t"
	kindSeq       = "l"
	kindMap       = "m"
)

// files interns the file names positions refer to.
type files struct {
	names []string
	index map[string]int
}

func newFiles() *files { return &files{index: map[string]int{}} }

func (f *files) id(name string) int {
	if i, ok := f.index[name]; ok {
		return i
	}
	f.names = append(f.names, name)
	f.index[name] = len(f.names) - 1
	return len(f.names) - 1
}

func (f *files) name(i int) (string, error) {
	if i < 0 || i >= len(f.names) {
		return "", fmt.Errorf("a position names file %d, and the table has %d", i, len(f.names))
	}
	return f.names[i], nil
}

func (f *files) pos(p value.Pos) *wirePos {
	if p.IsZero() {
		return nil
	}
	return &wirePos{F: f.id(p.File), L: p.Line, C: p.Col}
}

func (f *files) unpos(p *wirePos) (value.Pos, error) {
	if p == nil {
		return value.Pos{}, nil
	}
	name, err := f.name(p.F)
	if err != nil {
		return value.Pos{}, err
	}
	return value.Pos{File: name, Line: p.L, Col: p.C}, nil
}

// encodeValue converts a parsed value to its wire form.
//
// A type outside the model is an error rather than a best effort. The
// alternative is a value that crosses the boundary as something else and
// is applied to a host as something else again.
func encodeValue(v any, f *files) (wireNode, error) {
	switch t := v.(type) {
	case nil:
		return wireNode{K: kindNull}, nil
	case bool:
		return wireNode{K: kindBool, B: t}, nil
	case int:
		return wireNode{K: kindInt, N: strconv.FormatInt(int64(t), 10)}, nil
	case int64:
		return wireNode{K: kindInt, N: strconv.FormatInt(t, 10)}, nil
	case uint64:
		return wireNode{K: kindInt, N: strconv.FormatUint(t, 10)}, nil
	case float64:
		return wireNode{K: kindFloat, N: formatFloat(t)}, nil
	case string:
		if utf8.ValidString(t) {
			return wireNode{K: kindString, S: t}, nil
		}
		return wireNode{K: kindRawString, S: base64.StdEncoding.EncodeToString([]byte(t))}, nil
	case []byte:
		return wireNode{K: kindBinary, S: base64.StdEncoding.EncodeToString(t)}, nil
	case time.Time:
		return wireNode{K: kindTime, S: t.Format(time.RFC3339Nano)}, nil
	case []any:
		out := wireNode{K: kindSeq, L: make([]wireNode, len(t))}
		for i, item := range t {
			enc, err := encodeValue(item, f)
			if err != nil {
				return wireNode{}, err
			}
			out.L[i] = enc
		}
		return out, nil
	case *value.Map:
		out := wireNode{K: kindMap, P: f.pos(t.Pos), E: make([]wireEntry, 0, t.Len())}
		for _, e := range t.Entries() {
			key, err := encodeValue(e.Key, f)
			if err != nil {
				return wireNode{}, err
			}
			val, err := encodeValue(e.Val, f)
			if err != nil {
				return wireNode{}, err
			}
			out.E = append(out.E, wireEntry{K: key, V: val, KP: f.pos(e.KeyPos), VP: f.pos(e.ValPos)})
		}
		return out, nil
	}
	return wireNode{}, fmt.Errorf("%T is not one of the types the render sandbox can carry", v)
}

// decodeValue converts a wire node back.
func decodeValue(n wireNode, f *files) (any, error) {
	switch n.K {
	case kindNull:
		return nil, nil
	case kindBool:
		return n.B, nil
	case kindInt:
		return strconv.ParseInt(n.N, 10, 64)
	case kindFloat:
		return parseFloat(n.N)
	case kindString:
		return n.S, nil
	case kindRawString:
		raw, err := base64.StdEncoding.DecodeString(n.S)
		if err != nil {
			return nil, err
		}
		return string(raw), nil
	case kindBinary:
		return base64.StdEncoding.DecodeString(n.S)
	case kindTime:
		return time.Parse(time.RFC3339Nano, n.S)
	case kindSeq:
		out := make([]any, len(n.L))
		for i, item := range n.L {
			v, err := decodeValue(item, f)
			if err != nil {
				return nil, err
			}
			out[i] = v
		}
		return out, nil
	case kindMap:
		pos, err := f.unpos(n.P)
		if err != nil {
			return nil, err
		}
		out := value.NewMap(len(n.E))
		out.Pos = pos
		for _, e := range n.E {
			key, err := decodeValue(e.K, f)
			if err != nil {
				return nil, err
			}
			val, err := decodeValue(e.V, f)
			if err != nil {
				return nil, err
			}
			keyPos, err := f.unpos(e.KP)
			if err != nil {
				return nil, err
			}
			valPos, err := f.unpos(e.VP)
			if err != nil {
				return nil, err
			}
			out.SetAt(key, val, keyPos, valPos)
		}
		return out, nil
	}
	return nil, fmt.Errorf("the render sandbox received a value tagged %q, which is not a kind", n.K)
}

// formatFloat writes a float so that ParseFloat gives the same bits
// back, including the two spellings JSON has no room for.
func formatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "+inf"
	case math.IsInf(v, -1):
		return "-inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func parseFloat(s string) (float64, error) {
	switch s {
	case "nan":
		return math.NaN(), nil
	case "+inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	}
	return strconv.ParseFloat(s, 64)
}

// encodeMap and decodeMap are the mapping-shaped helpers the request and
// response use for grains, pillar and the rest, where nil is a value in
// its own right rather than an empty mapping.
func encodeMap(m *value.Map, f *files) (*wireNode, error) {
	if m == nil {
		return nil, nil
	}
	n, err := encodeValue(m, f)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func decodeMap(n *wireNode, f *files) (*value.Map, error) {
	if n == nil {
		return nil, nil
	}
	v, err := decodeValue(*n, f)
	if err != nil {
		return nil, err
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("expected a mapping across the sandbox boundary and got %T", v)
	}
	return m, nil
}
