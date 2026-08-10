package compat

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/yamlite"
)

// placeholder stands in for a template expression when a file is parsed
// after neutralisation. It is a plain scalar, so the surrounding YAML keeps
// its shape.
const placeholder = "halite_template_value"

// scanText reports the constructs a Salt SLS file can carry that halite's
// renderer and YAML parser do not accept. It works on the raw text because
// most of them stop the file from rendering at all, so there is nothing
// later in the pipeline to inspect.
func scanText(src string) []Finding {
	var out []Finding
	blockScalarIndent := -1
	for i, raw := range strings.Split(src, "\n") {
		num := i + 1
		line := strings.TrimRight(raw, " \r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if blockScalarIndent >= 0 {
			if indent > blockScalarIndent {
				continue // the body of a block scalar is content, not YAML
			}
			blockScalarIndent = -1
		}
		if num == 1 && strings.HasPrefix(line, "#!") {
			out = append(out, rendererFinding(num, strings.TrimSpace(line)))
			continue
		}
		out = append(out, jinjaFindings(num, line)...)

		// The YAML checks read the line with its template constructs taken
		// out: a line that is all Jinja has already been reported, and its
		// braces are not a flow collection.
		content := strings.TrimSpace(yamlite.StripComment(withoutTemplates(line)))
		if content == "" {
			continue
		}
		if strings.Contains(line[:indent], "\t") {
			out = append(out, Finding{
				Line: num, Severity: SevError, Code: "yaml-tab",
				Message: "tab in indentation",
				Hint:    "yamlite rejects tabs, as YAML does; indent with spaces",
			})
		}
		f, isBlock, ok := yamlFinding(num, content)
		if ok {
			out = append(out, f)
		}
		if isBlock {
			blockScalarIndent = indent
		}
	}
	return out
}

// withoutTemplates removes Jinja statements and comments from a line and
// reduces every expression to a plain scalar.
func withoutTemplates(line string) string {
	line = replaceSpans(line, "{%", "%}", "")
	line = replaceSpans(line, "{#", "#}", "")
	return replaceSpans(line, "{{", "}}", placeholder)
}

// rendererFinding reads a Salt renderer shebang. halite has one renderer —
// Go text/template over its YAML subset — so the line is at best a no-op.
func rendererFinding(num int, line string) Finding {
	spec := strings.TrimPrefix(line, "#!")
	f := Finding{Line: num, Code: "renderer", Message: fmt.Sprintf("renderer line %q", line)}
	switch {
	case strings.Contains(spec, "gpg"):
		f.Severity = SevError
		f.Message = "GPG renderer"
		f.Hint = "halite does not decrypt pillar; keep the tree at mode 0700 and decrypt into it with sops/age/git-crypt (docs/pillar-security.md)"
	case strings.Contains(spec, "py") || strings.Contains(spec, "mako") ||
		strings.Contains(spec, "wempy") || strings.Contains(spec, "stateconf"):
		f.Severity = SevError
		f.Message = fmt.Sprintf("renderer %q is not available", spec)
		f.Hint = "halite renders Go text/template over YAML only; the file has to be rewritten as data"
	default:
		f.Severity = SevInfo
		f.Message = fmt.Sprintf("renderer line %q is ignored", line)
		f.Hint = "halite has one renderer: Go text/template over its YAML subset"
	}
	return f
}

// yamlFinding checks one line of YAML content for features yamlite does not
// implement. isBlock reports a block scalar opener, whose body the caller
// then skips.
func yamlFinding(num int, content string) (f Finding, isBlock, ok bool) {
	at := func(sev Severity, code, msg, hint string) (Finding, bool, bool) {
		return Finding{Line: num, Severity: sev, Code: code, Message: msg, Hint: hint}, false, true
	}
	switch {
	case content == "---" || content == "..." || strings.HasPrefix(content, "--- "):
		return at(SevError, "yaml-multi-doc", "document separator",
			"yamlite parses a single document per file")
	case strings.HasPrefix(content, "? "):
		return at(SevError, "yaml-complex-key", "explicit key syntax",
			"yamlite keys are plain scalars")
	}

	body := content
	if body == "-" || strings.HasPrefix(body, "- ") {
		body = strings.TrimSpace(strings.TrimPrefix(body, "-"))
	}
	key, val, hasKV := yamlite.SplitKV(body)
	if !hasKV {
		val = body
	}
	switch {
	case key == "<<":
		return at(SevError, "yaml-merge-key", "merge key '<<'",
			"yamlite has no anchors; repeat the keys or move the shared data into pillar")
	case isBlockScalar(val):
		f := Finding{Line: num, Severity: SevError, Code: "yaml-block-scalar",
			Message: fmt.Sprintf("block scalar %q", val),
			Hint:    "yamlite has no multi-line scalars; ship the text as a file next to the SLS and use 'source:', or put it on one line in double quotes with \\n"}
		return f, true, true
	case strings.HasPrefix(val, "&"):
		return at(SevError, "yaml-anchor", "anchor definition",
			"yamlite has no anchors or aliases")
	case strings.HasPrefix(val, "*") && val != "*":
		return at(SevError, "yaml-alias", "alias reference",
			"yamlite has no anchors or aliases")
	case strings.HasPrefix(val, "!"):
		return at(SevError, "yaml-tag", fmt.Sprintf("type tag %q", strings.Fields(val)[0]),
			"yamlite has no tags; every scalar is a string")
	case isFlowCollection(val):
		return at(SevError, "yaml-flow", fmt.Sprintf("flow collection %q", val),
			"yamlite takes only the empty forms [] and {}; write a block list ('- item' on its own lines), or quote the value to keep it a string")
	}
	return Finding{}, false, false
}

func isBlockScalar(val string) bool {
	switch val {
	case "|", "|-", "|+", ">", ">-", ">+", "|2", ">2":
		return true
	}
	return false
}

// isFlowCollection reports a non-empty [a, b] or {a: b}. yamlite understands
// the empty forms and nothing else.
func isFlowCollection(val string) bool {
	if val == "[]" || val == "{}" {
		return false
	}
	return strings.HasPrefix(val, "[") || strings.HasPrefix(val, "{")
}

// jinjaFindings reports the Jinja constructs on one line. halite's template
// delimiters are Jinja's, so {{ ... }} is only a problem when what is
// inside it is Jinja rather than Go template syntax; {% ... %} and {# ... #}
// never are.
func jinjaFindings(num int, line string) []Finding {
	var out []Finding
	for _, tag := range spans(line, "{%", "%}") {
		out = append(out, blockTagFinding(num, tag))
	}
	if strings.Contains(line, "{#") {
		out = append(out, Finding{
			Line: num, Severity: SevError, Code: "jinja-comment",
			Message: "Jinja comment {# ... #}",
			Hint:    "Go templates comment with {{/* ... */}}, and YAML with #",
		})
	}
	for _, expr := range spans(line, "{{", "}}") {
		if f, ok := exprFinding(num, expr); ok {
			out = append(out, f)
		}
	}
	return out
}

// jinjaTagHints maps a Jinja statement to its halite equivalent, where one
// exists.
var jinjaTagHints = map[string]string{
	"if":          "Go templates: {{ if <cond> }} ... {{ else }} ... {{ end }}",
	"elif":        "Go templates: {{ else if <cond> }}",
	"else":        "Go templates: {{ else }}",
	"endif":       "Go templates: {{ end }}",
	"for":         "Go templates: {{ range $item := .Pillar.list }} ... {{ end }}",
	"endfor":      "Go templates: {{ end }}",
	"set":         "Go templates: {{ $name := <value> }}, scoped to the rest of the template",
	"macro":       "macros have no equivalent; repeat the states or move the varying part into pillar",
	"import":      "template imports are not supported; SLS 'include:' composes at the state level",
	"from":        "template imports are not supported; SLS 'include:' composes at the state level",
	"raw":         "there is no raw block; escape a literal brace pair with {{ \"{{\" }}",
	"load_yaml":   "load the data from the pillar tree and read it as {{ .Pillar.x }}",
	"load_json":   "load the data from the pillar tree and read it as {{ .Pillar.x }}",
	"import_yaml": "load the data from the pillar tree and read it as {{ .Pillar.x }}",
	"import_json": "load the data from the pillar tree and read it as {{ .Pillar.x }}",
}

func blockTagFinding(num int, tag string) Finding {
	name := firstWord(strings.TrimPrefix(strings.TrimSpace(tag), "-"))
	f := Finding{
		Line: num, Severity: SevError, Code: "jinja-block",
		Message: fmt.Sprintf("Jinja statement {%% %s ... %%}", name),
		Hint:    jinjaTagHints[name],
	}
	if f.Hint == "" {
		f.Hint = "halite renders Go text/template; there is no Jinja statement layer"
	}
	return f
}

// jinjaNames are the Salt template globals. Any of them outside a Go field
// reference means the expression is Jinja.
var jinjaNames = map[string]string{
	"salt":    "execution modules cannot be called from a template; precompute the value in pillar or an external module",
	"grains":  "grains are {{ .Grains.<key> }}",
	"pillar":  "pillar is {{ .Pillar.<key> }}",
	"mine":    "the mine is {{ .Mine.<function>.<agent> }}",
	"opts":    "master and minion options are not exposed to templates",
	"saltenv": "halite merges every environment in a top file; there is no saltenv",
	"sls":     "the current SLS name is not exposed to templates",
	"slspath": "sources are resolved relative to the SLS file, so a path variable is not needed",
	"tpldir":  "sources are resolved relative to the SLS file, so a path variable is not needed",
	"tplfile": "sources are resolved relative to the SLS file, so a path variable is not needed",
	"env":     "halite merges every environment in a top file; there is no env",
}

// goTemplateFuncs are the names a pipeline may use: halite's own helpers
// plus the text/template builtins.
var goTemplateFuncs = func() map[string]bool {
	out := map[string]bool{}
	for name := range sls.TemplateFuncs() {
		out[name] = true
	}
	for _, name := range []string{"and", "call", "html", "index", "slice", "js", "len",
		"not", "or", "print", "printf", "println", "urlquery",
		"eq", "ne", "lt", "le", "gt", "ge"} {
		out[name] = true
	}
	return out
}()

// exprFinding inspects one {{ ... }} expression. It reports only what is
// recognisably Jinja: whether the rest is valid Go template syntax is
// settled by rendering the file, which gives an exact error anyway.
func exprFinding(num int, expr string) (Finding, bool) {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(expr), "-"), "-"))
	at := func(code, msg, hint string) (Finding, bool) {
		return Finding{Line: num, Severity: SevError, Code: code,
			Message: msg, Hint: hint}, true
	}
	if name, hint, ok := jinjaGlobal(body); ok {
		return at("jinja-expr", fmt.Sprintf("Jinja expression {{ %s }} uses %s", body, name), hint)
	}
	if name, parens, ok := pipeFilter(body); ok {
		switch {
		case !goTemplateFuncs[name]:
			return at("jinja-filter", fmt.Sprintf("filter %q is not available", name),
				"halite's template functions are: "+funcList())
		case parens:
			return at("jinja-filter", fmt.Sprintf("filter %q is called with parentheses", name),
				fmt.Sprintf("Go templates pass arguments by position: {{ .Grains.x | %s \"arg\" }}", name))
		}
	}
	switch {
	case strings.Contains(body, ".get("):
		return at("jinja-expr", fmt.Sprintf("Jinja expression {{ %s }} calls .get()", body),
			"a missing key renders as an empty value; use {{ .Pillar.x | default \"y\" }}")
	case strings.Contains(body, " is defined"), strings.Contains(body, " is not "):
		return at("jinja-expr", fmt.Sprintf("Jinja test in {{ %s }}", body),
			"Go templates: {{ if .Pillar.x }} ... {{ end }}")
	case strings.Contains(body, " if ") && strings.Contains(body, " else "):
		return at("jinja-expr", fmt.Sprintf("Jinja inline conditional {{ %s }}", body),
			"Go templates: {{ if <cond> }}a{{ else }}b{{ end }}")
	case strings.Contains(body, "~"):
		return at("jinja-expr", fmt.Sprintf("Jinja concatenation {{ %s }}", body),
			"Go templates: {{ printf \"%s-%s\" .Grains.host .Grains.os }}")
	}
	return Finding{}, false
}

// jinjaGlobal finds a Salt template global used as a bare name, ignoring
// field references like {{ .Grains.os }} where the same word may appear.
func jinjaGlobal(body string) (name, hint string, ok bool) {
	for i := 0; i < len(body); i++ {
		if !isWordStart(body[i]) {
			continue
		}
		if i > 0 && (isWordByte(body[i-1]) || body[i-1] == '.' || body[i-1] == '$') {
			continue
		}
		j := i
		for j < len(body) && isWordByte(body[j]) {
			j++
		}
		word := body[i:j]
		if hint, found := jinjaNames[word]; found {
			return word, hint, true
		}
		i = j
	}
	return "", "", false
}

// pipeFilter returns the name applied by the first pipe in an expression,
// and whether it is called with parentheses (Jinja's calling convention).
func pipeFilter(body string) (name string, parens, ok bool) {
	i := strings.Index(body, "|")
	if i < 0 || (i+1 < len(body) && body[i+1] == '|') {
		return "", false, false
	}
	rest := strings.TrimSpace(body[i+1:])
	name = firstWord(rest)
	if name == "" {
		return "", false, false
	}
	return name, strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(rest, name)), "("), true
}

func funcList() string {
	names := make([]string, 0, len(sls.TemplateFuncs()))
	for name := range sls.TemplateFuncs() {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// spans returns the text inside every open/close pair on a line.
func spans(line, open, close string) []string {
	var out []string
	for {
		i := strings.Index(line, open)
		if i < 0 {
			return out
		}
		rest := line[i+len(open):]
		j := strings.Index(rest, close)
		if j < 0 {
			out = append(out, rest)
			return out
		}
		out = append(out, rest[:j])
		line = rest[j+len(close):]
	}
}

// neutralize rewrites an SLS file into something yamlite can parse: Jinja
// statements are dropped, expressions and flow collections become a
// placeholder scalar, block scalars collapse to one, and a state ID that a
// stripped conditional has left declared twice keeps its first block. The
// result is not what Salt would render — it is only enough structure to see
// which states the file declares, which is why reports built from it are
// marked approximate.
func neutralize(src string) string {
	var out []string
	blockScalarIndent := -1
	duplicateIndent := -1
	declared := map[string]bool{}
	for _, raw := range strings.Split(src, "\n") {
		line := strings.ReplaceAll(strings.TrimRight(raw, " \r"), "\t", "  ")
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if strings.TrimSpace(line) == "" {
			continue
		}
		if blockScalarIndent >= 0 {
			if indent > blockScalarIndent {
				continue
			}
			blockScalarIndent = -1
		}
		if duplicateIndent >= 0 {
			if indent > duplicateIndent {
				continue
			}
			duplicateIndent = -1
		}
		line = withoutTemplates(line)
		content := strings.TrimSpace(yamlite.StripComment(line))
		if content == "" || content == "---" || content == "..." || strings.HasPrefix(content, "#!") {
			continue
		}
		body := content
		dash := ""
		if body == "-" || strings.HasPrefix(body, "- ") {
			dash = "- "
			body = strings.TrimSpace(strings.TrimPrefix(body, "-"))
		}
		key, val, hasKV := yamlite.SplitKV(body)
		if !hasKV {
			out = append(out, line)
			continue
		}
		if key == "<<" {
			continue
		}
		// Both branches of a stripped {% if %} declare the same state ID, and
		// yamlite rejects a duplicate key. Keep the first block.
		if indent == 0 && dash == "" {
			if declared[key] {
				duplicateIndent = indent
				continue
			}
			declared[key] = true
		}
		switch {
		case isBlockScalar(val):
			val = `"` + placeholder + `"`
			blockScalarIndent = indent
		case strings.HasPrefix(val, "&"), strings.HasPrefix(val, "!"):
			val = dropFirstToken(val) // keep the value, drop the anchor or tag
		case strings.HasPrefix(val, "*"):
			val = placeholder
		case isFlowCollection(val):
			val = `"` + placeholder + `"`
		}
		rebuilt := strings.Repeat(" ", indent) + dash + key + ":"
		if val != "" {
			rebuilt += " " + val
		}
		out = append(out, rebuilt)
	}
	return strings.Join(out, "\n")
}

// parseNeutralized is the fallback structural pass for a file that does not
// render.
func parseNeutralized(src string) (any, bool) {
	tree, err := yamlite.Parse(neutralize(src))
	if err != nil {
		return nil, false
	}
	return tree, true
}

func replaceSpans(line, open, close, with string) string {
	for {
		i := strings.Index(line, open)
		if i < 0 {
			return line
		}
		j := strings.Index(line[i:], close)
		if j < 0 {
			return line[:i] + with
		}
		line = line[:i] + with + line[i+j+len(close):]
	}
}

// dropFirstToken returns what follows the first space-delimited token.
func dropFirstToken(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return ""
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	for i := 0; i < len(s); i++ {
		if !isWordByte(s[i]) {
			return s[:i]
		}
	}
	return s
}

func isWordStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func isWordByte(c byte) bool {
	return isWordStart(c) || c >= '0' && c <= '9'
}
