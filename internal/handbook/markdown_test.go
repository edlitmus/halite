package handbook

import (
	"strings"
	"testing"
)

func render(t *testing.T, src string) *Doc {
	t.Helper()
	d, err := Render(src, Options{Name: "t.md"})
	if err != nil {
		t.Fatalf("render: %v\n%s", err, src)
	}
	return d
}

// Each case is a construct the handbook is written in, with the HTML it
// must produce. A renderer that gets a table or a nested list subtly
// wrong would ship a chapter that reads as nonsense in the one place
// nobody looks at the Markdown.
func TestRenderConstructs(t *testing.T) {
	cases := []struct {
		name, src string
		want      []string
		not       []string
	}{
		{"heading and paragraph", "# One\n\nHello **bold** and *em* and `code <x>`.\n",
			[]string{`<h1 id="one">One`, `<strong>bold</strong>`, `<em>em</em>`, `<code>code &lt;x&gt;</code>`}, nil},
		{"duplicate headings get distinct ids", "# A\n\n## Same\n\n## Same\n",
			[]string{`id="same"`, `id="same-1"`}, nil},
		{"a heading's id is GitHub's", "## `halite-hub run`: the old `salt`\n",
			[]string{`id="halite-hub-run-the-old-salt"`}, nil},
		{"nested lists", "- one\n  - inner\n  - inner two\n- two\n",
			[]string{"<li>one\n<ul>", "<li>inner</li>", "<li>two</li>"}, nil},
		{"an ordered list keeps its start", "3. three\n4. four\n", []string{`<ol start="3">`}, nil},
		{"a wrapped sentence is not a list", "it means\n420. halite refuses it\n", []string{"<p>it means\n420. halite refuses it</p>"}, []string{"<ol"}},
		{"a list item with a code fence", "1. Do this:\n\n   ```sh\n   echo hi\n   ```\n\n2. Then that.\n",
			[]string{"<ol>", "echo hi", "<li>Then that.</li>"}, nil},
		{"a table with a pipe in a code span", "| a | b |\n|---|---|\n| `x | y` | z |\n",
			[]string{"<th>a</th>", "<code>x | y</code>", "<td>z</td>"}, nil},
		{"an escaped pipe", "| a |\n|---|\n| p\\|q |\n", []string{"<td>p|q</td>"}, nil},
		{"alignment", "| a | b |\n|:--|--:|\n| 1 | 2 |\n", []string{`text-align:left`, `text-align:right`}, nil},
		{"a fence is not interpreted", "```yaml\nkey: <b>&</b>\n```\n", []string{"key: &lt;b&gt;&amp;&lt;/b&gt;", `class="language-yaml"`}, nil},
		{"a comment is output nowhere", "text\n\n<!-- a note to the author -->\n\nmore\n", nil, []string{"note to the author", "<!--"}},
		{"an admonition", "> **Warning:** do not.\n", []string{`<blockquote class="note warning">`}, nil},
		{"emphasis does not eat identifiers", "the file_roots_dir and snake_case_name\n", []string{"file_roots_dir", "snake_case_name"}, []string{"<em>"}},
		{"an autolink", "see <https://example.com/x>\n", []string{`<a href="https://example.com/x">`}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := render(t, c.src)
			for _, w := range c.want {
				if !strings.Contains(d.HTML, w) {
					t.Errorf("missing %q in:\n%s", w, d.HTML)
				}
			}
			for _, n := range c.not {
				if strings.Contains(d.HTML, n) {
					t.Errorf("unexpected %q in:\n%s", n, d.HTML)
				}
			}
		})
	}
}

// The renderer refuses what it does not understand. A page that silently
// lost a line of raw HTML would be a regression nobody can see.
func TestRenderRefuses(t *testing.T) {
	for name, src := range map[string]string{
		"raw html":                     "# T\n\n<div>hi</div>\n",
		"an unclosed fence":            "# T\n\n```sh\nls\n",
		"an unclosed comment":          "# T\n\n<!-- never\n",
		"text after a comment":         "# T\n\n<!-- index: x --> and text\n",
		"an example with no block":     "# T\n\n<!-- example: a/b.sls -->\n\ntext\n",
		"a placeholder opening a line": "# T\n\n<name> is the node.\n",
	} {
		if _, err := Render(src, Options{Name: "t.md"}); err == nil {
			t.Errorf("%s: rendered without complaint", name)
		}
	}
}

func TestRenderRecordsWhatTheChecksNeed(t *testing.T) {
	src := "# Title\n\n## Sec\n\n<!-- index: idempotency; convergence -->\n\n" +
		"See [other](other.md#x) and `file.managed`.\n\n<!-- example: handbook/a.sls lines=2-3 -->\n```yaml\nb\nc\n```\n"
	d := render(t, src)
	if len(d.Directives) != 2 || d.Directives[0].Name != "index" || d.Directives[0].Section != "sec" {
		t.Errorf("directives: %+v", d.Directives)
	}
	if len(d.Links) != 1 || d.Links[0].Target != "other.md#x" {
		t.Errorf("links: %+v", d.Links)
	}
	if len(d.CodeSpans) != 1 || d.CodeSpans[0].Text != "file.managed" || d.CodeSpans[0].Section != "sec" {
		t.Errorf("code spans: %+v", d.CodeSpans)
	}
	if len(d.Fences) != 1 || d.Fences[0].Example != "handbook/a.sls" || d.Fences[0].ExampleLines != [2]int{2, 3} {
		t.Errorf("fences: %+v", d.Fences)
	}
}

func TestLinkRewrite(t *testing.T) {
	d, err := Render("[x](a.md#b) and [y](https://e.org)\n", Options{Name: "t.md",
		Rewrite: func(s string) string { return "R:" + s }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.HTML, `href="R:a.md#b"`) || !strings.Contains(d.HTML, `href="R:https://e.org"`) {
		t.Errorf("rewrite not applied: %s", d.HTML)
	}
}
