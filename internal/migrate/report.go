package migrate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

// Counts summarises the findings by category and severity, which is the
// effort estimate: a migration can be scoped before it is committed to
// rather than discovered during it.
type Counts struct {
	ByCategory map[Category]int
	BySeverity map[Severity]int
	Blocking   int
	Total      int
}

// Count summarises the report.
func (r *Report) Count() Counts {
	c := Counts{
		ByCategory: map[Category]int{},
		BySeverity: map[Severity]int{},
	}
	for _, f := range r.Findings {
		c.ByCategory[f.Category]++
		c.BySeverity[f.Severity]++
		c.Total++
		if f.Severity == Blocking {
			c.Blocking++
		}
	}
	return c
}

// Clean reports whether the tree has no blocking findings, which is the
// exit criterion of SPEC section 32 phase 6.
func (r *Report) Clean() bool { return r.Count().Blocking == 0 }

// Summary renders the human-readable report.
func (r *Report) Summary() string {
	var b strings.Builder
	counts := r.Count()

	fmt.Fprintf(&b, "Migration report for %s\n", r.Root)
	// Which build asked the questions. A report is read long after it is
	// produced and often beside an older copy of itself, and without this
	// there is no way to tell one from the other: a run whose findings
	// had already been fixed was indistinguishable from a stale file, and
	// working out which cost an afternoon.
	fmt.Fprintf(&b, "  by %s\n", version.Full("halite-hub"))
	fmt.Fprintf(&b, "  %d state files, %d pillar files\n", r.SLSFiles, r.PillarFiles)
	if r.PillarRoot != "" && r.PillarRoot != r.Root {
		fmt.Fprintf(&b, "  pillar read from %s\n", r.PillarRoot)
	} else if r.PillarFiles == 0 {
		fmt.Fprintf(&b, "  no pillar tree found; pass --pillar-root if it is elsewhere\n")
	}

	b.WriteString("\nRenderer inventory\n")
	for _, name := range sortedKeys(r.Renderers) {
		fmt.Fprintf(&b, "  %-24s %d file(s)\n", name, r.Renderers[name])
	}

	if len(r.Modules) > 0 {
		b.WriteString("\nModule usage\n")
		for _, name := range sortedKeys(r.Modules) {
			fmt.Fprintf(&b, "  %-32s %d reference(s)\n", name, r.Modules[name])
		}
	}

	if len(r.CustomModules) > 0 {
		b.WriteString("\nPython extension directories\n")
		for _, d := range r.CustomModules {
			fmt.Fprintf(&b, "  %s\n", d)
		}
	}

	if counts.Total == 0 {
		b.WriteString("\nNo findings. This tree compiles as written.\n")
		// The tree with nothing else wrong is exactly the one whose
		// dependency on cmd_default_shell would otherwise go unsaid.
		b.WriteString(r.shellLineNote())
		return b.String()
	}

	b.WriteString("\nFindings\n")
	for _, sev := range []Severity{Blocking, Review, Note} {
		first := true
		for _, f := range r.Findings {
			if f.Severity != sev {
				continue
			}
			if first {
				fmt.Fprintf(&b, "\n  %s (%d)\n", strings.ToUpper(string(sev)), counts.BySeverity[sev])
				first = false
			}
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			fmt.Fprintf(&b, "    [%s] %s\n      %s\n", f.Category, loc, f.Msg)
			if f.Action != "" {
				fmt.Fprintf(&b, "      -> %s\n", f.Action)
			}
		}
	}

	b.WriteString("\nEffort estimate\n")
	for _, cat := range sortedCategoryKeys(counts.ByCategory) {
		fmt.Fprintf(&b, "  %-16s %d\n", cat, counts.ByCategory[cat])
	}
	fmt.Fprintf(&b, "  %-16s %d\n", "TOTAL", counts.Total)
	fmt.Fprintf(&b, "  %-16s %d\n", "BLOCKING", counts.Blocking)

	b.WriteString(r.shellLineNote())

	if counts.Blocking == 0 {
		b.WriteString("\nNo blocking items. This tree can be applied once the review items are understood.\n")
	}
	return b.String()
}

// JSON renders the machine-readable form, which is what CI consumes so
// that the compatibility target is measured continuously.
func (r *Report) JSON() *value.Map {
	findings := make([]any, 0, len(r.Findings))
	for _, f := range r.Findings {
		m := value.MapOf(
			"category", string(f.Category),
			"severity", string(f.Severity),
			"file", f.File,
		)
		if f.Line > 0 {
			m.Set("line", int64(f.Line))
		}
		if f.Col > 0 {
			m.Set("column", int64(f.Col))
		}
		if f.Subject != "" {
			m.Set("subject", f.Subject)
		}
		m.Set("message", f.Msg)
		if f.Action != "" {
			m.Set("action", f.Action)
		}
		findings = append(findings, m)
	}

	renderers := value.NewMap(len(r.Renderers))
	for _, k := range sortedKeys(r.Renderers) {
		renderers.Set(k, int64(r.Renderers[k]))
	}
	modules := value.NewMap(len(r.Modules))
	for _, k := range sortedKeys(r.Modules) {
		modules.Set(k, int64(r.Modules[k]))
	}
	custom := make([]any, len(r.CustomModules))
	for i, c := range r.CustomModules {
		custom[i] = c
	}

	counts := r.Count()
	byCategory := value.NewMap(len(counts.ByCategory))
	for _, k := range sortedCategoryKeys(counts.ByCategory) {
		byCategory.Set(string(k), int64(counts.ByCategory[k]))
	}
	bySeverity := value.NewMap(3)
	for _, sev := range []Severity{Blocking, Review, Note} {
		bySeverity.Set(string(sev), int64(counts.BySeverity[sev]))
	}

	return value.MapOf(
		"schema", "halite.migrate/1",
		"root", r.Root,
		"state_files", int64(r.SLSFiles),
		"pillar_files", int64(r.PillarFiles),
		"renderers", renderers,
		"modules", modules,
		"python_extension_dirs", custom,
		"findings", findings,
		// Counted whether or not they were reported, so a pipeline
		// reading this can see a tree that depends on
		// cmd_default_shell rather than one that has no work to do.
		"shell_lines", int64(r.ShellLines),
		"cmd_default_shell_assumed", r.DefaultShell,
		"effort", value.MapOf(
			"by_category", byCategory,
			"by_severity", bySeverity,
			"total", int64(counts.Total),
			"blocking", int64(counts.Blocking),
		),
		"clean", counts.Blocking == 0,
	)
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedCategoryKeys(m map[Category]int) []Category {
	out := make([]Category, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// shellLineNote says a tree depends on cmd_default_shell.
//
// A tree audited with the setting has no work to do about its shell
// lines and has acquired a dependency instead. Saying only the first
// half would be the audit reporting a clean tree that stops working the
// day someone turns the setting off.
func (r *Report) shellLineNote() string {
	if !r.DefaultShell || r.ShellLines == 0 {
		return ""
	}
	return fmt.Sprintf("\n%d cmd state(s) name a program with arguments in it. They were not\n"+
		"reported, because cmd_default_shell was assumed: with it on they run as\n"+
		"they stand, and the day it is turned off they stop. Converting them is\n"+
		"how the tree stops depending on it. SPEC section 15.2.\n",
		r.ShellLines)
}
