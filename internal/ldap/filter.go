package ldap

import (
	"fmt"
	"strings"
)

// The Filter choice tags of RFC 4511 section 4.5.1.7.
const (
	filterAnd             = 0
	filterOr              = 1
	filterNot             = 2
	filterEqualityMatch   = 3
	filterSubstrings      = 4
	filterGreaterOrEqual  = 5
	filterLessOrEqual     = 6
	filterPresent         = 7
	filterApproxMatch     = 8
	filterExtensibleMatch = 9
)

// The SubstringFilter choice tags.
const (
	substringInitial = 0
	substringAny     = 1
	substringFinal   = 2
)

// Escape makes a value safe to put inside a filter.
//
// RFC 4515 section 3. This is the LDAP injection boundary and the whole
// reason a filter is built rather than concatenated: a username of
// `*)(objectClass=*` in `(uid=%s)` turns a lookup for one account into
// a match for every account, and a bind as the first one found.
//
// Every reserved character becomes its `\xx` escape, so what goes into
// the filter is a value and never syntax.
func Escape(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch c {
		case '\\', '*', '(', ')', 0x00:
			fmt.Fprintf(&b, "\\%02x", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// EscapeDN makes a value safe to put inside a distinguished name.
//
// RFC 4514 section 2.4, which has a different reserved set from a
// filter's — and using the filter's escaping on a DN would leave `,`
// and `=` alone, which is how one attribute value becomes two.
func EscapeDN(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c == '\\' || c == ',' || c == '+' || c == '"' ||
			c == '<' || c == '>' || c == ';' || c == '=':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c == ' ' && (i == 0 || i == len(value)-1):
			// A leading or trailing space is significant and must be
			// escaped; one in the middle is not.
			b.WriteString("\\ ")
		case c == '#' && i == 0:
			b.WriteString("\\#")
		case c == 0x00:
			b.WriteString("\\00")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// ParseFilter turns an RFC 4515 filter string into its BER encoding.
//
// An estate writes its filter in the configuration, in the form every
// LDAP tool uses, and it is parsed here rather than passed through as a
// string — because a server takes BER, and a client that builds that
// BER from a parsed tree cannot be talked into producing a filter its
// author did not write.
func ParseFilter(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("an empty filter")
	}
	encoded, rest, err := parseFilter(s)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(rest) != "" {
		return nil, fmt.Errorf("the filter has trailing text: %q", rest)
	}
	return encoded, nil
}

func parseFilter(s string) ([]byte, string, error) {
	s = strings.TrimLeft(s, " ")
	if !strings.HasPrefix(s, "(") {
		return nil, "", fmt.Errorf("a filter starts with `(`, this starts with %q", first(s))
	}
	s = s[1:]
	if s == "" {
		return nil, "", fmt.Errorf("a filter that is only `(`")
	}

	switch s[0] {
	case '&', '|':
		tag := filterAnd
		if s[0] == '|' {
			tag = filterOr
		}
		parts, rest, err := parseFilterList(s[1:])
		if err != nil {
			return nil, "", err
		}
		if len(parts) == 0 {
			return nil, "", fmt.Errorf("an `%c` filter with nothing in it", s[0])
		}
		// The choice is a SET OF, encoded constructed with the choice's
		// own context tag.
		return ctxSeq(tag, parts...), rest, nil
	case '!':
		inner, rest, err := parseFilter(s[1:])
		if err != nil {
			return nil, "", err
		}
		rest, err = expectClose(rest)
		if err != nil {
			return nil, "", err
		}
		return ctxSeq(filterNot, inner), rest, nil
	}

	// A simple item: everything up to the matching `)`.
	end := strings.IndexByte(s, ')')
	if end < 0 {
		return nil, "", fmt.Errorf("a filter item with no closing `)`")
	}
	item := s[:end]
	encoded, err := parseItem(item)
	if err != nil {
		return nil, "", err
	}
	return encoded, s[end+1:], nil
}

func parseFilterList(s string) ([][]byte, string, error) {
	var parts [][]byte
	for {
		s = strings.TrimLeft(s, " ")
		if strings.HasPrefix(s, ")") {
			return parts, s[1:], nil
		}
		if s == "" {
			return nil, "", fmt.Errorf("a filter list with no closing `)`")
		}
		part, rest, err := parseFilter(s)
		if err != nil {
			return nil, "", err
		}
		parts = append(parts, part)
		s = rest
	}
}

func expectClose(s string) (string, error) {
	s = strings.TrimLeft(s, " ")
	if !strings.HasPrefix(s, ")") {
		return "", fmt.Errorf("expected `)`, found %q", first(s))
	}
	return s[1:], nil
}

// parseItem reads one `attribute<op>value` assertion.
func parseItem(item string) ([]byte, error) {
	for _, op := range []struct {
		token string
		tag   int
	}{
		{">=", filterGreaterOrEqual},
		{"<=", filterLessOrEqual},
		{"~=", filterApproxMatch},
	} {
		if attr, value, ok := strings.Cut(item, op.token); ok {
			return ctxSeq(op.tag, str(strings.TrimSpace(attr)), str(unescapeValue(value))), nil
		}
	}
	attr, value, ok := strings.Cut(item, "=")
	if !ok {
		return nil, fmt.Errorf("%q is not an assertion; a filter item is attribute=value", item)
	}
	attr = strings.TrimSpace(attr)
	if attr == "" {
		return nil, fmt.Errorf("a filter item with no attribute: %q", item)
	}
	if value == "*" {
		return ctxStr(filterPresent, attr), nil
	}
	if strings.Contains(value, "*") {
		return substringFilter(attr, value)
	}
	return ctxSeq(filterEqualityMatch, str(attr), str(unescapeValue(value))), nil
}

// substringFilter builds `(attr=a*b*c)`.
func substringFilter(attr, value string) ([]byte, error) {
	parts := strings.Split(value, "*")
	var pieces [][]byte
	for i, part := range parts {
		if part == "" {
			continue
		}
		tag := substringAny
		switch i {
		case 0:
			tag = substringInitial
		case len(parts) - 1:
			tag = substringFinal
		}
		pieces = append(pieces, ctxStr(tag, unescapeValue(part)))
	}
	if len(pieces) == 0 {
		return nil, fmt.Errorf("a substring filter with nothing to match: %q", value)
	}
	return ctxSeq(filterSubstrings, str(attr), seq(pieces...)), nil
}

// unescapeValue turns RFC 4515's `\xx` escapes back into bytes.
//
// The value in a parsed filter is carried as BER, where a `*` is a byte
// and not a wildcard, so the escaping that made it safe as text is
// removed here rather than being sent literally to the server.
func unescapeValue(v string) string {
	if !strings.Contains(v, "\\") {
		return v
	}
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] != '\\' || i+2 >= len(v)+1 {
			b.WriteByte(v[i])
			continue
		}
		if i+2 < len(v)+1 && i+3 <= len(v) {
			if hi, ok := hexValue(v[i+1]); ok {
				if lo, ok2 := hexValue(v[i+2]); ok2 {
					b.WriteByte(hi<<4 | lo)
					i += 2
					continue
				}
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

func hexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func first(s string) string {
	if s == "" {
		return ""
	}
	return s[:1]
}
