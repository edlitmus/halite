package template

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Python's printf-style formatting, `format % args`, which is what Jinja's
// `format` filter is: `"%s-%s" | format(a, b)` is `"%s-%s" % (a, b)`, and
// with keywords `"%(name)s" | format(name=x)` is `"%(name)s" % {...}`.
//
// It was fmt.Sprintf with a check for "%!" in the result. Go's verbs are
// not Python's, and where they differ they mostly differ quietly: `%x` of
// a string printed its bytes in hex, where Python refuses; `%g` without a
// precision printed Go's shortest digits, `0.3333333333333333`, where
// Python prints six significant ones, `0.333333`; `%d` of a float refused
// where Python truncates. And the loud half refused what Python takes:
// `%s` of a bool, `%r`, and `%(name)s` (issue 321, M-52).
//
// Each conversion is built on pyFormatValue, the format-spec path, with
// the differences between the two languages written out here. It is held
// to Python 3.12.3 by 3,000 generated cases, refusals included
// (testdata/pyformat/percent.tsv, TestPercentFormatMatchesPython). A `*`
// width or precision is refused rather than guessed at.
func pyPercent(format string, args []any, kwargs map[string]any) (string, error) {
	var b strings.Builder
	next := 0
	mapping := len(kwargs) > 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(format) {
			return "", fmt.Errorf("incomplete format")
		}
		var key string
		hasKey := false
		if format[i] == '(' {
			depth, j := 1, i+1
			for ; j < len(format) && depth > 0; j++ {
				switch format[j] {
				case '(':
					depth++
				case ')':
					depth--
				}
			}
			if depth > 0 {
				return "", fmt.Errorf("incomplete format key")
			}
			key, hasKey = format[i+1:j-1], true
			i = j
		}
		var flags []byte
		for i < len(format) && strings.IndexByte("#0- +", format[i]) >= 0 {
			flags = append(flags, format[i])
			i++
		}
		width, precision := -1, -1
		start := i
		for i < len(format) && format[i] >= '0' && format[i] <= '9' {
			i++
		}
		if i > start {
			width, _ = strconv.Atoi(format[start:i])
		}
		if i < len(format) && format[i] == '*' {
			return "", fmt.Errorf("format(): a '*' width is not supported")
		}
		if i < len(format) && format[i] == '.' {
			i++
			start = i
			for i < len(format) && format[i] >= '0' && format[i] <= '9' {
				i++
			}
			precision = 0
			if i > start {
				precision, _ = strconv.Atoi(format[start:i])
			}
			if i < len(format) && format[i] == '*' {
				return "", fmt.Errorf("format(): a '*' precision is not supported")
			}
		}
		for i < len(format) && strings.IndexByte("hlL", format[i]) >= 0 {
			i++
		}
		if i >= len(format) {
			return "", fmt.Errorf("incomplete format")
		}
		conv := format[i]
		if conv == '%' {
			b.WriteByte('%')
			continue
		}
		var v any
		switch {
		case hasKey:
			if !mapping {
				return "", fmt.Errorf("format requires a mapping")
			}
			kv, ok := kwargs[key]
			if !ok {
				return "", fmt.Errorf("KeyError: %q", key)
			}
			v = kv
		case mapping:
			// Python would take the whole mapping as the one argument;
			// a tree that means that can say so with a positional one.
			return "", fmt.Errorf("format(): a conversion without a (name) beside keyword arguments")
		default:
			if next >= len(args) {
				return "", fmt.Errorf("not enough arguments for format string")
			}
			v = args[next]
			next++
		}
		out, err := percentConvert(conv, flags, width, precision, v)
		if err != nil {
			return "", err
		}
		b.WriteString(out)
	}
	if !mapping && next < len(args) {
		return "", fmt.Errorf("not all arguments converted during string formatting")
	}
	return b.String(), nil
}

func hasFlag(flags []byte, f byte) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

// percentConvert is one conversion. The flags become a format spec: `-`
// is left alignment, which beats `0`; `0` is zero padding after the sign;
// `+` and ` ` are the sign; `#` is the alternate form.
func percentConvert(conv byte, flags []byte, width, precision int, v any) (string, error) {
	left := hasFlag(flags, '-')
	zero := hasFlag(flags, '0') && !left
	spec := func(typ string, withPrecision bool) string {
		var s strings.Builder
		switch {
		case left:
			s.WriteByte('<')
		case zero:
			s.WriteString("0=")
		default:
			s.WriteByte('>')
		}
		if hasFlag(flags, '+') {
			s.WriteByte('+')
		} else if hasFlag(flags, ' ') {
			s.WriteByte(' ')
		}
		if hasFlag(flags, '#') {
			s.WriteByte('#')
		}
		if width > 0 {
			s.WriteString(strconv.Itoa(width))
		}
		if withPrecision && precision >= 0 {
			s.WriteString("." + strconv.Itoa(precision))
		}
		s.WriteString(typ)
		return s.String()
	}
	pad := func(s string) string {
		align := byte('>')
		if left {
			align = '<'
		}
		return padRunes(s, width, align)
	}
	switch conv {
	case 's', 'r', 'a':
		var s string
		if conv == 's' {
			s = renderValue(v)
		} else {
			s = reprValue(v)
			if conv == 'a' {
				s = asciiEscape(s)
			}
		}
		if precision >= 0 && len([]rune(s)) > precision {
			s = string([]rune(s)[:precision])
		}
		return pad(s), nil
	case 'c':
		var r string
		switch t := v.(type) {
		case string:
			if len([]rune(t)) != 1 {
				return "", fmt.Errorf("%%c requires an int or a unicode character, not a string of length %d", len([]rune(t)))
			}
			r = t
		case int64, bool:
			n, _ := asInt(t)
			if n < 0 || n > 0x10ffff {
				return "", fmt.Errorf("%%c arg not in range(0x110000)")
			}
			r = string(rune(n))
		default:
			return "", fmt.Errorf("%%c requires an int or a unicode character, not %s", typeName(v))
		}
		return pad(r), nil
	case 'd', 'i', 'u':
		n, err := percentInt(v, conv, true)
		if err != nil {
			return "", err
		}
		return percentInteger(n, "d", flags, width, precision, left, zero, spec)
	case 'x', 'X', 'o':
		n, err := percentInt(v, conv, false)
		if err != nil {
			return "", err
		}
		return percentInteger(n, string(conv), flags, width, precision, left, zero, spec)
	case 'e', 'E', 'f', 'F', 'g', 'G':
		x, ok := percentFloat(v)
		if !ok {
			return "", fmt.Errorf("must be real number, not %s", typeName(v))
		}
		p := precision
		if p < 0 {
			p = 6
		}
		saved := precision
		precision = p
		out, err := pyFormatValue(x, spec(string(conv), true))
		precision = saved
		return out, err
	}
	return "", fmt.Errorf("unsupported format character '%c'", conv)
}

// percentInteger formats an integer conversion. Precision is the minimum
// number of digits, which format() has no way to say, so it is applied to
// the digits before the width is: `%.3d` of -5 is "-005".
func percentInteger(n int64, typ string, flags []byte, width, precision int, left, zero bool, spec func(string, bool) string) (string, error) {
	if precision < 0 {
		return pyFormatValue(n, spec(typ, false))
	}
	base := map[string]int{"d": 10, "x": 16, "X": 16, "o": 8}[typ]
	mag := uint64(n)
	if n < 0 {
		mag = uint64(-(n + 1)) + 1
	}
	digits := strconv.FormatUint(mag, base)
	if typ == "X" {
		digits = strings.ToUpper(digits)
	}
	prefix := ""
	if hasFlag(flags, '#') && typ != "d" {
		prefix = map[string]string{"x": "0x", "X": "0X", "o": "0o"}[typ]
	}
	if len(digits) < precision {
		digits = strings.Repeat("0", precision-len(digits)) + digits
	}
	sign := ""
	switch {
	case n < 0:
		sign = "-"
	case hasFlag(flags, '+'):
		sign = "+"
	case hasFlag(flags, ' '):
		sign = " "
	}
	body := sign + prefix + digits
	if zero && len(body) < width {
		return sign + prefix + strings.Repeat("0", width-len(body)) + digits, nil
	}
	align := byte('>')
	if left {
		align = '<'
	}
	return padRunes(body, width, align), nil
}

func percentInt(v any, conv byte, truncate bool) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	case float64:
		if !truncate {
			return 0, fmt.Errorf("%%%c format: an integer is required, not float", conv)
		}
		if math.IsInf(t, 0) {
			return 0, fmt.Errorf("cannot convert float infinity to integer")
		}
		if math.IsNaN(t) {
			return 0, fmt.Errorf("cannot convert float NaN to integer")
		}
		if math.Abs(t) >= 1<<63 {
			return 0, fmt.Errorf("%%%c format: %v is out of halite's integer range", conv, t)
		}
		return int64(t), nil
	}
	if truncate {
		return 0, fmt.Errorf("%%%c format: a real number is required, not %s", conv, typeName(v))
	}
	return 0, fmt.Errorf("%%%c format: an integer is required, not %s", conv, typeName(v))
}

func percentFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func padRunes(s string, width int, align byte) string {
	return pad("", s, width, ' ', align)
}

// asciiEscape is ascii() applied to a repr: every character past ASCII
// written as \x, \u or \U, as Python writes it.
func asciiEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		case r <= 0xff:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r <= 0xffff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	return b.String()
}
