package handbook

import (
	"sort"
	"strings"
)

// Known is a set of identifiers of one kind that the index recognises
// when it sees one in a code span: every command, every setting, every
// module function, every event tag.
//
// The sets are handed in by the caller from the code itself, which is the
// point. An index someone maintains by hand is an index of what they
// remembered to add; one made from the build's own registries indexes
// what the build ships, and a function added tomorrow is indexed the day
// a chapter mentions it.
type Known struct {
	Kind  string
	Names []string
	// Prefix means a code span matches when it begins with the name
	// followed by a space, which is how a command with arguments is
	// recognised: `halite-hub keys accept web1` indexes under
	// `halite-hub keys accept`.
	Prefix bool
}

// Ref is one place a term is found.
type Ref struct {
	Chapter      *Chapter
	Section      string
	SectionTitle string
}

// Entry is one term of the index and everywhere it appears.
type Entry struct {
	Term string
	Kind string
	Refs []Ref
}

// General is the kind of an entry the authors asked for with an `index:`
// directive, as opposed to one the build recognised.
const General = "Concepts"

// BuildIndex collects the index of a loaded book.
func BuildIndex(bk *Book, known []Known) []*Entry {
	type key struct{ kind, term string }
	entries := map[key]*Entry{}
	seen := map[string]bool{}

	add := func(kind, term string, c *Chapter, section string) {
		k := key{kind, term}
		e := entries[k]
		if e == nil {
			e = &Entry{Term: term, Kind: kind}
			entries[k] = e
		}
		id := kind + "\x00" + term + "\x00" + c.Source + "\x00" + section
		if seen[id] {
			return
		}
		seen[id] = true
		e.Refs = append(e.Refs, Ref{Chapter: c, Section: section, SectionTitle: sectionTitle(c.Doc, section)})
	}

	exact := map[string]string{} // identifier -> kind
	var prefixed []struct{ name, kind string }
	for _, k := range known {
		for _, n := range k.Names {
			if k.Prefix {
				prefixed = append(prefixed, struct{ name, kind string }{n, k.Kind})
				continue
			}
			exact[n] = k.Kind
		}
	}
	// Longest first, so `halite-hub keys token create` is not indexed as
	// `halite-hub keys`.
	sort.Slice(prefixed, func(i, j int) bool { return len(prefixed[i].name) > len(prefixed[j].name) })

	match := func(span string) (string, string, bool) {
		span = strings.TrimSpace(span)
		if k, ok := exact[span]; ok {
			return k, span, true
		}
		if t := strings.TrimSuffix(span, ":"); t != span {
			if k, ok := exact[t]; ok {
				return k, t, true
			}
		}
		for _, p := range prefixed {
			if span == p.name || strings.HasPrefix(span, p.name+" ") {
				return p.kind, p.name, true
			}
		}
		return "", "", false
	}

	for _, c := range bk.All {
		d := c.Doc
		for _, dir := range d.Directives {
			if dir.Name != "index" {
				continue
			}
			for _, t := range strings.Split(dir.Value, ";") {
				if t = strings.TrimSpace(t); t != "" {
					add(General, t, c, dir.Section)
				}
			}
		}
		for _, cs := range d.CodeSpans {
			if kind, term, ok := match(cs.Text); ok {
				add(kind, term, c, cs.Section)
			}
		}
		// A heading that is itself an identifier is where a reference
		// chapter defines it, and the most useful place to point.
		for _, h := range d.Headings {
			if kind, term, ok := match(h.Text); ok {
				add(kind, term, c, h.ID)
			}
		}
	}

	out := make([]*Entry, 0, len(entries))
	for _, e := range entries {
		e.Refs = firstPerChapter(e.Refs)
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return kindOrder(out[i].Kind) < kindOrder(out[j].Kind)
		}
		a, b := sortKey(out[i].Term), sortKey(out[j].Term)
		if a != b {
			return a < b
		}
		return out[i].Term < out[j].Term
	})
	return out
}

// MaxRefs is how many places one index entry lists. A term used on every
// page (halite-hub, say) is not helped by a hundred links; the first few
// places it is explained are the useful ones.
const MaxRefs = 12

// firstPerChapter keeps the first reference in each chapter, in reading
// order, and at most MaxRefs of them. The index points at where a term is
// met, and a chapter is the unit a reader turns to.
func firstPerChapter(refs []Ref) []Ref {
	seen := map[*Chapter]bool{}
	var out []Ref
	for _, r := range refs {
		if seen[r.Chapter] {
			continue
		}
		seen[r.Chapter] = true
		out = append(out, r)
		if len(out) == MaxRefs {
			break
		}
	}
	return out
}

// sortKey orders a term as a reader looks for it: without leading hyphens
// (so --clean sorts with clean), case-folded.
func sortKey(term string) string {
	return strings.ToLower(strings.TrimLeft(term, "-/"))
}

func kindOrder(k string) string {
	if k == General {
		return "0"
	}
	return "1" + k
}

func sectionTitle(d *Doc, id string) string {
	for _, h := range d.Headings {
		if h.ID == id {
			return h.Text
		}
	}
	return ""
}
