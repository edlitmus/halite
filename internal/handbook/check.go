package handbook

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Problem is one thing wrong with the handbook, worded so that the
// person reading a failed test knows which file to open.
type Problem struct {
	File string
	Line int
	Msg  string
}

func (p Problem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("%s:%d: %s", p.File, p.Line, p.Msg)
	}
	return fmt.Sprintf("%s: %s", p.File, p.Msg)
}

// RunbookSections are the headings a chapter marked `kind: runbook` must
// have. A runbook is read at the worst moment, by someone who did not
// write it, and what they need is the same four things in the same order
// every time: what has to be true first, what to do, how to know it
// worked, and how to get back.
var RunbookSections = []string{"Before you start", "Procedure", "Verify", "If it goes wrong"}

// Check loads the whole book and returns every problem found. It does
// not stop at the first, for the reason the compiler of SPEC 11.2 does
// not: fixing a large tree one error per run is a grind.
func Check(bk *Book) []Problem {
	var ps []Problem
	add := func(file string, line int, format string, a ...any) {
		ps = append(ps, Problem{File: file, Line: line, Msg: fmt.Sprintf(format, a...)})
	}

	if err := bk.Load(nil, false); err != nil {
		// Render stops at its first error per chapter, and says which.
		return []Problem{{File: ContentsFile, Msg: err.Error()}}
	}
	orphans, err := bk.Orphans()
	if err != nil {
		add(ContentsFile, 0, "%v", err)
	}
	for _, o := range orphans {
		add(o, 0, "is under docs/handbook and not listed in %s, so nobody will find it", ContentsFile)
	}

	by := bk.bySource()
	for _, c := range bk.All {
		d := c.Doc
		// A file called 38-scaling.md is chapter 38. The number in the name
		// and the number in the book are two paths that must agree, and the
		// links between chapters are written against the first.
		if base := path.Base(c.Source); len(base) > 3 && base[2] == '-' &&
			base[0] >= '0' && base[0] <= '9' && base[1] >= '0' && base[1] <= '9' &&
			strings.HasPrefix(c.Source, "docs/handbook/") {
			if want := fmt.Sprintf("%02d", c.Number); base[:2] != want && c.Number > 0 {
				add(c.Source, 0, "is chapter %d in %s and its file name says %s", c.Number, ContentsFile, base[:2])
			}
		}
		if len(d.Headings) == 0 || d.Headings[0].Level != 1 {
			add(c.Source, 1, "a chapter begins with its title as a level-one heading")
			continue
		}
		prev := 0
		for _, h := range d.Headings {
			if h.Level == 1 && prev != 0 {
				add(c.Source, h.Line, "a second level-one heading, %q; a chapter has one", h.Text)
			}
			if prev != 0 && h.Level > prev+1 {
				add(c.Source, h.Line, "heading %q skips from level %d to %d", h.Text, prev, h.Level)
			}
			prev = h.Level
		}

		for _, dir := range d.Directives {
			if dir.Name == "index" && strings.Trim(dir.Value, "; ") == "" {
				add(c.Source, dir.Line, "an index: directive with no terms")
			}
			if dir.Name == "kind" && dir.Value != "runbook" && dir.Value != "guide" && dir.Value != "reference" && dir.Value != "tutorial" && dir.Value != "concept" {
				add(c.Source, dir.Line, "unknown kind %q; use concept, tutorial, guide, runbook or reference", dir.Value)
			}
		}
		if kindOf(d) == "runbook" {
			have := map[string]bool{}
			for _, h := range d.Headings {
				if h.Level == 2 {
					have[h.Text] = true
				}
			}
			for _, want := range RunbookSections {
				if !have[want] {
					add(c.Source, 0, "is marked `kind: runbook` and has no %q section; a runbook has %s",
						want, strings.Join(RunbookSections, ", "))
				}
			}
		}

		for _, l := range d.Links {
			if hasScheme(l.Target) {
				continue
			}
			p, frag, _ := strings.Cut(l.Target, "#")
			if p == "" {
				if !hasHeading(d, frag) {
					add(c.Source, l.Line, "links to #%s, which is not a heading in this chapter", frag)
				}
				continue
			}
			resolved := path.Clean(path.Join(path.Dir(c.Source), p))
			st, err := os.Stat(filepath.Join(bk.Root, filepath.FromSlash(resolved)))
			if err != nil {
				add(c.Source, l.Line, "links to %q, which does not exist", l.Target)
				continue
			}
			if ch, ok := by[resolved]; ok {
				if msg := chapterNumberMismatch(l.Text, ch); msg != "" {
					add(c.Source, l.Line, "%s", msg)
				}
			}
			if st.IsDir() || frag == "" {
				continue
			}
			if ch, ok := by[resolved]; ok && frag != "" && !hasHeading(ch.Doc, frag) {
				add(c.Source, l.Line, "links to %s#%s, and that chapter has no such heading", resolved, frag)
			}
		}

		for _, f := range d.Fences {
			if f.Example == "" {
				continue
			}
			if msg := checkExample(bk.Root, f); msg != "" {
				add(c.Source, f.Line, "%s", msg)
			}
		}
	}
	return ps
}

func kindOf(d *Doc) string {
	for _, dir := range d.Directives {
		if dir.Name == "kind" {
			return dir.Value
		}
	}
	return ""
}

func hasHeading(d *Doc, id string) bool {
	for _, h := range d.Headings {
		if h.ID == id || strings.TrimPrefix(h.ID, "") == id {
			return true
		}
	}
	return false
}

// checkExample holds a code block to the file it says it shows.
//
// This is the "two paths that must agree" guard for the handbook's own
// examples. The files under contrib/examples/handbook are compiled, linted
// or applied by tests; a block in a chapter that is a retyped copy of one
// is only as good as the retyping, so it is compared byte for byte
// instead, and a change to the file that is not made in the chapter fails
// here.
func checkExample(root string, f Fence) string {
	file := filepath.Join(root, "contrib", "examples", filepath.FromSlash(f.Example))
	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Sprintf("example %s: %v", f.Example, err)
	}
	want := strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if f.ExampleLines[0] > 0 {
		all := strings.Split(want, "\n")
		a, b := f.ExampleLines[0], f.ExampleLines[1]
		if a < 1 || b < a || b > len(all) {
			return fmt.Sprintf("example %s: lines %d-%d are outside the file, which has %d", f.Example, a, b, len(all))
		}
		want = strings.Join(all[a-1:b], "\n")
	}
	got := strings.TrimRight(f.Body, "\n")
	if got == want {
		return ""
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var g, w string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			w = wl[i]
		}
		if g != w {
			return fmt.Sprintf("the block differs from contrib/examples/%s at its line %d:\n    chapter: %q\n    file:    %q", f.Example, i+1, g, w)
		}
	}
	return "the block differs from contrib/examples/" + f.Example
}

var chapterWordRe = regexp.MustCompile(`(?i)\bchapters? (\d+)\b`)

// chapterNumberMismatch holds a link that names a chapter by number to the
// chapter it points at. The handbook says "see chapter 14" in many places,
// and a number typed into prose is a claim about the manifest that nothing
// else would ever check: insert a chapter and every one after it is wrong,
// silently, in the one place a reader trusts. Two paths that must agree.
func chapterNumberMismatch(text string, ch *Chapter) string {
	text = strings.TrimSpace(text)
	var said string
	if m := chapterWordRe.FindStringSubmatch(text); m != nil {
		said = m[1]
	} else if text != "" && strings.Trim(text, "0123456789") == "" {
		said = text
	} else {
		return ""
	}
	if ch.Number == 0 {
		return fmt.Sprintf("a link says chapter %s but %s is an appendix", said, ch.Source)
	}
	if said != fmt.Sprint(ch.Number) {
		return fmt.Sprintf("a link says chapter %s but %s is chapter %d", said, ch.Source, ch.Number)
	}
	return ""
}
