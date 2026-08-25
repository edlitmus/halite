package migrate

import (
	"strings"
)

// PyFunction is one function found in a Salt custom module.
type PyFunction struct {
	Name string
	// Params are the parameter names in order, with `*args` and
	// `**kwargs` spelled as Python wrote them.
	Params []PyParam
	// Doc is the first line of the docstring, when there is one.
	Doc string
	// Line is where it was found, for the report.
	Line int
}

// PyParam is one parameter.
type PyParam struct {
	Name string
	// Default is what Python declared, verbatim. Empty means required.
	Default string
	// HasDefault tells an empty default apart from no default: `x=''`
	// is optional with an empty string, and `x` is required.
	HasDefault bool
	// Variadic marks `*args`; Keywords marks `**kwargs`.
	Variadic bool
	Keywords bool
}

// PyModule is what one Python file declares.
type PyModule struct {
	// File is the path, for the report.
	File string
	// Name is the module name Salt would use — `__virtualname__` when
	// the file sets one, and the filename otherwise. That is what a
	// state calling it says, so it is what the bridge must answer to.
	Name      string
	Functions []PyFunction
	// HasVirtual records a `__virtual__` function, which decides at
	// runtime whether the module loads at all. A bridge cannot infer
	// what it checks, and the skeleton says so rather than dropping it.
	HasVirtual bool
}

// ReadPyModule extracts what a Salt custom module declares.
//
// A line-oriented reader rather than a Python parser, and deliberately:
// the job is to turn an unbounded porting problem into a bounded one
// (SPEC 24.6), and a list of function names with their parameters does
// that. It is a starting point for a person, not a translation — a
// generated skeleton that looked complete would be worse than one that
// obviously is not.
func ReadPyModule(file string, src []byte) PyModule {
	module := PyModule{File: file, Name: moduleNameFor(file)}
	lines := strings.Split(string(src), "\n")

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		if name, ok := virtualName(line); ok {
			module.Name = name
			continue
		}
		// Only a top-level def. One indented is a method or a closure,
		// and Salt does not expose either.
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		rest, isDef := strings.CutPrefix(strings.TrimSpace(line), "def ")
		if !isDef {
			continue
		}
		name, params, ok := readDef(rest, lines, &i)
		if !ok {
			continue
		}
		if name == "__virtual__" {
			module.HasVirtual = true
			continue
		}
		// Salt treats a leading underscore as private, and the dunders
		// are the loader's own.
		if strings.HasPrefix(name, "_") {
			continue
		}
		module.Functions = append(module.Functions, PyFunction{
			Name: name, Params: params, Line: i + 1,
			Doc: docstringAfter(lines, i),
		})
	}
	return module
}

// readDef reads a `def` that may span several lines, and leaves the
// index on its last one.
func readDef(rest string, lines []string, i *int) (string, []PyParam, bool) {
	name, after, ok := strings.Cut(rest, "(")
	if !ok {
		return "", nil, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil, false
	}

	// Gather until the parentheses balance. A signature that never
	// closes is a file this reader cannot make sense of, and it stops
	// rather than consuming the rest of the module.
	depth := 1 + strings.Count(after, "(") - strings.Count(after, ")")
	body := after
	for depth > 0 && *i+1 < len(lines) {
		*i++
		next := lines[*i]
		depth += strings.Count(next, "(") - strings.Count(next, ")")
		body += " " + next
	}
	if depth > 0 {
		return "", nil, false
	}
	if close := strings.LastIndex(body, ")"); close >= 0 {
		body = body[:close]
	}
	return name, readParams(body), true
}

// readParams splits a parameter list on the commas that are not inside
// a default value.
func readParams(body string) []PyParam {
	var params []PyParam
	for _, raw := range splitTopLevel(body) {
		raw = strings.TrimSpace(raw)
		if raw == "" || raw == "/" || raw == "*" {
			// A bare `*` or `/` marks where positional-only and
			// keyword-only begin; neither is a parameter.
			continue
		}
		param := PyParam{}
		switch {
		case strings.HasPrefix(raw, "**"):
			param.Keywords = true
			raw = strings.TrimPrefix(raw, "**")
		case strings.HasPrefix(raw, "*"):
			param.Variadic = true
			raw = strings.TrimPrefix(raw, "*")
		}
		if name, def, ok := strings.Cut(raw, "="); ok {
			param.Name = strings.TrimSpace(name)
			param.Default = strings.TrimSpace(def)
			param.HasDefault = true
		} else {
			param.Name = strings.TrimSpace(raw)
		}
		// A type annotation is not part of the name.
		if name, _, ok := strings.Cut(param.Name, ":"); ok {
			param.Name = strings.TrimSpace(name)
		}
		if param.Name != "" {
			params = append(params, param)
		}
	}
	return params
}

// splitTopLevel splits on commas outside brackets and quotes, so a
// default of `['a', 'b']` stays one parameter.
func splitTopLevel(body string) []string {
	var out []string
	depth := 0
	var quote byte
	start := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		if quote != 0 {
			if c == quote && (i == 0 || body[i-1] != '\\') {
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, body[start:i])
				start = i + 1
			}
		}
	}
	return append(out, body[start:])
}

// virtualName reads `__virtualname__ = "nginx"`, which is the name a
// state calls the module by.
func virtualName(line string) (string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(line), "__virtualname__")
	if !ok {
		return "", false
	}
	value, ok := strings.CutPrefix(strings.TrimSpace(rest), "=")
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if len(value) < 2 {
		return "", false
	}
	if q := value[0]; q == '\'' || q == '"' {
		if end := strings.IndexByte(value[1:], q); end >= 0 {
			return value[1 : 1+end], true
		}
	}
	return "", false
}

// docstringAfter reads the first line of a docstring, when the next
// non-empty line opens one.
func docstringAfter(lines []string, defLine int) string {
	for i := defLine + 1; i < len(lines) && i < defLine+4; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		for _, quote := range []string{`"""`, `'''`} {
			rest, ok := strings.CutPrefix(trimmed, quote)
			if !ok {
				continue
			}
			// A one-line docstring closes on the same line.
			if end := strings.Index(rest, quote); end >= 0 {
				return strings.TrimSpace(rest[:end])
			}
			if rest = strings.TrimSpace(rest); rest != "" {
				return rest
			}
			// The text is on the next line, which is the common shape.
			if i+1 < len(lines) {
				return strings.TrimSpace(lines[i+1])
			}
		}
		return ""
	}
	return ""
}

// moduleNameFor is the filename without its extension, which is what
// Salt calls a module that sets no `__virtualname__`.
func moduleNameFor(file string) string {
	base := file
	if slash := strings.LastIndexAny(base, `/\`); slash >= 0 {
		base = base[slash+1:]
	}
	return strings.TrimSuffix(base, ".py")
}
