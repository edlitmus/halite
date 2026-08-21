package template

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/edlitmus/halite/internal/value"
)

// FilterContext gives a filter the render state it needs: the position for
// diagnostics and the deterministic random source.
type FilterContext struct {
	r   *renderer
	Pos Pos
}

// Rand returns the render's random source, seeded per SPEC section 10.2.4.
func (fc *FilterContext) Rand() *randSource { return &randSource{fc.r} }

type randSource struct{ r *renderer }

func (rs *randSource) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return rs.r.rnd.Intn(n)
}

// Str renders a value the way `{{ }}` does, honouring undefined mode.
func (fc *FilterContext) Str(v any) (string, error) { return fc.r.toStrErr(v, fc.Pos) }

// Errorf builds a positioned error.
func (fc *FilterContext) Errorf(format string, args ...any) error {
	return errorf(fc.Pos, format, args...)
}

// FilterFunc is one filter. Filters receive the piped value first, then
// the arguments written at the call site.
type FilterFunc func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error)

// applyFilter runs one filter of a chain.
func (r *renderer) applyFilter(f *FilterExpr, obj any) (any, error) {
	fn, ok := r.env.filters[f.Name]
	if !ok {
		return nil, errorf(f.Pos(), "unknown filter %q", f.Name)
	}
	args := make([]any, 0, len(f.Args))
	for _, a := range f.Args {
		v, err := r.eval(a)
		if err != nil {
			return nil, err
		}
		args = append(args, v)
	}
	kwargs := map[string]any{}
	for _, kw := range f.Kwargs {
		v, err := r.eval(kw.Val)
		if err != nil {
			return nil, err
		}
		kwargs[kw.Name] = v
	}
	fc := &FilterContext{r: r, Pos: f.Pos()}
	out, err := fn(fc, obj, args, kwargs)
	if err != nil {
		if _, ok := err.(*Error); ok {
			return nil, err
		}
		return nil, &Error{Pos: f.Pos(), Msg: err.Error(), Cause: err}
	}
	return out, nil
}

func standardFilters() map[string]FilterFunc {
	f := map[string]FilterFunc{}
	addCoreFilters(f)
	addSequenceFilters(f)
	addSaltFilters(f)
	return f
}

func addCoreFilters(f map[string]FilterFunc) {
	f["abs"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		if i, ok := asInt(v); ok {
			if i < 0 {
				return -i, nil
			}
			return i, nil
		}
		if fl, ok := asFloat(v); ok {
			return math.Abs(fl), nil
		}
		return nil, fc.Errorf("abs expects a number, found %s", typeName(v))
	}

	f["attr"] = func(fc *FilterContext, v any, args []any, _ map[string]any) (any, error) {
		if len(args) != 1 {
			return nil, fc.Errorf("attr takes one argument")
		}
		name, ok := args[0].(string)
		if !ok {
			return nil, fc.Errorf("attr takes a string")
		}
		return fc.r.getAttr(v, name, fc.Pos)
	}

	f["capitalize"] = strFilter("capitalize", capitalize)
	f["lower"] = strFilter("lower", strings.ToLower)
	f["upper"] = strFilter("upper", strings.ToUpper)
	f["title"] = strFilter("title", titleCase)
	f["trim"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		if chars, ok := argString(args, kwargs, 0, "chars"); ok {
			return strings.Trim(s, chars), nil
		}
		return strings.TrimSpace(s), nil
	}
	f["striptags"] = strFilter("striptags", stripTags)

	f["string"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		return fc.Str(v)
	}

	f["int"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		def, hasDef := arg(args, kwargs, 0, "default")
		base := int64(10)
		if b, ok := arg(args, kwargs, 1, "base"); ok {
			base, _ = asInt(b)
		}
		if n, ok := toInt(v, int(base)); ok {
			return n, nil
		}
		if hasDef {
			return def, nil
		}
		return int64(0), nil
	}

	f["float"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		if fl, ok := asFloat(v); ok {
			return fl, nil
		}
		if s, ok := v.(string); ok {
			if fl, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
				return fl, nil
			}
		}
		if def, ok := arg(args, kwargs, 0, "default"); ok {
			return def, nil
		}
		return 0.0, nil
	}

	f["round"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		x, ok := asFloat(v)
		if !ok {
			return nil, fc.Errorf("round expects a number, found %s", typeName(v))
		}
		precision := int64(0)
		if p, ok := arg(args, kwargs, 0, "precision"); ok {
			precision, _ = asInt(p)
		}
		method := "common"
		if m, ok := argString(args, kwargs, 1, "method"); ok {
			method = m
		}
		mul := math.Pow(10, float64(precision))
		switch method {
		case "ceil":
			return math.Ceil(x*mul) / mul, nil
		case "floor":
			return math.Floor(x*mul) / mul, nil
		case "common":
			return math.Round(x*mul) / mul, nil
		}
		return nil, fc.Errorf("round method must be common, ceil, or floor")
	}

	// `default` is the escape hatch for an optional value and must accept
	// an undefined without the strict-mode error firing.
	def := func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		fallback, hasFallback := arg(args, kwargs, 0, "default_value")
		if !hasFallback {
			fallback = ""
		}
		boolean := false
		if b, ok := arg(args, kwargs, 1, "boolean"); ok {
			boolean = truthy(b)
		}
		if IsUndefined(v) {
			return fallback, nil
		}
		if boolean && !truthy(v) {
			return fallback, nil
		}
		return v, nil
	}
	f["default"] = def
	f["d"] = def

	f["length"] = lengthFilter
	f["count"] = lengthFilter

	f["escape"] = strFilter("escape", htmlEscape)
	f["e"] = strFilter("escape", htmlEscape)
	f["forceescape"] = strFilter("forceescape", htmlEscape)
	// Output is not HTML, so `safe` is the identity. It is present so a
	// tree carrying it renders.
	f["safe"] = func(_ *FilterContext, v any, _ []any, _ map[string]any) (any, error) { return v, nil }

	f["format"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return sprintfPython(s, args)
	}

	f["indent"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		width := int64(4)
		if w, ok := arg(args, kwargs, 0, "width"); ok {
			width, _ = asInt(w)
		}
		first := false
		if b, ok := arg(args, kwargs, 1, "first"); ok {
			first = truthy(b)
		}
		blank := false
		if b, ok := arg(args, kwargs, 2, "blank"); ok {
			blank = truthy(b)
		}
		pad := strings.Repeat(" ", int(width))
		lines := strings.Split(s, "\n")
		for i, ln := range lines {
			if i == 0 && !first {
				continue
			}
			if ln == "" && !blank {
				continue
			}
			lines[i] = pad + ln
		}
		return strings.Join(lines, "\n"), nil
	}

	f["center"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		width := int64(80)
		if w, ok := arg(args, kwargs, 0, "width"); ok {
			width, _ = asInt(w)
		}
		gap := int(width) - len([]rune(s))
		if gap <= 0 {
			return s, nil
		}
		left := gap / 2
		return strings.Repeat(" ", left) + s + strings.Repeat(" ", gap-left), nil
	}

	f["truncate"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		length := int64(255)
		if l, ok := arg(args, kwargs, 0, "length"); ok {
			length, _ = asInt(l)
		}
		killwords := false
		if b, ok := arg(args, kwargs, 1, "killwords"); ok {
			killwords = truthy(b)
		}
		end := "..."
		if e, ok := argString(args, kwargs, 2, "end"); ok {
			end = e
		}
		rs := []rune(s)
		if int64(len(rs)) <= length {
			return s, nil
		}
		cut := int(length) - len([]rune(end))
		if cut < 0 {
			cut = 0
		}
		out := string(rs[:cut])
		if !killwords {
			if i := strings.LastIndexByte(out, ' '); i > 0 {
				out = out[:i]
			}
		}
		return out + end, nil
	}

	f["wordcount"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return int64(len(strings.Fields(s))), nil
	}

	f["wordwrap"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		width := int64(79)
		if w, ok := arg(args, kwargs, 0, "width"); ok {
			width, _ = asInt(w)
		}
		return wordwrap(s, int(width)), nil
	}

	f["replace"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		old, ok1 := argString(args, kwargs, 0, "old")
		nw, ok2 := argString(args, kwargs, 1, "new")
		if !ok1 || !ok2 {
			return nil, fc.Errorf("replace takes two string arguments")
		}
		if n, ok := arg(args, kwargs, 2, "count"); ok {
			if i, ok := asInt(n); ok {
				return strings.Replace(s, old, nw, int(i)), nil
			}
		}
		return strings.ReplaceAll(s, old, nw), nil
	}

	f["filesizeformat"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		n, ok := asFloat(v)
		if !ok {
			return nil, fc.Errorf("filesizeformat expects a number")
		}
		binary := false
		if b, ok := arg(args, kwargs, 0, "binary"); ok {
			binary = truthy(b)
		}
		return humanSize(n, binary), nil
	}

	f["pprint"] = func(_ *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		return reprValue(v), nil
	}

	f["tojson"] = jsonFilter
	f["to_json"] = jsonFilter

	f["urlencode"] = func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return urlEncode(s), nil
	}

	f["xmlattr"] = func(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
		m, ok := v.(*value.Map)
		if !ok {
			return nil, fc.Errorf("xmlattr expects a mapping")
		}
		var b strings.Builder
		for _, e := range m.Entries() {
			if e.Val == nil || IsUndefined(e.Val) {
				continue
			}
			b.WriteByte(' ')
			b.WriteString(value.KeyString(e.Key))
			b.WriteString(`="`)
			b.WriteString(htmlEscape(renderValue(e.Val)))
			b.WriteByte('"')
		}
		return b.String(), nil
	}
}

func lengthFilter(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
	switch t := v.(type) {
	case string:
		return int64(len([]rune(t))), nil
	case []any:
		return int64(len(t)), nil
	case *value.Map:
		return int64(t.Len()), nil
	case map[string]any:
		return int64(len(t)), nil
	case nil:
		return int64(0), nil
	case Undefined:
		if err := fc.r.undefinedError(t, fc.Pos); err != nil {
			return nil, err
		}
		return int64(0), nil
	}
	return nil, fc.Errorf("length expects a string, sequence, or mapping, found %s", typeName(v))
}

func jsonFilter(fc *FilterContext, v any, args []any, kwargs map[string]any) (any, error) {
	indent := int64(0)
	if i, ok := arg(args, kwargs, 0, "indent"); ok {
		indent, _ = asInt(i)
	}
	// Spaced separators, because Python's json.dumps spaces them by
	// default and Jinja's tojson inherits that. A tree writing JSON into a
	// file through this filter produced spaced output under Salt, and
	// compact output here would make the file differ on the first run
	// after a migration.
	b, err := value.EncodeJSONSpaced(stripUndefined(v), int(indent), true)
	if err != nil {
		return nil, fc.Errorf("tojson: %v", err)
	}
	return string(b), nil
}

// stripUndefined replaces undefined markers with null, so that a partly
// built structure still serialises under permissive mode.
func stripUndefined(v any) any {
	switch t := v.(type) {
	case Undefined:
		return nil
	case *value.Map:
		out := value.NewMap(t.Len())
		for _, e := range t.Entries() {
			out.SetAt(e.Key, stripUndefined(e.Val), e.KeyPos, e.ValPos)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = stripUndefined(item)
		}
		return out
	}
	return v
}

func strFilter(name string, fn func(string) string) FilterFunc {
	return func(fc *FilterContext, v any, _ []any, _ map[string]any) (any, error) {
		s, err := fc.Str(v)
		if err != nil {
			return nil, err
		}
		return fn(s), nil
	}
}

func toInt(v any, base int) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(t)
		if base != 10 && base != 0 {
			if n, err := strconv.ParseInt(s, base, 64); err == nil {
				return n, true
			}
			return 0, false
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
		if fl, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(fl), true
		}
	}
	return 0, false
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	rs := []rune(strings.ToLower(s))
	rs[0] = unicode.ToUpper(rs[0])
	return string(rs)
}

func titleCase(s string) string {
	var b strings.Builder
	startOfWord := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if startOfWord {
				b.WriteRune(unicode.ToUpper(r))
			} else {
				b.WriteRune(unicode.ToLower(r))
			}
			startOfWord = false
		default:
			b.WriteRune(r)
			startOfWord = true
		}
	}
	return b.String()
}

func swapCase(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsUpper(r) {
			return unicode.ToLower(r)
		}
		return unicode.ToUpper(r)
	}, s)
}

func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}

func urlEncode(s string) string {
	const safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~/"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func humanSize(n float64, binary bool) string {
	base := 1000.0
	units := []string{"kB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"}
	if binary {
		base = 1024.0
		units = []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB", "ZiB", "YiB"}
	}
	if n < base {
		return fmt.Sprintf("%d Bytes", int64(n))
	}
	for i, u := range units {
		limit := math.Pow(base, float64(i+2))
		if n < limit || i == len(units)-1 {
			return fmt.Sprintf("%.1f %s", n/math.Pow(base, float64(i+1)), u)
		}
	}
	return fmt.Sprintf("%.1f %s", n, units[len(units)-1])
}

func wordwrap(s string, width int) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, w := range words[1:] {
			if len(line)+1+len(w) > width {
				out = append(out, line)
				line = w
				continue
			}
			line += " " + w
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// sprintfPython implements the printf-style `format` filter for the
// verbs SLS trees use.
func sprintfPython(format string, args []any) (string, error) {
	native := make([]any, len(args))
	for i, a := range args {
		switch t := a.(type) {
		case int64:
			native[i] = t
		case float64:
			native[i] = t
		case string:
			native[i] = t
		case bool:
			native[i] = t
		default:
			native[i] = renderValue(a)
		}
	}
	out := fmt.Sprintf(format, native...)
	if strings.Contains(out, "%!") {
		return "", fmt.Errorf("format %q does not match its arguments", format)
	}
	return out, nil
}

// sortAny orders a mixed sequence the way Jinja's sort does: numbers
// numerically, strings lexically, everything else by rendered form.
func sortAny(items []any, reverse, caseSensitive bool, key func(any) any) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := key(items[i]), key(items[j])
		less := lessValue(a, b, caseSensitive)
		if reverse {
			return lessValue(b, a, caseSensitive)
		}
		return less
	})
}

func lessValue(a, b any, caseSensitive bool) bool {
	as, aIsStr := a.(string)
	bs, bIsStr := b.(string)
	if aIsStr && bIsStr {
		if !caseSensitive {
			return strings.ToLower(as) < strings.ToLower(bs)
		}
		return as < bs
	}
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if aok && bok {
		return af < bf
	}
	return renderValue(a) < renderValue(b)
}
