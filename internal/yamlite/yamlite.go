// Package yamlite is a zero-dependency parser for the YAML subset used by
// Salt-style SLS files: nested mappings, block lists, scalar values, quoted
// strings, and comments. It intentionally does not implement full YAML
// (no anchors, no flow collections beyond [] / {}, no multi-line scalars).
// Mappings preserve declaration order, which the state compiler relies on.
package yamlite

import (
	"fmt"
	"strings"
)

// Map is an insertion-ordered string-keyed mapping.
type Map struct {
	Keys []string
	Vals map[string]any
}

// NewMap returns an empty ordered map.
func NewMap() *Map { return &Map{Vals: map[string]any{}} }

// Set inserts or replaces a key, preserving first-insertion order. The
// parser rejects duplicate keys before ever replacing one; replacement
// exists for programmatic construction.
func (m *Map) Set(k string, v any) {
	if _, ok := m.Vals[k]; !ok {
		m.Keys = append(m.Keys, k)
	}
	m.Vals[k] = v
}

// Get returns the value for k and whether it was present.
func (m *Map) Get(k string) (any, bool) {
	v, ok := m.Vals[k]
	return v, ok
}

type line struct {
	indent int
	text   string
	num    int
}

type parser struct {
	lines []line
	pos   int
}

// Parse parses src and returns a tree of *Map, []any, and string scalars.
func Parse(src string) (any, error) {
	var ls []line
	for i, raw := range strings.Split(src, "\n") {
		t := stripComment(raw)
		t = strings.TrimRight(t, " \r")
		content := strings.TrimLeft(t, " ")
		if content == "" {
			continue
		}
		if strings.HasPrefix(content, "\t") {
			return nil, fmt.Errorf("line %d: tabs are not allowed in indentation", i+1)
		}
		ls = append(ls, line{indent: len(t) - len(content), text: content, num: i + 1})
	}
	if len(ls) == 0 {
		return NewMap(), nil
	}
	p := &parser{lines: ls}
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.lines) {
		return nil, fmt.Errorf("line %d: unexpected content %q", p.lines[p.pos].num, p.lines[p.pos].text)
	}
	return v, nil
}

func (p *parser) parseValue() (any, error) {
	cur := p.lines[p.pos]
	if isListItem(cur.text) {
		return p.parseList(cur.indent)
	}
	return p.parseMap(cur.indent)
}

func (p *parser) parseList(ind int) (any, error) {
	out := []any{}
	for p.pos < len(p.lines) {
		cur := p.lines[p.pos]
		if cur.indent != ind || !isListItem(cur.text) {
			if cur.indent > ind {
				return nil, fmt.Errorf("line %d: unexpected indent", cur.num)
			}
			break
		}
		body := strings.TrimSpace(strings.TrimPrefix(cur.text, "-"))
		if body == "" {
			p.pos++
			if p.pos < len(p.lines) && p.lines[p.pos].indent > ind {
				v, err := p.parseValue()
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			} else {
				out = append(out, nil)
			}
			continue
		}
		if k, v, ok := splitKV(body); ok {
			m := NewMap()
			if v == "" {
				p.pos++
				if p.pos < len(p.lines) && p.lines[p.pos].indent > ind {
					child, err := p.parseValue()
					if err != nil {
						return nil, err
					}
					m.Set(k, child)
				} else {
					m.Set(k, nil)
				}
			} else {
				sv, err := scalar(v)
				if err != nil {
					return nil, fmt.Errorf("line %d: %v", cur.num, err)
				}
				m.Set(k, sv)
				p.pos++
			}
			if err := p.continueItem(m, ind); err != nil {
				return nil, err
			}
			out = append(out, m)
			continue
		}
		sv, err := scalar(body)
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", cur.num, err)
		}
		out = append(out, sv)
		p.pos++
	}
	return out, nil
}

// continueItem absorbs the rest of a multi-key list item. In YAML, a list
// entry's content starts after the dash, and further keys sit at that same
// column:
//
//   - mount: /var
//     threshold: "90"
//
// Both keys belong to one mapping. Without this, the second line looks
// like a stray indent.
func (p *parser) continueItem(m *Map, listIndent int) error {
	if p.pos >= len(p.lines) {
		return nil
	}
	next := p.lines[p.pos]
	if next.indent <= listIndent || isListItem(next.text) {
		return nil
	}
	rest, err := p.parseMap(next.indent)
	if err != nil {
		return err
	}
	child, ok := rest.(*Map)
	if !ok {
		return fmt.Errorf("line %d: unexpected content in list item", next.num)
	}
	for _, key := range child.Keys {
		if _, dup := m.Vals[key]; dup {
			return fmt.Errorf("line %d: duplicate key %q in list item", next.num, key)
		}
		m.Set(key, child.Vals[key])
	}
	return nil
}

func (p *parser) parseMap(ind int) (any, error) {
	m := NewMap()
	for p.pos < len(p.lines) {
		cur := p.lines[p.pos]
		if cur.indent != ind || isListItem(cur.text) {
			if cur.indent > ind {
				return nil, fmt.Errorf("line %d: unexpected indent", cur.num)
			}
			break
		}
		k, v, ok := splitKV(cur.text)
		if !ok {
			return nil, fmt.Errorf("line %d: expected 'key:' or 'key: value', got %q", cur.num, cur.text)
		}
		if _, dup := m.Vals[k]; dup {
			return nil, fmt.Errorf("line %d: duplicate key %q", cur.num, k)
		}
		if v != "" {
			sv, err := scalar(v)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", cur.num, err)
			}
			m.Set(k, sv)
			p.pos++
			continue
		}
		p.pos++
		if p.pos < len(p.lines) && p.lines[p.pos].indent > ind {
			child, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			m.Set(k, child)
		} else {
			m.Set(k, nil)
		}
	}
	return m, nil
}

func isListItem(s string) bool {
	return s == "-" || strings.HasPrefix(s, "- ")
}

// quoteOpens reports whether a quote character at position i in s begins a
// quoted scalar. Per YAML, quotes are only special at the start of a scalar:
// at the start of the line's content, after a "- " list marker, or after a
// "key: " separator. A quote in the middle of a plain scalar is literal text.
func quoteOpens(s string, i int) bool {
	j := i
	for j > 0 && s[j-1] == ' ' {
		j--
	}
	if j == 0 {
		return true
	}
	if j == i {
		return false // glued to preceding text: mid-scalar, literal
	}
	switch s[j-1] {
	case ':':
		return true
	case '-':
		return j == 1 || s[j-2] == ' '
	}
	return false
}

// splitKV splits "key: value" or "key:" at the first unquoted colon that is
// followed by a space or end-of-line. Colons inside values (URLs, times) are
// left alone.
func splitKV(s string) (key, val string, ok bool) {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if q != 0 {
			if c == q {
				if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++ // '' inside single quotes is an escaped quote
					continue
				}
				q = 0
			}
			continue
		}
		if (c == '\'' || c == '"') && quoteOpens(s, i) {
			q = c
			continue
		}
		if c == ':' {
			if i == len(s)-1 {
				return unquote(strings.TrimSpace(s[:i])), "", true
			}
			if s[i+1] == ' ' {
				return unquote(strings.TrimSpace(s[:i])), strings.TrimSpace(s[i+1:]), true
			}
		}
	}
	return "", "", false
}

// scalar interprets a scalar value. The empty flow collections [] and {} are
// the only flow syntax supported; any other unquoted value starting with '['
// or '{' is an error rather than a silently misparsed string.
func scalar(v string) (any, error) {
	switch v {
	case "":
		return "", nil
	case "[]":
		return []any{}, nil
	case "{}":
		return NewMap(), nil
	}
	if v[0] == '[' || v[0] == '{' {
		return nil, fmt.Errorf("flow collection %q is not supported (only empty [] / {}); use block syntax, or quote the value to keep it as a string", v)
	}
	return unquote(v), nil
}

func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	if s[0] == '\'' && s[len(s)-1] == '\'' {
		// The only escape in single quotes, per YAML: '' is a literal '.
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	if s[0] == '"' && s[len(s)-1] == '"' {
		// Double quotes get escape processing, per YAML semantics.
		body := s[1 : len(s)-1]
		var b strings.Builder
		for i := 0; i < len(body); i++ {
			if body[i] == '\\' && i+1 < len(body) {
				i++
				switch body[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				case '\\':
					b.WriteByte('\\')
				case '"':
					b.WriteByte('"')
				default:
					b.WriteByte('\\')
					b.WriteByte(body[i])
				}
				continue
			}
			b.WriteByte(body[i])
		}
		return b.String()
	}
	return s
}

// stripComment removes a trailing "# ..." comment that is not inside quotes.
// A '#' only starts a comment at the beginning of the line or after a space.
func stripComment(s string) string {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if q != 0 {
			if c == q {
				if q == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++ // '' inside single quotes is an escaped quote
					continue
				}
				q = 0
			}
			continue
		}
		if (c == '\'' || c == '"') && quoteOpens(s, i) {
			q = c
			continue
		}
		if c == '#' && (i == 0 || s[i-1] == ' ') {
			return s[:i]
		}
	}
	return s
}
