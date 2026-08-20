package yaml

import (
	"encoding/base64"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// resolvePlain applies the scalar resolution rules of SPEC section 10.1.3
// to an unquoted scalar. Quoted scalars never come here: a quoted scalar
// is a string, always, which is how an operator writes a literal `yes` or
// a version number that would otherwise become a float.
func (p *parser) resolvePlain(s string, pos value.Pos) any {
	if s == "" {
		return nil
	}

	switch s {
	case "~", "null", "Null", "NULL":
		return nil
	case "true", "True", "TRUE":
		return true
	case "false", "False", "FALSE":
		return false
	}

	// YAML 1.1's extra boolean spellings. PyYAML resolves these, so Salt
	// resolves these, so existing trees depend on them. Every one is a
	// warning naming the file and line, and yaml_bool_11: false switches
	// a file or a tree to YAML 1.2 semantics once it has been audited.
	if b, ok := bool11(s); ok {
		if !p.opts.Bool11 {
			return s
		}
		p.warn(WarnBool11, pos, "%q resolves to the boolean %v under YAML 1.1; quote it to keep the string", s, b)
		return b
	}

	// A colon-separated number is a sexagesimal in YAML 1.1: 1:30 is 90.
	// That is never what an operator means in an SLS file, so it stays a
	// string, matching YAML 1.2, with a warning where it would have
	// differed.
	if isSexagesimal(s) {
		p.warn(WarnSexagesimal, pos, "%q is a YAML 1.1 sexagesimal number; halite reads it as a string", s)
		return s
	}

	if v, ok := p.resolveNumber(s, pos); ok {
		return v
	}

	// A date or a timestamp stays a string. Salt does not want a struct
	// here: a date that silently becomes a time value breaks file.managed
	// contents and pillar values that are meant to be written out
	// verbatim. An explicit !!timestamp tag still produces a timestamp.
	return s
}

// bool11 recognises the boolean spellings YAML 1.1 has and YAML 1.2 does
// not. The set is PyYAML's, exactly.
//
// SPEC section 10.1.3's table also lists the single letters y, Y, n, and N,
// with the rationale "PyYAML does this, so Salt does this, so existing
// trees depend on it". PyYAML does not: its bool resolver matches
//
//	yes|Yes|YES|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF
//
// and stops there. Implementing the table as written would make `name: n`
// a boolean here and a string in Salt, which breaks the compatibility the
// section exists to preserve. The letters are therefore omitted, and the
// divergence is recorded in docs/DIVERGENCE.md.
func bool11(s string) (bool, bool) {
	switch s {
	case "yes", "Yes", "YES", "on", "On", "ON":
		return true, true
	case "no", "No", "NO", "off", "Off", "OFF":
		return false, true
	}
	return false, false
}

// Bool11Spellings lists the scalars that resolve to a boolean only under
// YAML 1.1. The lint and migration reports use it to scan a tree.
func Bool11Spellings() []string {
	return []string{
		"yes", "Yes", "YES", "no", "No", "NO",
		"on", "On", "ON", "off", "Off", "OFF",
	}
}

// IsBool11 reports whether a scalar is one of the YAML 1.1-only boolean
// spellings.
func IsBool11(s string) bool { _, ok := bool11(s); return ok }

func isSexagesimal(s string) bool {
	body := strings.TrimPrefix(strings.TrimPrefix(s, "-"), "+")
	if !strings.Contains(body, ":") {
		return false
	}
	parts := strings.Split(body, ":")
	if len(parts) < 2 {
		return false
	}
	for i, part := range parts {
		if part == "" {
			return false
		}
		// The last field may be fractional in a sexagesimal float.
		if i == len(parts)-1 {
			part = strings.Replace(part, ".", "", 1)
			if part == "" {
				return false
			}
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func (p *parser) resolveNumber(s string, pos value.Pos) (any, bool) {
	sign := int64(1)
	body := s
	switch {
	case strings.HasPrefix(body, "-"):
		sign, body = -1, body[1:]
	case strings.HasPrefix(body, "+"):
		body = body[1:]
	}
	// A second sign is not a number. Without this check the leading sign
	// is stripped once and strconv parses the rest, so --1 would resolve
	// to 1 rather than staying the string it is.
	if body == "" || body[0] == '-' || body[0] == '+' {
		return nil, false
	}

	// Floats with a leading dot: .inf, -.Inf, .NAN.
	switch body {
	case ".inf", ".Inf", ".INF":
		return math.Inf(int(sign)), true
	case ".nan", ".NaN", ".NAN":
		return math.NaN(), true
	}

	lower := strings.ToLower(body)
	switch {
	case strings.HasPrefix(lower, "0x"):
		if n, err := strconv.ParseInt(stripUnderscores(body[2:]), 16, 64); err == nil {
			return sign * n, true
		}
		return nil, false
	case strings.HasPrefix(lower, "0o"):
		if n, err := strconv.ParseInt(stripUnderscores(body[2:]), 8, 64); err == nil {
			return sign * n, true
		}
		return nil, false
	case strings.HasPrefix(lower, "0b"):
		if n, err := strconv.ParseInt(stripUnderscores(body[2:]), 2, 64); err == nil {
			return sign * n, true
		}
		return nil, false
	}

	clean := stripUnderscores(body)
	if clean == "" || !hasDigit(clean) {
		return nil, false
	}

	// A leading zero on an all-digit scalar is octal in YAML 1.1. This is
	// the rule that turns a file mode written as 0644 into the decimal
	// 420, which is why the specification requires modes to be quoted.
	if len(clean) > 1 && clean[0] == '0' && allDigits(clean) {
		if n, err := strconv.ParseInt(clean[1:], 8, 64); err == nil {
			p.warn(WarnOctalImplicit, pos, "%q has a leading zero and is read as octal %d under YAML 1.1; quote it to keep the string", s, sign*n)
			return sign * n, true
		}
		return nil, false
	}

	if n, err := strconv.ParseInt(clean, 10, 64); err == nil {
		return sign * n, true
	}
	// Reject anything that is not a well-formed float: strconv accepts
	// "inf", "nan", and hex float forms that YAML does not.
	if !looksLikeFloat(clean) {
		return nil, false
	}
	if f, err := strconv.ParseFloat(clean, 64); err == nil {
		return float64(sign) * f, true
	}
	return nil, false
}

func stripUnderscores(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	return strings.ReplaceAll(s, "_", "")
}

func hasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// looksLikeFloat holds ParseFloat to YAML's float grammar, so that "inf",
// "nan", "1p3", and "0x1.8p3" are strings rather than numbers.
func looksLikeFloat(s string) bool {
	seenDigit, seenDot, seenExp := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			seenDigit = true
		case c == '.':
			if seenDot || seenExp {
				return false
			}
			seenDot = true
		case c == 'e' || c == 'E':
			if seenExp || !seenDigit {
				return false
			}
			seenExp = true
			if i+1 < len(s) && (s[i+1] == '+' || s[i+1] == '-') {
				i++
			}
			if i+1 >= len(s) {
				return false
			}
		default:
			return false
		}
	}
	return seenDigit && (seenDot || seenExp)
}

// applyTag applies an explicit tag to a scalar, which is the only path by
// which a timestamp or a binary value enters the model. An unknown tag is
// an error, including every !!python/... tag: refusing them is the
// security property, not an omission.
func (p *parser) applyTag(tag, raw string, quoted bool, pos value.Pos) (any, error) {
	switch tag {
	case "":
		if quoted {
			return raw, nil
		}
		return p.resolvePlain(raw, pos), nil
	case "!!str", "tag:yaml.org,2002:str":
		return raw, nil
	case "!!bool", "tag:yaml.org,2002:bool":
		switch strings.ToLower(raw) {
		case "true", "yes", "on", "y":
			return true, nil
		case "false", "no", "off", "n":
			return false, nil
		}
		return nil, p.errAt(pos, "!!bool value %q is not a boolean", raw)
	case "!!null", "tag:yaml.org,2002:null":
		return nil, nil
	case "!!int", "tag:yaml.org,2002:int":
		v, ok := p.resolveNumber(raw, pos)
		if n, isInt := v.(int64); ok && isInt {
			return n, nil
		}
		return nil, p.errAt(pos, "!!int value %q is not an integer", raw)
	case "!!float", "tag:yaml.org,2002:float":
		v, ok := p.resolveNumber(raw, pos)
		if !ok {
			return nil, p.errAt(pos, "!!float value %q is not a number", raw)
		}
		switch n := v.(type) {
		case float64:
			return n, nil
		case int64:
			return float64(n), nil
		}
		return nil, p.errAt(pos, "!!float value %q is not a number", raw)
	case "!!binary", "tag:yaml.org,2002:binary":
		clean := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
				return -1
			}
			return r
		}, raw)
		b, err := base64.StdEncoding.DecodeString(clean)
		if err != nil {
			return nil, p.errAt(pos, "!!binary value is not valid base64: %v", err)
		}
		return b, nil
	case "!!timestamp", "tag:yaml.org,2002:timestamp":
		if t, ok := parseTimestamp(raw); ok {
			return t, nil
		}
		return nil, p.errAt(pos, "!!timestamp value %q is not a timestamp", raw)
	case "!!seq", "tag:yaml.org,2002:seq", "!!map", "tag:yaml.org,2002:map":
		return nil, p.errAt(pos, "tag %s cannot be applied to a scalar", tag)
	}
	return nil, p.errAt(pos, "unsupported tag %s; halite constructs only the nine types in SPEC section 10.1.1", tag)
}

var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999Z0700",
	"2006-01-02t15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999 -0700",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseTimestamp(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
