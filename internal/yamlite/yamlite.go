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

// Set inserts or replaces a key, preserving first-insertion order.
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
				m.Set(k, scalar(v))
				p.pos++
			}
			out = append(out, m)
			continue
		}
		out = append(out, scalar(body))
		p.pos++
	}
	return out, nil
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
		if v != "" {
			m.Set(k, scalar(v))
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

// splitKV splits "key: value" or "key:" at the first unquoted colon that is
// followed by a space or end-of-line. Colons inside values (URLs, times) are
// left alone.
func splitKV(s string) (key, val string, ok bool) {
	var q byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if q != 0 {
			if c == q {
				q = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
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

func scalar(v string) any {
	switch v {
	case "[]":
		return []any{}
	case "{}":
		return NewMap()
	}
	return unquote(v)
}

func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	if s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
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
				q = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			q = c
			continue
		}
		if c == '#' && (i == 0 || s[i-1] == ' ') {
			return s[:i]
		}
	}
	return s
}
