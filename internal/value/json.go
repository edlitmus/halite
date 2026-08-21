package value

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// JSON here is the canonical form of SPEC section 6.4: UTF-8, no HTML
// escaping, and integers that survive a round trip.
//
// Numbers are the known JSON hazard. Decoding uses json.Number semantics
// and never converts through float64, so a 64-bit integer grain such as
// mem_total in bytes, or a package epoch, comes back as the integer it
// went out as. Salt has bugs here; this does not inherit them.

// DecodeJSON parses JSON into the ordered model, keeping object key order
// rather than losing it to a Go map.
func DecodeJSON(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	// Reject trailing content, so that a truncated or concatenated
	// document is an error rather than a silent partial parse.
	if dec.More() {
		return nil, fmt.Errorf("unexpected content after the JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := NewMap(4)
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, fmt.Errorf("object key is not a string")
				}
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				if m.Has(key) {
					return nil, fmt.Errorf("duplicate object key %q", key)
				}
				m.Set(key, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return m, nil
		case '[':
			out := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return out, nil
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, nil
		}
		f, err := t.Float64()
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	return tok, nil
}

// EncodeJSON renders a value as JSON, preserving mapping order. indent of
// zero produces the compact form.
func EncodeJSON(v any, indent int) ([]byte, error) {
	return EncodeJSONSpaced(v, indent, false)
}

// EncodeJSONSpaced renders JSON with Python's separators when spaced is
// set: `, ` between items and `: ` after a key.
//
// It exists for the `tojson` filter. Python's json.dumps spaces its
// separators by default and Jinja's tojson inherits that, so a tree that
// writes JSON into a file through the filter produces spaced output under
// Salt. Rendering it compact here would make every such file differ on the
// first run after a migration, which reads as a change the tree did not
// ask for. The wire format of SPEC section 6.4 stays compact.
func EncodeJSONSpaced(v any, indent int, spaced bool) ([]byte, error) {
	var b bytes.Buffer
	if err := writeJSON(&b, v, indent, 0, spaced); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeJSON(b *bytes.Buffer, v any, indent, depth int, spaced bool) error {
	pad := func(n int) {
		if indent > 0 {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat(" ", indent*n))
		}
	}

	switch t := v.(type) {
	case *Map:
		if t == nil || t.Len() == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteByte('{')
		for i, e := range t.Entries() {
			if i > 0 {
				b.WriteByte(',')
				if spaced && indent == 0 {
					b.WriteByte(' ')
				}
			}
			pad(depth + 1)
			if err := writeJSONString(b, KeyString(e.Key)); err != nil {
				return err
			}
			b.WriteByte(':')
			if indent > 0 || spaced {
				b.WriteByte(' ')
			}
			if err := writeJSON(b, e.Val, indent, depth+1, spaced); err != nil {
				return err
			}
		}
		pad(depth)
		b.WriteByte('}')
		return nil

	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
				if spaced && indent == 0 {
					b.WriteByte(' ')
				}
			}
			pad(depth + 1)
			if err := writeJSON(b, item, indent, depth+1, spaced); err != nil {
				return err
			}
		}
		pad(depth)
		b.WriteByte(']')
		return nil

	case string:
		return writeJSONString(b, t)
	}

	// Scalars go through the standard encoder, which writes a trailing
	// newline the surrounding structure must not inherit.
	var scratch bytes.Buffer
	enc := json.NewEncoder(&scratch)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b.Write(bytes.TrimRight(scratch.Bytes(), "\n"))
	return nil
}

func writeJSONString(b *bytes.Buffer, s string) error {
	var scratch bytes.Buffer
	enc := json.NewEncoder(&scratch)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	b.Write(bytes.TrimRight(scratch.Bytes(), "\n"))
	return nil
}
