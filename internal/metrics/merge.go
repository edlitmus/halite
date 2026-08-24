package metrics

import (
	"sort"
	"strings"
)

// Merge combines several expositions into one that is still valid.
//
// Concatenation is not enough. The text format allows one `# HELP` and
// one `# TYPE` per metric name in a document, and two components that
// both expose `halite_build_info` — which is the point of its
// `component` label — produce two of each. A scraper rejects the whole
// body for that, so the failure is total and arrives as "no metrics at
// all" rather than as one duplicated family.
//
// Families come out in name order with a single declaration each,
// followed by every series any part contributed, so the result is
// diffable between two scrapes for the same reason a single registry's
// output is.
func Merge(parts ...string) string {
	type merged struct {
		help, kind string
		series     []string
	}
	families := map[string]*merged{}
	var order []string
	// Lines before any declaration, and comments a part added to
	// explain itself, are kept ahead of everything: a comment saying
	// why a component's metrics are absent is the most important line
	// in the body when it is there.
	var preamble []string

	for _, part := range parts {
		current := ""
		for _, line := range strings.Split(part, "\n") {
			if line == "" {
				continue
			}
			name, kind, help, ok := declaration(line)
			if ok {
				current = name
				f, seen := families[name]
				if !seen {
					f = &merged{}
					families[name] = f
					order = append(order, name)
				}
				if help != "" {
					f.help = help
				}
				if kind != "" {
					f.kind = kind
				}
				continue
			}
			if strings.HasPrefix(line, "#") {
				preamble = append(preamble, line)
				continue
			}
			if current == "" {
				// A series line with no declaration in front of it.
				// Kept, because dropping data to tidy the document
				// would be the wrong trade.
				preamble = append(preamble, line)
				continue
			}
			families[current].series = append(families[current].series, line)
		}
	}

	sort.Strings(order)
	var b strings.Builder
	for _, line := range preamble {
		b.WriteString(line + "\n")
	}
	for _, name := range order {
		f := families[name]
		if f.help != "" {
			b.WriteString("# HELP " + name + " " + f.help + "\n")
		}
		if f.kind != "" {
			b.WriteString("# TYPE " + name + " " + f.kind + "\n")
		}
		for _, line := range f.series {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// declaration reads a `# HELP` or `# TYPE` line.
func declaration(line string) (name, kind, help string, ok bool) {
	if rest, found := strings.CutPrefix(line, "# HELP "); found {
		name, help, _ = strings.Cut(rest, " ")
		return name, "", help, true
	}
	if rest, found := strings.CutPrefix(line, "# TYPE "); found {
		name, kind, _ = strings.Cut(rest, " ")
		return name, kind, "", true
	}
	return "", "", "", false
}
