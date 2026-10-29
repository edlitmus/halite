package template

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Python's format-specification mini-language, the part after the colon
// in `"{:>8.2f}".format(x)`.
//
// formatBraces found the spec and threw it away, so `"{:>5}"`, `"{:.2f}"`
// and `"{:,}"` rendered the bare value -- `a`, `3.14159`, `1234567` where
// Jinja, which is Python here, gives `    a`, `3.14` and `1,234,567`. A
// config file built that way came out unaligned, unrounded or ungrouped,
// with no error (issue 321, M-52).
//
//	format_spec ::= [[fill]align][sign]["z"]["#"]["0"][width]
//	                [grouping]["." precision][type]
//
// It is held to Python 3's format() by a differential of 6,000 generated
// value-and-spec pairs, errors included (TestPyFormatSpecMatchesPython).
// What it does not do -- the `n` type's locale, and formatting anything
// but a string, an integer, a bool or a float -- it refuses, rather than
// ignoring the spec as before.

type pySpec struct {
	fill      rune
	fillGiven bool
	align     byte // 0, '<', '>', '^', '='
	sign      byte // 0, '+', '-', ' '
	z         bool
	alt       bool
	zero      bool
	width     int
	grouping  byte // 0, ',', '_'
	precision int  // -1 when absent
	typ       byte // 0 when absent
}

func parsePySpec(spec string) (pySpec, error) {
	p := pySpec{fill: ' ', precision: -1}
	i := 0
	isAlign := func(b byte) bool { return b == '<' || b == '>' || b == '^' || b == '=' }
	if r, n := utf8.DecodeRuneInString(spec); n > 0 && n < len(spec) && isAlign(spec[n]) {
		p.fill, p.fillGiven, p.align = r, true, spec[n]
		i = n + 1
	} else if len(spec) > 0 && isAlign(spec[0]) {
		p.align = spec[0]
		i = 1
	}
	if i < len(spec) && (spec[i] == '+' || spec[i] == '-' || spec[i] == ' ') {
		p.sign = spec[i]
		i++
	}
	if i < len(spec) && spec[i] == 'z' {
		p.z = true
		i++
	}
	if i < len(spec) && spec[i] == '#' {
		p.alt = true
		i++
	}
	if i < len(spec) && spec[i] == '0' {
		p.zero = true
		i++
	}
	start := i
	for i < len(spec) && spec[i] >= '0' && spec[i] <= '9' {
		i++
	}
	if i > start {
		w, err := formatNumber(spec[start:i], "width")
		if err != nil {
			return p, err
		}
		p.width = w
	}
	if i < len(spec) && (spec[i] == ',' || spec[i] == '_') {
		p.grouping = spec[i]
		i++
	}
	if i < len(spec) && spec[i] == '.' {
		i++
		start = i
		for i < len(spec) && spec[i] >= '0' && spec[i] <= '9' {
			i++
		}
		if i == start {
			return p, fmt.Errorf("format specifier missing precision")
		}
		pr, err := formatNumber(spec[start:i], "precision")
		if err != nil {
			return p, err
		}
		p.precision = pr
	}
	if i < len(spec) {
		p.typ = spec[i]
		i++
	}
	if i != len(spec) {
		return p, fmt.Errorf("invalid format specifier %q", spec)
	}
	return p, nil
}

// maxFormatNumber bounds a format spec's width and precision. Both end
// up as a padded or zero-extended string, so a spec that names one is a
// request to allocate it, and the render's MaxOutput is not in reach of
// this path to refuse it afterwards. strconv.Atoi's error was discarded,
// so `{:>99999999999999999999}` became a width of MaxInt and
// `{:.9223372036854775807f}` a precision the same; under a 1G memory
// limit the second killed the process.
// The bound is DefaultOptions' MaxOutput, so nothing that could have been
// rendered is refused. Python raises for a number past ssize_t
// ("Too many decimal digits in format string") and would MemoryError on
// the rest; refusing both the same way is the nearest honest thing.
const maxFormatNumber = 64 << 20

// formatNumber reads the digits of a width or precision, refusing one
// that overflows or exceeds maxFormatNumber.
func formatNumber(digits, what string) (int, error) {
	n, err := strconv.Atoi(digits)
	if err != nil || n > maxFormatNumber {
		return 0, fmt.Errorf("format %s is too large (limit %d)", what, maxFormatNumber)
	}
	return n, nil
}

// pyFormatValue is format(v, spec).
func pyFormatValue(v any, spec string) (string, error) {
	if spec == "" {
		return renderValue(v), nil
	}
	p, err := parsePySpec(spec)
	if err != nil {
		return "", err
	}
	// The `0` flag is a fill of '0' when no fill was written, and, for a
	// number with no alignment written, `=`. Since Python 3.10 it does not
	// change a string's alignment.
	if p.zero && !p.fillGiven {
		p.fill = '0'
	}
	if _, isStr := v.(string); p.zero && p.align == 0 && !isStr {
		p.align = '='
	}
	switch t := v.(type) {
	case string:
		return p.formatString(t)
	case bool:
		// bool's __format__ is int's for any spec that is not empty.
		n := int64(0)
		if t {
			n = 1
		}
		return p.formatInt(n)
	case int64:
		return p.formatInt(t)
	case int:
		return p.formatInt(int64(t))
	case float64:
		return p.formatFloat(t)
	}
	return "", fmt.Errorf("format(): halite formats a string, an integer or a float with a spec, not %s", typeName(v))
}

func (p pySpec) formatString(s string) (string, error) {
	switch {
	case p.typ != 0 && p.typ != 's':
		return "", fmt.Errorf("Unknown format code '%c' for object of type 'str'", p.typ)
	case p.sign != 0:
		return "", fmt.Errorf("Sign not allowed in string format specifier")
	case p.alt:
		return "", fmt.Errorf("Alternate form (#) not allowed in string format specifier")
	case p.z:
		return "", fmt.Errorf("Negative zero coercion (z) not allowed in format specifier")
	case p.grouping != 0:
		return "", fmt.Errorf("Cannot specify '%c' with 's'.", p.grouping)
	case p.align == '=':
		return "", fmt.Errorf("'=' alignment not allowed in string format specifier")
	}
	if p.precision >= 0 && utf8.RuneCountInString(s) > p.precision {
		s = string([]rune(s)[:p.precision])
	}
	align := p.align
	if align == 0 {
		align = '<'
	}
	return pad("", s, p.width, p.fill, align), nil
}

func (p pySpec) formatInt(n int64) (string, error) {
	switch p.typ {
	case 'e', 'E', 'f', 'F', 'g', 'G', '%':
		return p.formatFloat(float64(n))
	case 0, 'd', 'n', 'b', 'o', 'x', 'X', 'c':
	default:
		return "", fmt.Errorf("Unknown format code '%c' for object of type 'int'", p.typ)
	}
	if p.precision >= 0 {
		return "", fmt.Errorf("Precision not allowed in integer format specifier")
	}
	if p.z {
		return "", fmt.Errorf("Negative zero coercion (z) not allowed in integer format specifier")
	}
	if p.typ == 'c' {
		switch {
		case p.sign != 0:
			return "", fmt.Errorf("Sign not allowed with integer format specifier 'c'")
		case p.alt:
			return "", fmt.Errorf("Alternate form (#) not allowed with integer format specifier 'c'")
		case p.grouping != 0:
			return "", fmt.Errorf("Cannot specify ',' with 'c'.")
		case n < 0 || n > 0x10ffff:
			return "", fmt.Errorf("%%c arg not in range(0x110000)")
		}
		align := p.align
		if align == 0 {
			align = '>'
		}
		return pad("", string(rune(n)), p.width, p.fill, align), nil
	}
	if p.typ == 'n' && p.grouping != 0 {
		return "", fmt.Errorf("Cannot specify ',' with 'n'.")
	}
	base, prefix, every := 10, "", 3
	switch p.typ {
	case 'b':
		base, prefix, every = 2, "0b", 4
	case 'o':
		base, prefix, every = 8, "0o", 4
	case 'x':
		base, prefix, every = 16, "0x", 4
	case 'X':
		base, prefix, every = 16, "0X", 4
	}
	if p.grouping == ',' && base != 10 {
		return "", fmt.Errorf("Cannot specify ',' with '%c'.", p.typ)
	}
	neg := n < 0
	var digits string
	if neg {
		digits = strconv.FormatUint(uint64(-(n+1))+1, base)
	} else {
		digits = strconv.FormatUint(uint64(n), base)
	}
	if p.typ == 'X' {
		digits = strings.ToUpper(digits)
	}
	if !p.alt {
		prefix = ""
	}
	return p.finishNumber(neg, prefix, digits, "", every), nil
}

func (p pySpec) formatFloat(x float64) (string, error) {
	switch p.typ {
	case 0, 'e', 'E', 'f', 'F', 'g', 'G', '%':
	case 'n':
		return "", fmt.Errorf("format(): the 'n' type follows the locale, which halite does not read")
	default:
		return "", fmt.Errorf("Unknown format code '%c' for object of type 'float'", p.typ)
	}
	neg := math.Signbit(x)
	ax := math.Abs(x)
	var body string
	upper := p.typ == 'E' || p.typ == 'F' || p.typ == 'G'
	switch {
	case math.IsNaN(x):
		neg = false
		body = "nan"
		if p.typ == '%' {
			body += "%"
		}
	case math.IsInf(x, 0):
		body = "inf"
		if p.typ == '%' {
			body += "%"
		}
	default:
		body = p.floatDigits(ax)
	}
	if upper {
		body = strings.ToUpper(body)
	}
	special := math.IsNaN(x) || math.IsInf(x, 0)
	if p.z && neg && !special && isZeroBody(body) {
		neg = false
	}
	// Grouping applies to the integer part only, and never to inf or nan,
	// which are still padded with the fill.
	if special {
		q := p
		q.grouping = 0
		return q.finishNumber(neg, "", body, "", 3), nil
	}
	intPart, rest := body, ""
	if i := strings.IndexAny(body, ".eE%"); i >= 0 {
		intPart, rest = body[:i], body[i:]
	}
	return p.finishNumber(neg, "", intPart, rest, 3), nil
}

// floatDigits is the unsigned number, before grouping and padding.
func (p pySpec) floatDigits(ax float64) string {
	prec := p.precision
	switch p.typ {
	case 'f', 'F':
		if prec < 0 {
			prec = 6
		}
		s := strconv.FormatFloat(ax, 'f', prec, 64)
		if p.alt && prec == 0 {
			s += "."
		}
		return s
	case '%':
		if prec < 0 {
			prec = 6
		}
		s := strconv.FormatFloat(ax*100, 'f', prec, 64)
		if p.alt && prec == 0 {
			s += "."
		}
		return s + "%"
	case 'e', 'E':
		if prec < 0 {
			prec = 6
		}
		s := strconv.FormatFloat(ax, 'e', prec, 64)
		if p.alt && prec == 0 {
			s = strings.Replace(s, "e", ".e", 1)
		}
		return s
	case 'g', 'G':
		if prec < 0 {
			prec = 6
		}
		return pyGeneral(ax, prec, p.alt, false)
	}
	// No type: repr without a precision; with one, 'g' that keeps a
	// ".0" on a whole number.
	if prec < 0 {
		return pyFloatRepr(ax, p.alt)
	}
	return pyGeneral(ax, prec, p.alt, true)
}

// pyGeneral is Python's 'g': `prec` significant digits, fixed point when
// the exponent is in [-4, prec), scientific otherwise, trailing zeros
// removed unless `alt`. `keepPoint` is the no-type rule, CPython's
// Py_DTSF_ADD_DOT_0: at least one digit after the point in fixed
// notation, and so scientific from an exponent of prec-1, which is why
// `"{:.0}".format(3.14)` is "3e+00" where `"{:.0g}"` is "3".
func pyGeneral(ax float64, prec int, alt, keepPoint bool) string {
	if prec == 0 {
		prec = 1
	}
	e := strconv.FormatFloat(ax, 'e', prec-1, 64)
	exp, _ := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	limit := prec
	if keepPoint {
		limit = prec - 1
	}
	var s string
	if exp >= -4 && exp < limit {
		s = strconv.FormatFloat(ax, 'f', prec-1-exp, 64)
		if !alt {
			if strings.Contains(s, ".") {
				s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
			}
			if keepPoint && !strings.Contains(s, ".") {
				s += ".0"
			}
		} else if !strings.Contains(s, ".") {
			s += "."
		}
		return s
	}
	mant, ex := e[:strings.IndexByte(e, 'e')], e[strings.IndexByte(e, 'e'):]
	if !alt {
		if strings.Contains(mant, ".") {
			mant = strings.TrimRight(strings.TrimRight(mant, "0"), ".")
		}
	} else if !strings.Contains(mant, ".") {
		mant += "."
	}
	return mant + ex
}

// pyFloatStr is Python's str() of a float, which is what a template
// renders. It was fixed point with ".0" below 1e15 and Go's shortest 'g'
// above, so 1e15 rendered "1e+15" where Python writes
// "1000000000000000.0", and an infinity "+Inf" where Python writes "inf"
// (issue 321, M-52).
func pyFloatStr(x float64) string {
	switch {
	case math.IsNaN(x):
		return "nan"
	case math.IsInf(x, 1):
		return "inf"
	case math.IsInf(x, -1):
		return "-inf"
	}
	if math.Signbit(x) {
		return "-" + pyFloatRepr(-x, false)
	}
	return pyFloatRepr(x, false)
}

// pyFloatRepr is repr() of a non-negative finite float: the shortest
// digits that read back, fixed point for exponents in [-4, 16), with ".0"
// on a whole number.
func pyFloatRepr(ax float64, alt bool) string {
	e := strconv.FormatFloat(ax, 'e', -1, 64)
	exp, _ := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if exp >= -4 && exp < 16 {
		s := strconv.FormatFloat(ax, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	mant, ex := e[:strings.IndexByte(e, 'e')], e[strings.IndexByte(e, 'e'):]
	if alt && !strings.Contains(mant, ".") {
		mant += "."
	}
	return mant + ex
}

func isZeroBody(s string) bool {
	for _, r := range s {
		if r >= '1' && r <= '9' {
			return false
		}
		if r == 'e' || r == 'E' {
			break
		}
	}
	return true
}

// finishNumber adds the sign, the prefix, grouping and padding, in the
// order Python does: with `=` alignment, or the `0` flag, the padding goes
// between the sign and the digits, and zero padding is grouped too.
func (p pySpec) finishNumber(neg bool, prefix, intDigits, rest string, every int) string {
	sign := ""
	switch {
	case neg:
		sign = "-"
	case p.sign == '+':
		sign = "+"
	case p.sign == ' ':
		sign = " "
	}
	align := p.align
	if align == 0 {
		align = '>'
	}
	if align == '=' {
		need := p.width - len(sign) - len(prefix) - utf8.RuneCountInString(rest)
		if p.grouping != 0 && p.fill == '0' {
			intDigits = groupZeroPadded(intDigits, need, every, p.grouping)
		} else {
			intDigits = group(intDigits, every, p.grouping)
			if n := utf8.RuneCountInString(intDigits); n < need {
				intDigits = strings.Repeat(string(p.fill), need-n) + intDigits
			}
		}
		return sign + prefix + intDigits + rest
	}
	return pad("", sign+prefix+group(intDigits, every, p.grouping)+rest, p.width, p.fill, align)
}

func group(digits string, every int, sep byte) string {
	if sep == 0 || len(digits) <= every {
		return digits
	}
	var b strings.Builder
	first := len(digits) % every
	if first == 0 {
		first = every
	}
	b.WriteString(digits[:first])
	for i := first; i < len(digits); i += every {
		b.WriteByte(sep)
		b.WriteString(digits[i : i+every])
	}
	return b.String()
}

// groupZeroPadded pads with zeros to width and groups the zeros as well,
// as Python does: `format(1234, "08,")` is "0,001,234", one wider than
// asked, because a separator never starts the field.
func groupZeroPadded(digits string, width, every int, sep byte) string {
	for {
		if g := group(digits, every, sep); len(g) >= width {
			return g
		}
		digits = "0" + digits
	}
}

func pad(prefix, s string, width int, fill rune, align byte) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return prefix + s
	}
	gap := width - n
	f := string(fill)
	switch align {
	case '<':
		return prefix + s + strings.Repeat(f, gap)
	case '^':
		left := gap / 2
		return prefix + strings.Repeat(f, left) + s + strings.Repeat(f, gap-left)
	}
	return prefix + strings.Repeat(f, gap) + s
}
