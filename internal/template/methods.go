package template

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/value"
)

// method resolves a bound method on a value. Salt trees lean on Python's
// string and dict methods heavily — `.split('.')`, `.startswith(...)`,
// `.get('key', default)` — so the common ones are implemented rather than
// left to filters.
func method(obj any, name string) (Callable, bool) {
	obj = untuple(obj)

	switch t := obj.(type) {
	case string:
		if fn, ok := stringMethod(t, name); ok {
			return fn, true
		}
	case *value.Map:
		if fn, ok := mapMethod(t, name); ok {
			return fn, true
		}
	case []any:
		if fn, ok := listMethod(t, name); ok {
			return fn, true
		}
	}
	return nil, false
}

func arg(args []any, kwargs map[string]any, i int, name string) (any, bool) {
	if i < len(args) {
		return args[i], true
	}
	if v, ok := kwargs[name]; ok {
		return v, true
	}
	return nil, false
}

func argString(args []any, kwargs map[string]any, i int, name string) (string, bool) {
	v, ok := arg(args, kwargs, i, name)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func mapMethod(m *value.Map, name string) (Callable, bool) {
	switch name {
	case "get":
		// Salt trees write `pillar.get('a:b:c', default)`. A literal key
		// is tried first, so a pillar key that genuinely contains a colon
		// still resolves; a colon path is the fallback. SPEC section
		// 10.2.7 names this spelling as supported.
		return funcValue{"get", func(args []any, kwargs map[string]any) (any, error) {
			key, ok := arg(args, kwargs, 0, "key")
			if !ok {
				return nil, fmt.Errorf("get() needs a key")
			}
			def, hasDef := arg(args, kwargs, 1, "default")
			if v, found := m.Get(normalizeKey(key)); found {
				return v, nil
			}
			if s, isStr := key.(string); isStr && strings.Contains(s, ":") {
				delim := ":"
				if d, ok := argString(args, kwargs, 2, "delimiter"); ok {
					delim = d
				}
				if v, found := value.Traverse(m, s, delim); found {
					return v, nil
				}
			}
			if hasDef {
				return def, nil
			}
			return nil, nil
		}}, true

	case "items":
		return funcValue{"items", func([]any, map[string]any) (any, error) {
			out := make([]any, 0, m.Len())
			for _, e := range m.Entries() {
				out = append(out, []any{e.Key, e.Val})
			}
			return out, nil
		}}, true

	case "keys":
		return funcValue{"keys", func([]any, map[string]any) (any, error) {
			ks := m.Keys()
			out := make([]any, len(ks))
			copy(out, ks)
			return out, nil
		}}, true

	case "values":
		return funcValue{"values", func([]any, map[string]any) (any, error) {
			out := make([]any, 0, m.Len())
			for _, e := range m.Entries() {
				out = append(out, e.Val)
			}
			return out, nil
		}}, true

	case "update":
		return funcValue{"update", func(args []any, kwargs map[string]any) (any, error) {
			if len(args) > 0 {
				src, ok := args[0].(*value.Map)
				if !ok {
					return nil, fmt.Errorf("update() takes a mapping")
				}
				for _, e := range src.Entries() {
					m.Set(e.Key, e.Val)
				}
			}
			for k, v := range kwargs {
				m.Set(k, v)
			}
			return nil, nil
		}}, true

	case "pop":
		return funcValue{"pop", func(args []any, kwargs map[string]any) (any, error) {
			key, ok := arg(args, kwargs, 0, "key")
			if !ok {
				return nil, fmt.Errorf("pop() needs a key")
			}
			v, found := m.Get(normalizeKey(key))
			if found {
				m.Delete(normalizeKey(key))
				return v, nil
			}
			if def, ok := arg(args, kwargs, 1, "default"); ok {
				return def, nil
			}
			return nil, fmt.Errorf("pop(): key %v is not present", key)
		}}, true

	case "setdefault":
		return funcValue{"setdefault", func(args []any, kwargs map[string]any) (any, error) {
			key, ok := arg(args, kwargs, 0, "key")
			if !ok {
				return nil, fmt.Errorf("setdefault() needs a key")
			}
			if v, found := m.Get(normalizeKey(key)); found {
				return v, nil
			}
			def, _ := arg(args, kwargs, 1, "default")
			m.Set(normalizeKey(key), def)
			return def, nil
		}}, true
	}
	return nil, false
}

func listMethod(l []any, name string) (Callable, bool) {
	switch name {
	case "count":
		return funcValue{"count", func(args []any, _ map[string]any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("count() takes one argument")
			}
			n := int64(0)
			for _, item := range l {
				if equalValues(item, args[0]) {
					n++
				}
			}
			return n, nil
		}}, true
	case "index":
		return funcValue{"index", func(args []any, _ map[string]any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("index() takes one argument")
			}
			for i, item := range l {
				if equalValues(item, args[0]) {
					return int64(i), nil
				}
			}
			return nil, fmt.Errorf("index(): value is not in the list")
		}}, true
	}
	return nil, false
}

func stringMethod(s, name string) (Callable, bool) {
	simple := func(fn func(string) string) (Callable, bool) {
		return funcValue{name, func([]any, map[string]any) (any, error) { return fn(s), nil }}, true
	}

	switch name {
	case "lower":
		return simple(strings.ToLower)
	case "upper":
		return simple(strings.ToUpper)
	case "title":
		return simple(titleCase)
	case "capitalize":
		return simple(capitalize)
	case "swapcase":
		return simple(swapCase)

	case "strip":
		return trimMethod(name, s, strings.Trim, strings.TrimSpace)
	case "lstrip":
		return trimMethod(name, s, strings.TrimLeft, func(x string) string { return strings.TrimLeft(x, " \t\r\n") })
	case "rstrip":
		return trimMethod(name, s, strings.TrimRight, func(x string) string { return strings.TrimRight(x, " \t\r\n") })

	case "split":
		return funcValue{name, func(args []any, kwargs map[string]any) (any, error) {
			sep, hasSep := argString(args, kwargs, 0, "sep")
			limit := -1
			if n, ok := arg(args, kwargs, 1, "maxsplit"); ok {
				if i, ok := asInt(n); ok && i >= 0 {
					limit = int(i) + 1
				}
			}
			var parts []string
			if !hasSep {
				parts = strings.Fields(s)
				if limit > 0 && len(parts) > limit {
					parts = append(parts[:limit-1], strings.Join(parts[limit-1:], " "))
				}
			} else if limit > 0 {
				parts = strings.SplitN(s, sep, limit)
			} else {
				parts = strings.Split(s, sep)
			}
			return stringsToAny(parts), nil
		}}, true

	case "rsplit":
		return funcValue{name, func(args []any, kwargs map[string]any) (any, error) {
			sep, hasSep := argString(args, kwargs, 0, "sep")
			if !hasSep {
				return stringsToAny(strings.Fields(s)), nil
			}
			return stringsToAny(strings.Split(s, sep)), nil
		}}, true

	case "splitlines":
		return funcValue{name, func([]any, map[string]any) (any, error) {
			trimmed := strings.TrimSuffix(s, "\n")
			if trimmed == "" {
				return []any{}, nil
			}
			return stringsToAny(strings.Split(trimmed, "\n")), nil
		}}, true

	case "join":
		return funcValue{name, func(args []any, _ map[string]any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("join() takes one argument")
			}
			items, ok := args[0].([]any)
			if !ok {
				return nil, fmt.Errorf("join() takes a sequence")
			}
			parts := make([]string, len(items))
			for i, item := range items {
				parts[i] = renderValue(item)
			}
			return strings.Join(parts, s), nil
		}}, true

	case "replace":
		return funcValue{name, func(args []any, kwargs map[string]any) (any, error) {
			old, ok1 := argString(args, kwargs, 0, "old")
			nw, ok2 := argString(args, kwargs, 1, "new")
			if !ok1 || !ok2 {
				return nil, fmt.Errorf("replace() takes two string arguments")
			}
			if n, ok := arg(args, kwargs, 2, "count"); ok {
				if i, ok := asInt(n); ok {
					return strings.Replace(s, old, nw, int(i)), nil
				}
			}
			return strings.ReplaceAll(s, old, nw), nil
		}}, true

	case "startswith", "endswith":
		return funcValue{name, func(args []any, _ map[string]any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("%s() takes one argument", name)
			}
			test := strings.HasPrefix
			if name == "endswith" {
				test = strings.HasSuffix
			}
			switch p := args[0].(type) {
			case string:
				return test(s, p), nil
			case []any:
				for _, item := range p {
					if ps, ok := item.(string); ok && test(s, ps) {
						return true, nil
					}
				}
				return false, nil
			}
			return nil, fmt.Errorf("%s() takes a string or a sequence of strings", name)
		}}, true

	case "format":
		return funcValue{name, func(args []any, kwargs map[string]any) (any, error) {
			return formatBraces(s, args, kwargs)
		}}, true

	case "find", "rfind":
		return funcValue{name, func(args []any, _ map[string]any) (any, error) {
			if len(args) < 1 {
				return nil, fmt.Errorf("%s() takes one argument", name)
			}
			sub, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("%s() takes a string", name)
			}
			if name == "rfind" {
				return int64(strings.LastIndex(s, sub)), nil
			}
			return int64(strings.Index(s, sub)), nil
		}}, true

	case "count":
		return funcValue{name, func(args []any, _ map[string]any) (any, error) {
			if len(args) != 1 {
				return nil, fmt.Errorf("count() takes one argument")
			}
			sub, ok := args[0].(string)
			if !ok {
				return nil, fmt.Errorf("count() takes a string")
			}
			return int64(strings.Count(s, sub)), nil
		}}, true

	case "isdigit":
		return funcValue{name, func([]any, map[string]any) (any, error) {
			if s == "" {
				return false, nil
			}
			for _, r := range s {
				if r < '0' || r > '9' {
					return false, nil
				}
			}
			return true, nil
		}}, true

	case "zfill":
		return funcValue{name, func(args []any, _ map[string]any) (any, error) {
			n, ok := asInt(args[0])
			if len(args) != 1 || !ok {
				return nil, fmt.Errorf("zfill() takes an integer")
			}
			if int64(len(s)) >= n {
				return s, nil
			}
			return strings.Repeat("0", int(n)-len(s)) + s, nil
		}}, true

	case "encode", "decode":
		// Present so that a tree carrying a Python 2 idiom renders rather
		// than failing; halite strings are already UTF-8 text.
		return funcValue{name, func([]any, map[string]any) (any, error) { return s, nil }}, true
	}
	return nil, false
}

func trimMethod(name, s string, cut func(string, string) string, def func(string) string) (Callable, bool) {
	return funcValue{name, func(args []any, kwargs map[string]any) (any, error) {
		if chars, ok := argString(args, kwargs, 0, "chars"); ok {
			return cut(s, chars), nil
		}
		return def(s), nil
	}}, true
}

func stringsToAny(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// formatBraces implements str.format() for the positional and named forms
// SLS trees use. Format specifications are not implemented; a template
// that needs one uses a filter.
func formatBraces(s string, args []any, kwargs map[string]any) (string, error) {
	var b strings.Builder
	auto := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '{' {
			if i+1 < len(s) && s[i+1] == '{' {
				b.WriteByte('{')
				i++
				continue
			}
			end := strings.IndexByte(s[i:], '}')
			if end < 0 {
				return "", fmt.Errorf("format(): unmatched { in %q", s)
			}
			field := s[i+1 : i+end]
			i += end
			if spec := strings.IndexByte(field, ':'); spec >= 0 {
				field = field[:spec]
			}
			switch {
			case field == "":
				if auto >= len(args) {
					return "", fmt.Errorf("format(): not enough positional arguments")
				}
				b.WriteString(renderValue(args[auto]))
				auto++
			case isAllDigits(field):
				n, _ := strconv.Atoi(field)
				if n >= len(args) {
					return "", fmt.Errorf("format(): positional argument %d is missing", n)
				}
				b.WriteString(renderValue(args[n]))
			default:
				v, ok := kwargs[field]
				if !ok {
					return "", fmt.Errorf("format(): keyword argument %q is missing", field)
				}
				b.WriteString(renderValue(v))
			}
			continue
		}
		if c == '}' {
			if i+1 < len(s) && s[i+1] == '}' {
				b.WriteByte('}')
				i++
				continue
			}
			return "", fmt.Errorf("format(): unmatched } in %q", s)
		}
		b.WriteByte(c)
	}
	return b.String(), nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
