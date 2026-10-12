// Package handbook turns the Markdown under docs/ into the handbook that
// ships in every release, and holds that Markdown to the checks that
// make "always up to date" a property of the build rather than an
// intention.
//
// # Why there is a Markdown converter in a project with no dependencies
//
// SPEC 4.2 closes the dependency list, and Markdown is not in the Go
// standard library. The handbook has to be readable in two places that
// never meet: on GitHub, where it is Markdown, and inside a release
// archive on a machine with no network, where it has to be something a
// browser opens. Writing the converter here, for the dialect the
// documentation actually uses, is a few hundred lines; admitting a
// library to render prose would be a SPEC change for a tool that
// ships in no binary.
//
// It is deliberately not CommonMark. It converts the subset the
// handbook is written in -- ATX headings, paragraphs, fenced code,
// nested lists, GFM tables, block quotes, inline code, emphasis, links
// -- and it REFUSES the constructs it does not understand rather than
// guessing: a line of raw HTML, for instance, is an error naming the
// line. A renderer that quietly mangles a page it does not understand is
// the documentation equivalent of a state module that reports success
// without changing anything.
package handbook

import (
	"fmt"
	"html"
	"regexp"
	"strings"
)

// Heading is one heading of a document, with the anchor GitHub would
// give it, so that a link written for one renderer lands in the other.
type Heading struct {
	Level int
	Text  string
	ID    string
	Line  int
}

// Fence is one fenced code block.
type Fence struct {
	Lang string
	Body string
	Line int
	// Example is the path an `example:` directive named for this block,
	// relative to contrib/examples, and Lines the 1-based inclusive range
	// of that file it shows, or zero for the whole file.
	Example      string
	ExampleLines [2]int
	// Section is the id of the heading the block sits under.
	Section string
}

// Directive is an instruction to the build written as an HTML comment,
// invisible on GitHub: `<!-- index: term; other term -->`.
type Directive struct {
	Name    string
	Value   string
	Line    int
	Section string
}

// CodeSpan is one inline `code span`, with the section it appeared in,
// which is what the automatic index is made of.
type CodeSpan struct {
	Text    string
	Section string
}

// Link is one link target, as written.
type Link struct {
	Target string
	Text   string
	Line   int
}

// Doc is a rendered document and what was learned about it on the way.
type Doc struct {
	Source     string
	Title      string
	Headings   []Heading
	Fences     []Fence
	Directives []Directive
	CodeSpans  []CodeSpan
	Links      []Link
	HTML       string
}

// Options controls rendering.
type Options struct {
	// Rewrite maps a link target as written to the one the HTML should
	// carry. Nil leaves them alone.
	Rewrite func(target string) string
	// IDPrefix is put in front of every heading id, which is how the
	// single-page edition keeps two chapters' "Overview" headings apart.
	IDPrefix string
	// Name is used in error messages.
	Name string
}

type renderer struct {
	opts    Options
	doc     *Doc
	ids     map[string]int
	section string
	// pending is the example directive waiting for the next fence.
	pending *Directive
	err     error
	line0   int
}

// Render converts one document.
func Render(src string, opts Options) (*Doc, error) {
	r := &renderer{opts: opts, doc: &Doc{Source: opts.Name}, ids: map[string]int{}}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	var b strings.Builder
	r.blocks(&b, lines, 1)
	if r.err != nil {
		return nil, r.err
	}
	if r.pending != nil {
		return nil, fmt.Errorf("%s:%d: an example: directive is not followed by a code block", opts.Name, r.pending.Line)
	}
	r.doc.HTML = b.String()
	return r.doc, nil
}

func (r *renderer) fail(line int, format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf("%s:%d: %s", r.opts.Name, line, fmt.Sprintf(format, args...))
	}
}

var (
	headingRe   = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	fenceRe     = regexp.MustCompile("^(\\s*)(```+|~~~+)\\s*(.*)$")
	ulRe        = regexp.MustCompile(`^(\s*)([-*+])\s+(.*)$`)
	olRe        = regexp.MustCompile(`^(\s*)(\d{1,9})([.)])\s+(.*)$`)
	tableSepRe  = regexp.MustCompile(`^\s*\|?\s*:?-{1,}:?\s*(\|\s*:?-{1,}:?\s*)*\|?\s*$`)
	commentRe   = regexp.MustCompile(`<!--(.*?)-->`)
	directiveRe = regexp.MustCompile(`^\s*(index|example|abstract|kind)\s*:\s*(.*?)\s*$`)
)

// blocks renders a run of lines. first is the 1-based document line of
// lines[0], so that errors and recorded positions are real.
func (r *renderer) blocks(b *strings.Builder, lines []string, first int) {
	i := 0
	for i < len(lines) && r.err == nil {
		line := lines[i]
		ln := first + i
		trim := strings.TrimSpace(line)

		switch {
		case trim == "":
			i++

		// An HTML comment on a line of its own is a directive or a note
		// to the author, and never output.
		case strings.HasPrefix(trim, "<!--"):
			j := i
			text := lines[j]
			for !strings.Contains(text, "-->") && j+1 < len(lines) {
				j++
				text += "\n" + lines[j]
			}
			if !strings.Contains(text, "-->") {
				r.fail(ln, "an HTML comment is never closed")
				return
			}
			r.comment(text, ln)
			rest := strings.TrimSpace(commentRe.ReplaceAllString(text, ""))
			if rest != "" {
				r.fail(ln, "text follows a comment on the same line; put the comment on a line of its own")
				return
			}
			i = j + 1

		case fenceRe.MatchString(line):
			i = r.fence(b, lines, i, first)

		case headingRe.MatchString(line):
			m := headingRe.FindStringSubmatch(line)
			r.heading(b, len(m[1]), m[2], ln)
			i++

		case isHR(line) && !ulRe.MatchString(line):
			b.WriteString("<hr>\n")
			i++

		case strings.HasPrefix(trim, ">"):
			j := i
			var inner []string
			for j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), ">") {
				t := strings.TrimSpace(lines[j])
				t = strings.TrimPrefix(t, ">")
				t = strings.TrimPrefix(t, " ")
				inner = append(inner, t)
				j++
			}
			class := ""
			if len(inner) > 0 {
				if m := admonitionRe.FindStringSubmatch(inner[0]); m != nil {
					class = ` class="note ` + strings.ReplaceAll(strings.ToLower(m[1]), " ", "-") + `"`
				}
			}
			b.WriteString("<blockquote" + class + ">\n")
			r.blocks(b, inner, ln)
			b.WriteString("</blockquote>\n")
			i = j

		case i+1 < len(lines) && strings.Contains(line, "|") && tableSepRe.MatchString(lines[i+1]) && strings.Contains(lines[i+1], "-"):
			i = r.table(b, lines, i, first)

		case ulRe.MatchString(line) || olRe.MatchString(line):
			i = r.list(b, lines, i, first)

		case strings.HasPrefix(line, "<") && !strings.HasPrefix(trim, "<http") && !strings.HasPrefix(trim, "<mailto"):
			// A line that begins with an angle bracket is raw HTML, or a
			// placeholder like <name> that was meant as text and will not
			// survive a renderer. Either way it is a mistake to find now.
			r.fail(ln, "a line begins with %q: raw HTML is not supported, and a placeholder belongs in a code span", firstWord(trim))
			return

		default:
			// Paragraph: until a blank line or the start of another block.
			j := i
			var para []string
			for j < len(lines) {
				t := strings.TrimSpace(lines[j])
				if t == "" || fenceRe.MatchString(lines[j]) || headingRe.MatchString(lines[j]) ||
					strings.HasPrefix(t, ">") || ulRe.MatchString(lines[j]) || startsList(lines[j]) ||
					(isHR(lines[j]) && j > i) || strings.HasPrefix(t, "<!--") {
					if j > i {
						break
					}
				}
				para = append(para, lines[j])
				j++
			}
			text := strings.Join(para, "\n")
			b.WriteString("<p>" + r.inline(text, ln) + "</p>\n")
			i = j
		}
	}
}

var admonitionRe = regexp.MustCompile(`^\*\*(Note|Warning|Caution|Tip|Danger|Important|Not built|Verified|Not verified):?\*\*`)

func firstWord(s string) string {
	if k := strings.IndexAny(s, " \t"); k > 0 {
		return s[:k]
	}
	return s
}

func (r *renderer) comment(text string, ln int) {
	for _, m := range commentRe.FindAllStringSubmatch(text, -1) {
		d := directiveRe.FindStringSubmatch(m[1])
		if d == nil {
			continue
		}
		dir := Directive{Name: d[1], Value: d[2], Line: ln, Section: r.section}
		r.doc.Directives = append(r.doc.Directives, dir)
		if dir.Name == "example" {
			if r.pending != nil {
				r.fail(ln, "two example: directives with no code block between them")
				return
			}
			r.pending = &dir
		}
	}
}

func (r *renderer) heading(b *strings.Builder, level int, text string, ln int) {
	plain := stripInline(text)
	id := r.opts.IDPrefix + slug(plain)
	if n := r.ids[id]; n > 0 {
		r.ids[id] = n + 1
		id = fmt.Sprintf("%s-%d", id, n)
	} else {
		r.ids[id] = 1
	}
	r.section = id
	r.doc.Headings = append(r.doc.Headings, Heading{Level: level, Text: plain, ID: id, Line: ln})
	if level == 1 && r.doc.Title == "" {
		r.doc.Title = plain
	}
	fmt.Fprintf(b, "<h%d id=\"%s\">%s<a class=\"anchor\" href=\"#%s\" aria-hidden=\"true\">#</a></h%d>\n",
		level, id, r.inline(text, ln), id, level)
}

func (r *renderer) fence(b *strings.Builder, lines []string, i, first int) int {
	m := fenceRe.FindStringSubmatch(lines[i])
	indent, marker, info := len(m[1]), m[2], strings.TrimSpace(m[3])
	ln := first + i
	j := i + 1
	var body []string
	for ; j < len(lines); j++ {
		t := strings.TrimSpace(lines[j])
		if strings.HasPrefix(t, marker[:3]) && strings.Trim(t, marker[:1]) == "" && len(t) >= len(marker) {
			break
		}
		l := lines[j]
		// Remove up to the fence's own indentation.
		k := 0
		for k < indent && k < len(l) && l[k] == ' ' {
			k++
		}
		body = append(body, l[k:])
	}
	if j >= len(lines) {
		r.fail(ln, "a code fence is never closed")
		return len(lines)
	}
	lang := ""
	if info != "" {
		lang = strings.Fields(info)[0]
	}
	f := Fence{Lang: lang, Body: strings.Join(body, "\n"), Line: ln, Section: r.section}
	if r.pending != nil {
		f.Example, f.ExampleLines = parseExample(r.pending.Value)
		if f.Example == "" {
			r.fail(r.pending.Line, "an example: directive must name a path")
		}
		r.pending = nil
	}
	r.doc.Fences = append(r.doc.Fences, f)
	if f.Example != "" {
		fmt.Fprintf(b, "<div class=\"example\"><div class=\"example-path\">%s</div>", html.EscapeString(exampleCaption(f)))
	}
	cls := ""
	if lang != "" {
		cls = ` class="language-` + html.EscapeString(lang) + `"`
	}
	fmt.Fprintf(b, "<pre><code%s>%s\n</code></pre>\n", cls, html.EscapeString(f.Body))
	if f.Example != "" {
		b.WriteString("</div>\n")
	}
	return j + 1
}

// parseExample splits `path lines=3-9` into its parts.
func parseExample(v string) (string, [2]int) {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return "", [2]int{}
	}
	var rng [2]int
	for _, f := range fields[1:] {
		if strings.HasPrefix(f, "lines=") {
			fmt.Sscanf(strings.TrimPrefix(f, "lines="), "%d-%d", &rng[0], &rng[1])
		}
	}
	return fields[0], rng
}

func exampleCaption(f Fence) string {
	c := "contrib/examples/" + f.Example
	if f.ExampleLines[0] > 0 {
		c += fmt.Sprintf(" (lines %d-%d)", f.ExampleLines[0], f.ExampleLines[1])
	}
	return c
}

// splitRow splits a table row into cells. A pipe inside a code span does
// not end a cell, because the handbook writes `a | b` in code and means
// it; an escaped pipe never does.
func splitRow(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	var cells []string
	var cur strings.Builder
	inCode := false
	rs := []rune(row)
	for k := 0; k < len(rs); k++ {
		c := rs[k]
		switch {
		case c == '\\' && k+1 < len(rs) && rs[k+1] == '|':
			cur.WriteRune('|')
			k++
		case c == '`':
			inCode = !inCode
			cur.WriteRune(c)
		case c == '|' && !inCode:
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(c)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" || len(cells) == 0 {
		cells = append(cells, s)
	}
	return cells
}

func (r *renderer) table(b *strings.Builder, lines []string, i, first int) int {
	ln := first + i
	head := splitRow(lines[i])
	seps := splitRow(lines[i+1])
	align := make([]string, len(head))
	for k, s := range seps {
		if k >= len(align) {
			break
		}
		l, rr := strings.HasPrefix(s, ":"), strings.HasSuffix(s, ":")
		switch {
		case l && rr:
			align[k] = "center"
		case rr:
			align[k] = "right"
		case l:
			align[k] = "left"
		}
	}
	if len(seps) != len(head) {
		r.fail(ln, "a table's header has %d cells and its separator %d", len(head), len(seps))
		return len(lines)
	}
	cell := func(tag, text, a string) string {
		st := ""
		if a != "" {
			st = ` style="text-align:` + a + `"`
		}
		return "<" + tag + st + ">" + r.inline(text, ln) + "</" + tag + ">"
	}
	b.WriteString("<div class=\"table\"><table>\n<thead><tr>")
	for k, h := range head {
		b.WriteString(cell("th", h, align[k]))
	}
	b.WriteString("</tr></thead>\n<tbody>\n")
	j := i + 2
	for j < len(lines) && strings.TrimSpace(lines[j]) != "" && strings.Contains(lines[j], "|") {
		cells := splitRow(lines[j])
		b.WriteString("<tr>")
		for k := range head {
			c := ""
			if k < len(cells) {
				c = cells[k]
			}
			b.WriteString(cell("td", c, align[k]))
		}
		b.WriteString("</tr>\n")
		j++
	}
	b.WriteString("</tbody></table></div>\n")
	return j
}

// list renders one list, items and all their nested content.
func (r *renderer) list(b *strings.Builder, lines []string, i, first int) int {
	type item struct {
		lines []string
		at    int
	}
	var items []item
	ordered := false
	var baseIndent int
	start := 1
	j := i
	for j < len(lines) {
		line := lines[j]
		var indent int
		var content string
		var isItem bool
		if m := ulRe.FindStringSubmatch(line); m != nil {
			indent, content, isItem = len(m[1]), m[3], true
			if len(items) == 0 {
				baseIndent = indent
			}
			if isItem && indent == baseIndent && ordered && len(items) > 0 {
				break
			}
		} else if m := olRe.FindStringSubmatch(line); m != nil {
			indent, content, isItem = len(m[1]), m[4], true
			if len(items) == 0 {
				baseIndent = indent
				ordered = true
				fmt.Sscanf(m[2], "%d", &start)
			} else if indent == baseIndent && !ordered {
				break
			}
		}
		if isItem && indent <= baseIndent+1 && indent >= baseIndent {
			// A new item at this list's level. Its content starts on the
			// bullet's line; continuation lines are indented past it.
			items = append(items, item{lines: []string{content}, at: first + j})
			j++
			continue
		}
		if len(items) == 0 {
			break
		}
		trim := strings.TrimSpace(line)
		if trim == "" {
			// A blank line continues the list only if the next non-blank
			// line is indented under it or is another item of this list.
			k := j + 1
			for k < len(lines) && strings.TrimSpace(lines[k]) == "" {
				k++
			}
			if k >= len(lines) {
				break
			}
			ind := leadingSpaces(lines[k])
			_, isUl := matchItem(lines[k])
			if ind > baseIndent || (ind == baseIndent && isUl && sameKind(lines[k], ordered)) {
				cur := &items[len(items)-1]
				cur.lines = append(cur.lines, "")
				j++
				continue
			}
			break
		}
		ind := leadingSpaces(line)
		if ind > baseIndent {
			cur := &items[len(items)-1]
			cur.lines = append(cur.lines, dedent(line, baseIndent+2))
			j++
			continue
		}
		// Unindented text directly after an item line is a lazy
		// continuation of its paragraph, unless it starts a new block.
		if fenceRe.MatchString(line) || headingRe.MatchString(line) || strings.HasPrefix(trim, ">") || strings.HasPrefix(trim, "|") {
			break
		}
		if prev := items[len(items)-1].lines; len(prev) > 0 && prev[len(prev)-1] != "" {
			cur := &items[len(items)-1]
			cur.lines = append(cur.lines, line)
			j++
			continue
		}
		break
	}
	tag := "ul"
	if ordered {
		tag = "ol"
		if start != 1 {
			tag = fmt.Sprintf(`ol start="%d"`, start)
		}
	}
	b.WriteString("<" + tag + ">\n")
	for _, it := range items {
		b.WriteString("<li>")
		inner := trimBlank(it.lines)
		// A "tight" item -- no blank line anywhere in it -- puts its
		// opening text straight in the <li>, with any nested list or
		// code after it, as CommonMark does.
		if !hasBlank(inner) {
			k := 0
			for k < len(inner) && !startsBlock(inner[k], k == 0) {
				k++
			}
			b.WriteString(r.inline(strings.Join(inner[:k], "\n"), it.at))
			if k < len(inner) {
				b.WriteString("\n")
				var ib strings.Builder
				r.blocks(&ib, inner[k:], it.at+k)
				b.WriteString(strings.TrimSpace(ib.String()))
			}
		} else {
			var ib strings.Builder
			r.blocks(&ib, inner, it.at)
			b.WriteString(strings.TrimSpace(ib.String()))
		}
		b.WriteString("</li>\n")
	}
	closing := "ul"
	if ordered {
		closing = "ol"
	}
	b.WriteString("</" + closing + ">\n")
	return j
}

func matchItem(line string) (string, bool) {
	if m := ulRe.FindStringSubmatch(line); m != nil {
		return m[3], true
	}
	if m := olRe.FindStringSubmatch(line); m != nil {
		return m[4], true
	}
	return "", false
}

func sameKind(line string, ordered bool) bool {
	return ordered == olRe.MatchString(line)
}

func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

func dedent(s string, n int) string {
	k := 0
	for k < n && k < len(s) && s[k] == ' ' {
		k++
	}
	return s[k:]
}

func trimBlank(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func hasBlank(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			return true
		}
	}
	return false
}

// startsBlock reports whether a line of a list item begins something
// that is not the item's opening paragraph. The first line never does:
// it is the text after the bullet.
func startsBlock(l string, first bool) bool {
	if first {
		return false
	}
	t := strings.TrimSpace(l)
	return fenceRe.MatchString(l) || ulRe.MatchString(l) || olRe.MatchString(l) ||
		strings.HasPrefix(t, ">") || strings.HasPrefix(t, "<!--") || headingRe.MatchString(l)
}

// slug is GitHub's heading anchor: lower case, spaces to hyphens, and
// everything that is not a letter, digit, hyphen or underscore removed.
func slug(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		switch {
		case c == ' ' || c == '-':
			b.WriteRune('-')
		case c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || c > 127:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// Slug exposes the anchor rule to the checks that resolve links.
func Slug(s string) string { return slug(s) }

var (
	codeSpanRe = regexp.MustCompile("(`+)(.+?)(`+)")
	linkRe     = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)
	autoRe     = regexp.MustCompile(`<(https?://[^>\s]+|mailto:[^>\s]+)>`)
	strongRe   = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	emStarRe   = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*\n]*?)\*($|[^*\w])`)
	emUndRe    = regexp.MustCompile(`(^|[^\w])_([^_\s][^_\n]*?)_($|[^\w])`)
)

// stripInline reduces inline Markdown to its text, for a heading's id and
// for the contents list.
func stripInline(s string) string {
	s = commentRe.ReplaceAllString(s, "")
	s = linkRe.ReplaceAllString(s, "$1")
	s = strings.NewReplacer("**", "", "`", "").Replace(s)
	return strings.TrimSpace(s)
}

// inline renders the inline constructs of one paragraph, and records the
// code spans and links on the way.
func (r *renderer) inline(s string, ln int) string {
	s = commentRe.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)

	// Pull code spans out first so that nothing inside one is touched,
	// then put them back as placeholders that cannot occur in prose.
	var spans []string
	s = codeSpanRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := codeSpanRe.FindStringSubmatch(m)
		text := strings.TrimSpace(sub[2])
		r.doc.CodeSpans = append(r.doc.CodeSpans, CodeSpan{Text: text, Section: r.section})
		spans = append(spans, "<code>"+html.EscapeString(text)+"</code>")
		return fmt.Sprintf("\x00%d\x00", len(spans)-1)
	})

	// Links next: their targets contain characters emphasis would eat.
	var links []string
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := linkRe.FindStringSubmatch(m)
		target := sub[2]
		r.doc.Links = append(r.doc.Links, Link{Target: target, Text: sub[1], Line: ln})
		href := target
		if r.opts.Rewrite != nil {
			href = r.opts.Rewrite(target)
		}
		links = append(links, "<a href=\""+html.EscapeString(href)+"\">"+r.inlineText(sub[1])+"</a>")
		return fmt.Sprintf("\x01%d\x01", len(links)-1)
	})
	s = autoRe.ReplaceAllStringFunc(s, func(m string) string {
		u := autoRe.FindStringSubmatch(m)[1]
		links = append(links, "<a href=\""+html.EscapeString(u)+"\">"+html.EscapeString(u)+"</a>")
		return fmt.Sprintf("\x01%d\x01", len(links)-1)
	})

	s = r.inlineText(s)

	// Restore links, then code spans (a code span may sit in link text).
	for k := len(links) - 1; k >= 0; k-- {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x01%d\x01", k), links[k])
	}
	for k := len(spans) - 1; k >= 0; k-- {
		s = strings.ReplaceAll(s, fmt.Sprintf("\x00%d\x00", k), spans[k])
	}
	return s
}

// inlineText applies emphasis and escaping to text that has no code
// spans or links left in it.
func (r *renderer) inlineText(s string) string {
	s = html.EscapeString(s)
	s = strongRe.ReplaceAllString(s, "<strong>$1</strong>")
	s = emStarRe.ReplaceAllString(s, "$1<em>$2</em>$3")
	s = emUndRe.ReplaceAllString(s, "$1<em>$2</em>$3")
	// A backslash escapes the next punctuation character.
	s = regexp.MustCompile(`\\([\\*_{}\[\]()#+\-.!|<>~`+"`"+`])`).ReplaceAllString(s, "$1")
	// A line ending in two spaces, or a backslash, is a hard break.
	s = strings.ReplaceAll(s, "  \n", "<br>\n")
	return s
}

// isHR reports a thematic break: three or more of one of - * _, with
// nothing else but spaces. RE2 has no backreference to say "the same
// character", which this project knows well.
func isHR(line string) bool {
	t := strings.ReplaceAll(strings.TrimSpace(line), " ", "")
	if len(t) < 3 || leadingSpaces(line) > 3 {
		return false
	}
	c := t[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	return strings.Trim(t, string(c)) == ""
}

// startsList reports whether a line begins an ordered list that is
// allowed to interrupt a paragraph. CommonMark only lets one that starts
// at 1 do so, and the handbook relies on it: "...the mode 0644 means
// 420." wrapped so that a line begins "420." is a sentence.
func startsList(line string) bool {
	m := olRe.FindStringSubmatch(line)
	return m != nil && m[2] == "1"
}
