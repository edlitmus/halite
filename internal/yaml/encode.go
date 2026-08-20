package yaml

import (
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// EncodeOptions controls rendering.
type EncodeOptions struct {
	// Indent is the number of spaces per level. Default 2.
	Indent int
	// Flow renders collections inline, which is what the yaml_encode
	// template filter wants when it is embedding a value in one line.
	Flow bool
}

// Encode renders a value as YAML. It is the inverse of Parse for every
// value Parse can produce, and it always quotes a string that would
// otherwise resolve to something else, so a round trip is stable.
func Encode(v any, opts EncodeOptions) string {
	if opts.Indent <= 0 {
		opts.Indent = 2
	}
	var b strings.Builder
	if opts.Flow {
		writeFlow(&b, v)
		return b.String()
	}
	writeBlock(&b, v, 0, opts)
	return b.String()
}

// EncodeScalar renders a single scalar, quoting it when a plain rendering
// would resolve to a different type. It backs the yaml_dquote, yaml_squote,
// and yaml_encode filters.
func EncodeScalar(v any) string {
	var b strings.Builder
	writeScalar(&b, v)
	return b.String()
}

func writeBlock(b *strings.Builder, v any, indent int, opts EncodeOptions) {
	pad := strings.Repeat(" ", indent)
	switch t := v.(type) {
	case *value.Map:
		if t.Len() == 0 {
			b.WriteString("{}\n")
			return
		}
		for i, e := range t.Entries() {
			if i > 0 || indent > 0 {
				b.WriteString(pad)
			}
			writeScalar(b, e.Key)
			b.WriteByte(':')
			writeChild(b, e.Val, indent, opts)
		}
	case []any:
		if len(t) == 0 {
			b.WriteString("[]\n")
			return
		}
		for i, item := range t {
			if i > 0 || indent > 0 {
				b.WriteString(pad)
			}
			b.WriteByte('-')
			writeChild(b, item, indent, opts)
		}
	default:
		writeScalar(b, v)
		b.WriteByte('\n')
	}
}

// writeChild renders the value that follows a "key:" or a "-", choosing
// between the same line and the lines below.
func writeChild(b *strings.Builder, v any, indent int, opts EncodeOptions) {
	switch t := v.(type) {
	case *value.Map:
		if t.Len() == 0 {
			b.WriteString(" {}\n")
			return
		}
		b.WriteByte('\n')
		writeBlockIndented(b, v, indent+opts.Indent, opts)
	case []any:
		if len(t) == 0 {
			b.WriteString(" []\n")
			return
		}
		b.WriteByte('\n')
		writeBlockIndented(b, v, indent+opts.Indent, opts)
	default:
		b.WriteByte(' ')
		writeScalar(b, v)
		b.WriteByte('\n')
	}
}

func writeBlockIndented(b *strings.Builder, v any, indent int, opts EncodeOptions) {
	pad := strings.Repeat(" ", indent)
	switch t := v.(type) {
	case *value.Map:
		for _, e := range t.Entries() {
			b.WriteString(pad)
			writeScalar(b, e.Key)
			b.WriteByte(':')
			writeChild(b, e.Val, indent, opts)
		}
	case []any:
		for _, item := range t {
			b.WriteString(pad)
			b.WriteByte('-')
			writeChild(b, item, indent, opts)
		}
	}
}

func writeFlow(b *strings.Builder, v any) {
	switch t := v.(type) {
	case *value.Map:
		b.WriteByte('{')
		for i, e := range t.Entries() {
			if i > 0 {
				b.WriteString(", ")
			}
			writeScalar(b, e.Key)
			b.WriteString(": ")
			writeFlow(b, e.Val)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			writeFlow(b, item)
		}
		b.WriteByte(']')
	default:
		writeScalar(b, v)
	}
}

func writeScalar(b *strings.Builder, v any) {
	switch t := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case int:
		b.WriteString(strconv.Itoa(t))
	case float64:
		switch {
		case math.IsInf(t, 1):
			b.WriteString(".inf")
		case math.IsInf(t, -1):
			b.WriteString("-.inf")
		case math.IsNaN(t):
			b.WriteString(".nan")
		default:
			s := strconv.FormatFloat(t, 'g', -1, 64)
			b.WriteString(s)
			// Keep a whole-number float from resolving back as an int.
			if !strings.ContainsAny(s, ".eEn") {
				b.WriteString(".0")
			}
		}
	case []byte:
		b.WriteString("!!binary ")
		b.WriteString(base64.StdEncoding.EncodeToString(t))
	case time.Time:
		b.WriteString("!!timestamp ")
		b.WriteString(t.Format(time.RFC3339Nano))
	case string:
		writeString(b, t)
	default:
		writeString(b, fmt.Sprint(v))
	}
}

// writeString renders a string, quoting it whenever a plain rendering
// would parse back as something other than this string.
func writeString(b *strings.Builder, s string) {
	if needsQuoting(s) {
		b.WriteString(Quote(s))
		return
	}
	b.WriteString(s)
}

// Quote renders a string as a double-quoted YAML scalar with the escapes
// the parser accepts. It backs the yaml_dquote filter.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\x%02x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// SingleQuote renders a string as a single-quoted YAML scalar. It backs
// the yaml_squote filter.
func SingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func needsQuoting(s string) bool {
	if s == "" {
		return true
	}
	// A string that would resolve to a non-string must be quoted, and so
	// must one that only stays a string by way of a lint warning: output
	// this encoder produced should never make `lint` complain. The probe
	// runs through the real resolver so that the encoder and the parser
	// can never disagree about what is ambiguous.
	probe := &parser{opts: Options{Bool11: true}}
	if v := probe.resolvePlain(s, value.Pos{}); v != any(s) || len(probe.warnings) > 0 {
		return true
	}
	switch s[0] {
	case '-', '?', ':', ',', '[', ']', '{', '}', '#', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`', ' ':
		return true
	}
	if strings.ContainsAny(s, "\n\r\t\x00") {
		return true
	}
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") || strings.Contains(s, " #") {
		return true
	}
	if s != strings.TrimSpace(s) {
		return true
	}
	return false
}
