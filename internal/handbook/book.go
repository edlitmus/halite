package handbook

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ContentsFile is the manifest: the one place that says what is in the
// handbook and in what order.
const ContentsFile = "docs/handbook/CONTENTS"

// Chapter is one entry of the book.
type Chapter struct {
	// Part is the title of the part it belongs to.
	Part string
	// Number counts chapters from 1 across the whole book. Appendices
	// are numbered by the part they sit in, not here, and carry 0.
	Number int
	// Source is the repository-relative path of the Markdown.
	Source string
	// Out is the base name of the HTML page, without extension.
	Out string
	Doc *Doc
}

// Part is a run of chapters under one title.
type Part struct {
	Title    string
	Chapters []*Chapter
}

// Book is the parsed manifest and everything rendered from it.
type Book struct {
	Root  string
	Parts []*Part
	// All is the chapters in reading order.
	All []*Chapter
}

// ReadContents parses the manifest. Its grammar is two kinds of line:
//
//	part Title of the part
//	path/relative/to/docs/handbook.md
//
// with `#` comments and blank lines ignored. A path may climb out of
// docs/handbook, which is how the pages written before the handbook --
// getting-started, operations, the generated references -- are in it
// without being copied. A copy would be two paths that must agree, and
// this project knows what becomes of those.
func ReadContents(root string) (*Book, error) {
	f, err := os.Open(filepath.Join(root, ContentsFile))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	bk := &Book{Root: root}
	var cur *Part
	sc := bufio.NewScanner(f)
	ln := 0
	seen := map[string]string{}
	n := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if t, ok := strings.CutPrefix(line, "part "); ok {
			cur = &Part{Title: strings.TrimSpace(t)}
			bk.Parts = append(bk.Parts, cur)
			continue
		}
		if cur == nil {
			return nil, fmt.Errorf("%s:%d: %q comes before any `part` line", ContentsFile, ln, line)
		}
		src := path.Clean(path.Join("docs/handbook", line))
		if !strings.HasSuffix(src, ".md") {
			return nil, fmt.Errorf("%s:%d: %q is not a Markdown file", ContentsFile, ln, line)
		}
		out := strings.TrimSuffix(path.Base(src), ".md")
		out = strings.ToLower(out)
		if prev, dup := seen[out]; dup {
			return nil, fmt.Errorf("%s:%d: %s and %s would both be written as %s.html", ContentsFile, ln, prev, src, out)
		}
		seen[out] = src
		ch := &Chapter{Part: cur.Title, Source: src, Out: out}
		if !strings.HasPrefix(strings.ToLower(cur.Title), "appendix") {
			n++
			ch.Number = n
		}
		cur.Chapters = append(cur.Chapters, ch)
		bk.All = append(bk.All, ch)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for _, p := range bk.Parts {
		if len(p.Chapters) == 0 {
			return nil, fmt.Errorf("%s: part %q has no chapters", ContentsFile, p.Title)
		}
	}
	if len(bk.All) == 0 {
		return nil, fmt.Errorf("%s lists nothing", ContentsFile)
	}
	return bk, nil
}

// byOut finds a chapter by the page it is written to.
func (bk *Book) bySource() map[string]*Chapter {
	m := map[string]*Chapter{}
	for _, c := range bk.All {
		m[c.Source] = c
	}
	return m
}

// Load renders every chapter. prefix is "" for the per-chapter pages and
// non-empty for the single-page edition, where heading ids are made
// unique by chapter.
func (bk *Book) Load(rewriteFor func(from *Chapter) func(string) string, singlePage bool) error {
	for _, c := range bk.All {
		data, err := os.ReadFile(filepath.Join(bk.Root, filepath.FromSlash(c.Source)))
		if err != nil {
			return fmt.Errorf("%s (listed in %s): %w", c.Source, ContentsFile, err)
		}
		opts := Options{Name: c.Source}
		if singlePage {
			opts.IDPrefix = c.Out + "--"
		}
		if rewriteFor != nil {
			opts.Rewrite = rewriteFor(c)
		}
		doc, err := Render(string(data), opts)
		if err != nil {
			return err
		}
		c.Doc = doc
	}
	return nil
}

// Orphans lists Markdown files under docs/handbook that the manifest
// does not name. A chapter nobody listed is a chapter nobody reads, and
// the README of the directory is the one exception.
func (bk *Book) Orphans() ([]string, error) {
	listed := bk.bySource()
	var out []string
	dir := filepath.Join(bk.Root, "docs", "handbook")
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(bk.Root, p)
		rel = filepath.ToSlash(rel)
		if rel == "docs/handbook/README.md" {
			return nil
		}
		if _, ok := listed[rel]; !ok {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
