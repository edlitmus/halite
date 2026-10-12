package handbook

import (
	"fmt"
	"html"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// SiteOptions describes one build of the handbook.
type SiteOptions struct {
	// Out is the directory to write.
	Out string
	// Version and Commit are stamped into every page. They are inputs and
	// not read from the clock, so two builders given the same commit write
	// the same bytes -- SPEC 4.3 requires the release to be reproducible
	// and a page that carried its build time would break that for no one's
	// benefit.
	Version string
	Commit  string
	// RepoURL is where a link to a file that is not in the book goes:
	// https://github.com/edlitmus/halite/blob/<ref>/
	RepoURL string
	Known   []Known
}

// Build writes the handbook as HTML: a page per chapter, the contents,
// the index, and the whole book on one page for searching and printing.
// It returns the names of the files it wrote, sorted.
func Build(bk *Book, o SiteOptions) ([]string, error) {
	if err := os.MkdirAll(o.Out, 0o755); err != nil {
		return nil, err
	}
	var written []string
	write := func(name, body string) error {
		written = append(written, name)
		return os.WriteFile(filepath.Join(o.Out, name), []byte(body), 0o644)
	}

	// The per-chapter edition.
	if err := bk.Load(bk.rewriter(o, false), false); err != nil {
		return nil, err
	}
	idx := BuildIndex(bk, o.Known)
	for i, c := range bk.All {
		var prev, next *Chapter
		if i > 0 {
			prev = bk.All[i-1]
		}
		if i+1 < len(bk.All) {
			next = bk.All[i+1]
		}
		if err := write(c.Out+".html", chapterPage(bk, c, prev, next, o)); err != nil {
			return nil, err
		}
	}
	if err := write("index.html", contentsPage(bk, o)); err != nil {
		return nil, err
	}
	if err := write("genindex.html", indexPage(bk, idx, o, false)); err != nil {
		return nil, err
	}
	if err := write("handbook.css", css); err != nil {
		return nil, err
	}

	// The single-page edition. Rendered again, because the heading ids
	// have to be unique across chapters and the links point at them.
	if err := bk.Load(bk.rewriter(o, true), true); err != nil {
		return nil, err
	}
	idx = BuildIndex(bk, o.Known)
	if err := write("handbook.html", singlePage(bk, idx, o)); err != nil {
		return nil, err
	}
	sort.Strings(written)
	return written, nil
}

// rewriter returns the link rewriting for one chapter.
func (bk *Book) rewriter(o SiteOptions, single bool) func(*Chapter) func(string) string {
	by := bk.bySource()
	return func(from *Chapter) func(string) string {
		return func(target string) string {
			if hasScheme(target) {
				return target
			}
			p, frag, _ := strings.Cut(target, "#")
			if p == "" {
				if single {
					return "#" + from.Out + "--" + frag
				}
				return "#" + frag
			}
			resolved := path.Clean(path.Join(path.Dir(from.Source), p))
			if ch, ok := by[resolved]; ok {
				if single {
					if frag == "" {
						return "#" + ch.Out
					}
					return "#" + ch.Out + "--" + frag
				}
				if frag == "" {
					return ch.Out + ".html"
				}
				return ch.Out + ".html#" + frag
			}
			if o.RepoURL == "" {
				return target
			}
			kind := "blob/"
			if st, err := os.Stat(filepath.Join(bk.Root, filepath.FromSlash(resolved))); err == nil && st.IsDir() {
				kind = "tree/"
			}
			u := strings.TrimSuffix(o.RepoURL, "/")
			// RepoURL is written with /blob/ in it; swap for a directory.
			if kind == "tree/" {
				u = strings.Replace(u, "/blob/", "/tree/", 1)
			}
			u += "/" + resolved
			if frag != "" {
				u += "#" + frag
			}
			return u
		}
	}
}

func hasScheme(t string) bool {
	for _, s := range []string{"http://", "https://", "mailto:"} {
		if strings.HasPrefix(t, s) {
			return true
		}
	}
	return false
}

func esc(s string) string { return html.EscapeString(s) }

func label(c *Chapter) string {
	if c.Number > 0 {
		return fmt.Sprintf("Chapter %d", c.Number)
	}
	return "Appendix"
}

func chapterTitle(c *Chapter) string {
	if c.Doc != nil && c.Doc.Title != "" {
		return c.Doc.Title
	}
	return c.Out
}

func nav(bk *Book, cur *Chapter, prefix string) string {
	var b strings.Builder
	b.WriteString("<nav class=\"toc\" aria-label=\"Handbook contents\">\n")
	b.WriteString("<p class=\"toc-home\"><a href=\"" + prefix + "index.html\">The halite Handbook</a></p>\n")
	for _, p := range bk.Parts {
		b.WriteString("<p class=\"toc-part\">" + esc(p.Title) + "</p>\n<ol>\n")
		for _, c := range p.Chapters {
			cls := ""
			if c == cur {
				cls = ` class="current"`
			}
			num := ""
			if c.Number > 0 {
				num = fmt.Sprintf("%d. ", c.Number)
			}
			fmt.Fprintf(&b, "<li%s><a href=\"%s%s.html\">%s%s</a>", cls, prefix, c.Out, num, esc(chapterTitle(c)))
			if c == cur {
				b.WriteString("<ul>")
				for _, h := range c.Doc.Headings {
					if h.Level == 2 {
						fmt.Fprintf(&b, "<li><a href=\"#%s\">%s</a></li>", h.ID, esc(h.Text))
					}
				}
				b.WriteString("</ul>")
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ol>\n")
	}
	b.WriteString("<p class=\"toc-part\">Everything</p>\n<ol><li><a href=\"" + prefix + "genindex.html\">Index</a></li>" +
		"<li><a href=\"" + prefix + "handbook.html\">The whole book on one page</a></li></ol>\n")
	b.WriteString("</nav>\n")
	return b.String()
}

func shell(title, nav, body, footer string, o SiteOptions) string {
	return "<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n" +
		"<title>" + esc(title) + " · halite Handbook</title>\n" +
		"<link rel=\"stylesheet\" href=\"handbook.css\">\n</head>\n<body>\n" +
		"<div class=\"layout\">\n" + nav + "<main>\n" + body + "\n" + footer + "</main>\n</div>\n</body>\n</html>\n"
}

func footer(o SiteOptions) string {
	v := o.Version
	if v == "" {
		v = "unreleased"
	}
	c := ""
	if o.Commit != "" {
		c = " · commit " + esc(o.Commit)
	}
	return "<footer>The halite Handbook, for halite " + esc(v) + c + ". Built from <code>docs/</code> in the source tree; " +
		"generated chapters are checked against the code on every test run.</footer>\n"
}

func chapterPage(bk *Book, c, prev, next *Chapter, o SiteOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<p class=\"crumb\">%s · %s</p>\n", esc(c.Part), label(c))
	b.WriteString(c.Doc.HTML)
	b.WriteString("<div class=\"pager\">")
	if prev != nil {
		fmt.Fprintf(&b, "<a rel=\"prev\" href=\"%s.html\">← %s</a>", prev.Out, esc(chapterTitle(prev)))
	}
	if next != nil {
		fmt.Fprintf(&b, "<a rel=\"next\" href=\"%s.html\">%s →</a>", next.Out, esc(chapterTitle(next)))
	}
	b.WriteString("</div>\n")
	return shell(chapterTitle(c), nav(bk, c, ""), b.String(), footer(o), o)
}

func contentsPage(bk *Book, o SiteOptions) string {
	var b strings.Builder
	v := o.Version
	if v == "" {
		v = "unreleased"
	}
	b.WriteString("<h1>The halite Handbook</h1>\n")
	fmt.Fprintf(&b, "<p class=\"lede\">For halite %s.</p>\n", esc(v))
	for _, p := range bk.Parts {
		fmt.Fprintf(&b, "<h2>%s</h2>\n<ol class=\"contents\">\n", esc(p.Title))
		for _, c := range p.Chapters {
			num := ""
			if c.Number > 0 {
				num = fmt.Sprintf("<span class=\"num\">%d</span> ", c.Number)
			}
			fmt.Fprintf(&b, "<li>%s<a href=\"%s.html\">%s</a>", num, c.Out, esc(chapterTitle(c)))
			if a := abstractOf(c.Doc); a != "" {
				fmt.Fprintf(&b, "<span class=\"abstract\"> — %s</span>", esc(a))
			}
			b.WriteString("</li>\n")
		}
		b.WriteString("</ol>\n")
	}
	b.WriteString("<p><a href=\"genindex.html\">Index</a> · <a href=\"handbook.html\">The whole book on one page</a></p>\n")
	return shell("Contents", nav(bk, nil, ""), b.String(), footer(o), o)
}

func abstractOf(d *Doc) string {
	for _, dir := range d.Directives {
		if dir.Name == "abstract" {
			return dir.Value
		}
	}
	return ""
}

func indexPage(bk *Book, idx []*Entry, o SiteOptions, single bool) string {
	var b strings.Builder
	b.WriteString("<h1 id=\"genindex\">Index</h1>\n")
	b.WriteString(indexBody(idx, single))
	return shell("Index", nav(bk, nil, ""), b.String(), footer(o), o)
}

func indexBody(idx []*Entry, single bool) string {
	var b strings.Builder
	kind := ""
	letter := ""
	open := false
	for _, e := range idx {
		if e.Kind != kind {
			if open {
				b.WriteString("</dl>\n")
			}
			kind, letter = e.Kind, ""
			fmt.Fprintf(&b, "<h2>%s</h2>\n", esc(indexHeading(kind)))
			b.WriteString("<dl class=\"index\">\n")
			open = true
		}
		l := strings.ToUpper(firstLetter(e.Term))
		if l != letter {
			letter = l
			fmt.Fprintf(&b, "<dt class=\"letter\">%s</dt>\n", esc(l))
		}
		fmt.Fprintf(&b, "<dt>%s</dt><dd>", termHTML(e))
		for i, r := range e.Refs {
			if i > 0 {
				b.WriteString(", ")
			}
			href := r.Chapter.Out + ".html"
			if single {
				href = "#" + r.Chapter.Out
			}
			if r.Section != "" {
				if single {
					href = "#" + r.Section
				} else {
					href += "#" + r.Section
				}
			}
			text := fmt.Sprintf("%s", chapterShort(r.Chapter))
			if r.SectionTitle != "" && r.SectionTitle != chapterTitle(r.Chapter) {
				text += " §" + r.SectionTitle
			}
			fmt.Fprintf(&b, "<a href=\"%s\">%s</a>", href, esc(text))
		}
		b.WriteString("</dd>\n")
	}
	if open {
		b.WriteString("</dl>\n")
	}
	return b.String()
}

func termHTML(e *Entry) string {
	if e.Kind == General {
		return esc(e.Term)
	}
	return "<code>" + esc(e.Term) + "</code>"
}

func chapterShort(c *Chapter) string {
	if c.Number > 0 {
		return fmt.Sprintf("%d. %s", c.Number, chapterTitle(c))
	}
	return chapterTitle(c)
}

func firstLetter(s string) string {
	for _, r := range strings.TrimLeft(s, "-/") {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return string(r)
		}
	}
	return "#"
}

func indexHeading(kind string) string {
	switch kind {
	case General:
		return "Concepts"
	case "Command":
		return "Commands"
	case "Setting":
		return "Configuration settings"
	case "Function":
		return "Module functions"
	case "Event":
		return "Event tags"
	case "Metric":
		return "Metrics"
	case "Package":
		return "Source packages"
	case "Requisite":
		return "Requisites and state options"
	case "Runner":
		return "Hub runners"
	case "Beacon":
		return "Beacons"
	}
	return kind
}

func singlePage(bk *Book, idx []*Entry, o SiteOptions) string {
	var b strings.Builder
	b.WriteString("<h1 class=\"cover\">The halite Handbook</h1>\n")
	v := o.Version
	if v == "" {
		v = "unreleased"
	}
	fmt.Fprintf(&b, "<p class=\"lede\">For halite %s. The whole book on one page, for searching and printing.</p>\n", esc(v))
	b.WriteString("<h2>Contents</h2>\n")
	for _, p := range bk.Parts {
		fmt.Fprintf(&b, "<h3 class=\"contents-part\">%s</h3>\n<ol class=\"contents\">\n", esc(p.Title))
		for _, c := range p.Chapters {
			num := ""
			if c.Number > 0 {
				num = fmt.Sprintf("<span class=\"num\">%d</span> ", c.Number)
			}
			fmt.Fprintf(&b, "<li>%s<a href=\"#%s\">%s</a></li>\n", num, c.Out, esc(chapterTitle(c)))
		}
		b.WriteString("</ol>\n")
	}
	part := ""
	for _, c := range bk.All {
		if c.Part != part {
			part = c.Part
			fmt.Fprintf(&b, "<h2 class=\"part\">%s</h2>\n", esc(part))
		}
		fmt.Fprintf(&b, "<section class=\"chapter\" id=\"%s\">\n<p class=\"crumb\">%s · %s</p>\n%s</section>\n",
			c.Out, esc(c.Part), label(c), c.Doc.HTML)
	}
	b.WriteString("<section class=\"chapter\" id=\"genindex\"><h1>Index</h1>\n" + indexBody(idx, true) + "</section>\n")
	return shell("The whole book", "", b.String(), footer(o), o)
}

const css = `:root{--bg:#fff;--fg:#1c1e21;--muted:#5b6370;--rule:#d9dde3;--code-bg:#f4f5f7;--accent:#0b5cad;--note:#eef5ff;--warn:#fff4e5;--ok:#eefaf0}
@media (prefers-color-scheme:dark){:root{--bg:#14161a;--fg:#e4e6ea;--muted:#9aa3af;--rule:#2d323a;--code-bg:#1d2026;--accent:#6cb2ff;--note:#18222f;--warn:#2b2216;--ok:#17261b}}
*{box-sizing:border-box}
html{font-size:100%}
body{margin:0;background:var(--bg);color:var(--fg);font:1rem/1.6 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
.layout{display:flex;align-items:flex-start;max-width:90rem;margin:0 auto}
nav.toc{flex:0 0 19rem;position:sticky;top:0;max-height:100vh;overflow:auto;padding:1rem 1rem 3rem 1.25rem;border-right:1px solid var(--rule);font-size:.875rem}
nav.toc ol{list-style:none;margin:0 0 .5rem;padding:0}
nav.toc li{margin:.15rem 0}
nav.toc ul{list-style:none;margin:.25rem 0 .5rem .75rem;padding:0 0 0 .6rem;border-left:2px solid var(--rule)}
nav.toc li.current>a{font-weight:600}
nav.toc .toc-part{margin:1rem 0 .25rem;text-transform:uppercase;letter-spacing:.06em;font-size:.72rem;color:var(--muted);font-weight:600}
nav.toc .toc-home{font-weight:700;font-size:1rem;margin:0}
main{flex:1 1 auto;min-width:0;max-width:52rem;padding:1.5rem 1.5rem 4rem}
a{color:var(--accent);text-decoration:none}a:hover{text-decoration:underline}
h1,h2,h3,h4{line-height:1.25;margin:2rem 0 .75rem}
h1{font-size:2rem;margin-top:.5rem}h2{font-size:1.5rem;border-bottom:1px solid var(--rule);padding-bottom:.25rem}h3{font-size:1.2rem}h4{font-size:1.05rem}
.anchor{margin-left:.4rem;opacity:0;font-weight:400}h1:hover .anchor,h2:hover .anchor,h3:hover .anchor,h4:hover .anchor{opacity:.5}
code,pre{font:.875em/1.5 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
code{background:var(--code-bg);padding:.1em .3em;border-radius:3px}
pre{background:var(--code-bg);padding:.8rem 1rem;overflow:auto;border-radius:6px;border:1px solid var(--rule)}
pre code{background:none;padding:0}
.example{margin:1rem 0}.example pre{margin-top:0;border-top-left-radius:0;border-top-right-radius:0}
.example-path{background:var(--rule);padding:.2rem 1rem;font:.75rem ui-monospace,monospace;border-radius:6px 6px 0 0;color:var(--muted)}
.table{overflow-x:auto}table{border-collapse:collapse;margin:1rem 0;font-size:.925rem}
th,td{border:1px solid var(--rule);padding:.4rem .7rem;text-align:left;vertical-align:top}th{background:var(--code-bg)}
blockquote{margin:1rem 0;padding:.1rem 1rem;border-left:4px solid var(--accent);background:var(--note)}
blockquote.warning,blockquote.caution,blockquote.danger,blockquote.not-built,blockquote.not-verified{border-color:#c77700;background:var(--warn)}
blockquote.verified{border-color:#2a8f3d;background:var(--ok)}
hr{border:0;border-top:1px solid var(--rule);margin:2rem 0}
.crumb{color:var(--muted);font-size:.8rem;margin:0;text-transform:uppercase;letter-spacing:.05em}
.lede{font-size:1.15rem;color:var(--muted)}
.pager{display:flex;justify-content:space-between;gap:1rem;margin:3rem 0 1rem;padding-top:1rem;border-top:1px solid var(--rule)}
ol.contents{list-style:none;padding:0}ol.contents li{margin:.35rem 0}ol.contents .num{display:inline-block;min-width:2rem;color:var(--muted)}
.abstract{color:var(--muted)}
dl.index dt{font-weight:500;margin-top:.3rem}dl.index dt.letter{font-size:1.2rem;font-weight:700;margin-top:1.2rem;border-bottom:1px solid var(--rule)}
dl.index dd{margin:0 0 .2rem 1.5rem;font-size:.9rem;color:var(--muted)}
footer{margin-top:4rem;padding-top:1rem;border-top:1px solid var(--rule);color:var(--muted);font-size:.8rem}
h2.part{font-size:1.8rem;margin-top:4rem;border-bottom:2px solid var(--fg)}
section.chapter{margin-top:3rem}
.cover{font-size:2.6rem}
@media (max-width:60rem){.layout{display:block}nav.toc{position:static;max-height:none;border-right:0;border-bottom:1px solid var(--rule)}main{padding:1rem}}
@media print{nav.toc,.pager,.anchor{display:none}.layout{display:block}main{max-width:none;padding:0}pre{white-space:pre-wrap}section.chapter{break-before:page}a{color:inherit}}
`
