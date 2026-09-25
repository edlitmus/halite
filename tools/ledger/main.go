// Command ledger allocates docs/DIVERGENCE.md section numbers, and
// renumbers a branch's own sections when main has taken them first.
//
// # Why this exists
//
// A section number is chosen when an entry is written and checked when it
// is merged, so two branches in flight pick the same one and whoever
// merges second renumbers. CLAUDE.md says to renumber yours rather than
// main's, which is right, and says nothing about how — so it was done by
// hand. In one day it was done three times for one change, and the ledger
// has carried a `docs: renumber this branch's ledger sections, which main
// took first` commit since well before that.
//
// Doing it by hand is not merely tedious; it is the kind of edit that
// goes wrong quietly. Renumbering 5.149 to 5.151 means rewriting every
// citation of 5.149 — and main may cite 5.149 too, meaning *its* 5.149,
// which must not move. A blanket substitution corrupts the other
// session's references and nothing checks citation *targets*, only that
// the number exists. The last time, the safe version was a hand-written
// list of twenty-six file-and-line pairs.
//
// This computes that list instead. Git already knows which lines a branch
// added; a citation on a line this branch did not touch belongs to
// somebody else, by construction rather than by judgement.
//
// # What it does
//
//	ledger next        the next free number in each chapter
//	ledger plan        what renumber would change, and nothing else
//	ledger renumber    renumber this branch's sections and their citations
//
// A section is "this branch's" when its title is absent from
// origin/main's copy of the ledger. Titles are matched rather than
// numbers because the number is the thing in flux.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const ledgerPath = "docs/DIVERGENCE.md"

// baseRef is what "somebody else's" means. Overridable because a branch
// is not always cut from origin/main.
func baseRef() string {
	if r := os.Getenv("LEDGER_BASE"); r != "" {
		return r
	}
	return "origin/main"
}

func main() {
	mode := "plan"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "next":
		run(cmdNext)
	case "plan":
		run(func(p *plan) error { return p.report(os.Stdout) })
	case "renumber":
		run(func(p *plan) error {
			if err := p.report(os.Stdout); err != nil {
				return err
			}
			return p.apply()
		})
	default:
		fmt.Fprintf(os.Stderr, "ledger: unknown mode %q\n\nusage: ledger [next|plan|renumber]\n", mode)
		os.Exit(2)
	}
}

func run(f func(*plan) error) {
	p, err := newPlan()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledger: %v\n", err)
		os.Exit(1)
	}
	if err := f(p); err != nil {
		fmt.Fprintf(os.Stderr, "ledger: %v\n", err)
		os.Exit(1)
	}
}

func cmdNext(p *plan) error {
	chapters := make([]int, 0, len(p.baseMax))
	for c := range p.baseMax {
		chapters = append(chapters, c)
	}
	sort.Ints(chapters)
	for _, c := range chapters {
		fmt.Printf("chapter %d: next free is %d.%d\n", c, c, p.baseMax[c]+1)
	}
	return nil
}

// section is one numbered heading in the ledger.
type section struct {
	hashes string // "###", which the audit also checks
	major  int
	minor  int
	suffix string
	title  string
	start  int // line index, 0-based
	end    int // exclusive
}

func (s section) label() string { return fmt.Sprintf("%d.%d%s", s.major, s.minor, s.suffix) }

var headingRe = regexp.MustCompile(`^(#{2,4}) (\d+)\.(\d+)([a-z]?) (.*)$`)

func parseSections(lines []string) []section {
	var out []section
	for i, line := range lines {
		m := headingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		major, err1 := strconv.Atoi(m[2])
		minor, err2 := strconv.Atoi(m[3])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, section{
			hashes: m[1], major: major, minor: minor, suffix: m[4],
			title: m[5], start: i,
		})
	}
	// A section runs to the next heading at its own level or shallower.
	// Not "the next heading of any level": an entry owns its `####`
	// subsections, and cutting at the first of them would move a heading
	// and leave its body behind -- which is the one mistake that would
	// make this tool worse than doing it by hand.
	for i := range out {
		out[i].end = len(lines)
		for j := out[i].start + 1; j < len(lines); j++ {
			depth := headingDepth(lines[j])
			if depth > 0 && depth <= len(out[i].hashes) {
				out[i].end = j
				break
			}
		}
	}
	return out
}

// headingDepth counts the leading hashes of a Markdown heading, or 0.
//
// Deliberately blind to a fenced code block. The ledger quotes shell and
// Go, not Markdown, and a `#` comment inside a fence is indented by a tab
// in this document -- so it is not at column 0 and is not counted.
func headingDepth(line string) int {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n >= len(line) || line[n] != ' ' {
		return 0
	}
	return n
}

// move is one section's renumbering.
type move struct {
	sec  section
	want string // the label it should carry
}

// plan is everything the tool works out before it writes anything.
type plan struct {
	lines   []string
	mine    []section        // sections absent from the base, in file order
	baseMax map[int]int      // chapter to the highest minor the base uses
	moves   []move           // the ones whose number must change
	touched map[string][]int // file to the 1-based lines this branch added
	// misplaced is set when one of this branch's sections has a free
	// number and sits in the wrong place -- which happens on a rebase,
	// because git appends both sides' additions and the base's may land
	// last. The first version of this tool checked only the number, said
	// "nothing to do", and left the ledger reading 5.151, 5.153, 5.152
	// for the ordering audit to reject.
	misplaced bool
}

// newPlan reads the repository and works out what to do.
//
// The reading is here and the deciding is in buildPlan, so that the
// deciding can be tested. That split exists because of a bug it would
// have caught: the position check was written, unit-tested as a free
// function, and then disabling its one call site changed nothing any test
// noticed. A test that exercises a function is not a test that it is
// wired in.
func newPlan() (*plan, error) {
	body, err := os.ReadFile(ledgerPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", ledgerPath, err)
	}
	baseBody, err := gitShow(baseRef() + ":" + ledgerPath)
	if err != nil {
		return nil, fmt.Errorf("reading %s from %s: %w (fetch first, or set LEDGER_BASE)",
			ledgerPath, baseRef(), err)
	}
	touched, err := touchedLines()
	if err != nil {
		return nil, err
	}
	return buildPlan(strings.Split(string(body), "\n"),
		strings.Split(baseBody, "\n"), touched)
}

// buildPlan decides, from the two versions of the ledger and the lines
// this branch touched, what must be renumbered and whether anything must
// move.
func buildPlan(lines, baseLines []string, touched map[string][]int) (*plan, error) {
	here := parseSections(lines)
	if len(here) == 0 {
		return nil, fmt.Errorf("%s has no numbered sections; refusing to guess", ledgerPath)
	}
	base := parseSections(baseLines)

	baseTitles := map[string]bool{}
	baseMax := map[int]int{}
	for _, s := range base {
		baseTitles[s.title] = true
		if s.minor > baseMax[s.major] {
			baseMax[s.major] = s.minor
		}
	}

	p := &plan{lines: lines, baseMax: baseMax, touched: touched}
	for _, s := range here {
		// Matched by title, because the number is what is in dispute.
		if !baseTitles[s.title] {
			p.mine = append(p.mine, s)
		}
	}

	// Consecutive numbers from the base's high-water mark, in the order
	// the sections already appear, so that a branch adding three entries
	// keeps the order its author chose.
	next := map[int]int{}
	for _, s := range p.mine {
		if _, ok := next[s.major]; !ok {
			next[s.major] = baseMax[s.major] + 1
		}
		want := fmt.Sprintf("%d.%d", s.major, next[s.major])
		next[s.major]++
		if want != s.label() {
			p.moves = append(p.moves, move{sec: s, want: want})
		}
	}

	isMine := map[string]bool{}
	for _, s := range p.mine {
		isMine[s.title] = true
	}
	p.misplaced = misplaced(here, isMine)
	return p, nil
}

// misplaced reports whether any of the base's sections sits after one of
// this branch's, within a chapter.
//
// Position is a separate question from number, and missing that was this
// tool's own first bug: a rebase appends both sides' additions and the
// base's may land last, so a branch's section can hold a perfectly free
// number in the wrong place. The tool said "nothing to do" and left the
// ledger reading 5.151, 5.153, 5.152 for the ordering audit to reject.
//
// A free function taking what it needs, rather than a method reading the
// repository, so that the case that escaped can be written down as a
// test.
func misplaced(all []section, isMine map[string]bool) bool {
	lastMine := map[int]int{}
	for _, s := range all {
		if isMine[s.title] && s.start > lastMine[s.major] {
			lastMine[s.major] = s.start
		}
	}
	for _, s := range all {
		if !isMine[s.title] && lastMine[s.major] > 0 && s.start > lastMine[s.major] {
			return true
		}
	}
	return false
}

// dirty reports whether anything needs writing.
func (p *plan) dirty() bool { return len(p.moves) > 0 || p.misplaced }

func (p *plan) report(w *os.File) error {
	if len(p.mine) == 0 {
		fmt.Fprintf(w, "no section in %s is absent from %s; nothing to renumber\n",
			ledgerPath, baseRef())
		return nil
	}
	fmt.Fprintf(w, "sections this branch adds, against %s:\n", baseRef())
	for _, s := range p.mine {
		fmt.Fprintf(w, "  %-8s %s\n", s.label(), s.title)
	}
	if !p.dirty() {
		fmt.Fprintln(w, "\nevery one already has a free number, and sits last in its "+
			"chapter; nothing to do")
		return nil
	}
	if p.misplaced {
		fmt.Fprintln(w, "\nposition: one of these sits before a section of the base's, "+
			"which the ordering audit rejects. Moving it to the end of its chapter.")
	}
	if len(p.moves) > 0 {
		fmt.Fprintln(w, "\nrenumbering:")
	}
	for _, m := range p.moves {
		fmt.Fprintf(w, "  %s -> %s  %s\n", m.sec.label(), m.want, m.sec.title)
	}
	fmt.Fprintln(w, "\ncitations to rewrite, on lines this branch added:")
	total := 0
	for _, m := range p.moves {
		for _, hit := range p.citations(m.sec.label()) {
			fmt.Fprintf(w, "  %s:%d  %s -> %s\n", hit.file, hit.line, m.sec.label(), m.want)
			total++
		}
	}
	if total == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	return nil
}

// hit is one citation this branch added.
type hit struct {
	file string
	line int // 1-based
}

// citationRe matches a label in prose or a comment: "DIVERGENCE 5.149",
// "(DIVERGENCE 5.149)", "5.149's", "in 5.149;". The label is required to
// end at a non-digit so that 5.14 does not match inside 5.149.
func citationRe(label string) *regexp.Regexp {
	return regexp.MustCompile(regexp.QuoteMeta(label) + `([^0-9]|$)`)
}

// citations are the places this branch cites a label.
//
// Only lines this branch added or changed, which is the whole point: main
// may cite the same number meaning its own section, and rewriting that
// would silently repoint somebody else's reference. Nothing in the audits
// would catch it -- `TestLedgerDivergenceCitationsResolve` checks that a
// cited number exists, not that it is the one meant.
func (p *plan) citations(label string) []hit {
	re := citationRe(label)
	var out []hit
	files := make([]string, 0, len(p.touched))
	for f := range p.touched {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			continue // deleted on this branch; nothing to rewrite
		}
		lines := strings.Split(string(body), "\n")
		for _, n := range p.touched[file] {
			if n-1 < 0 || n-1 >= len(lines) {
				continue
			}
			if re.MatchString(lines[n-1]) {
				out = append(out, hit{file: file, line: n})
			}
		}
	}
	return out
}

func (p *plan) apply() error {
	if !p.dirty() {
		return nil
	}

	// Citations first, and in every file including the ledger, while the
	// line numbers still describe the file on disk. Moving sections
	// invalidates them.
	edits := map[string]map[int]struct{ from, to string }{}
	for _, m := range p.moves {
		for _, h := range p.citations(m.sec.label()) {
			if edits[h.file] == nil {
				edits[h.file] = map[int]struct{ from, to string }{}
			}
			// A line citing two renumbered sections is rare and would
			// need both; taking the first keeps this honest by failing
			// the audit rather than half-writing.
			if _, taken := edits[h.file][h.line]; !taken {
				edits[h.file][h.line] = struct{ from, to string }{m.sec.label(), m.want}
			}
		}
	}
	for file, byLine := range edits {
		body, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("reading %s: %w", file, err)
		}
		lines := strings.Split(string(body), "\n")
		for n, e := range byLine {
			re := citationRe(e.from)
			lines[n-1] = re.ReplaceAllString(lines[n-1], e.to+"$1")
		}
		if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", file, err)
		}
		fmt.Printf("rewrote %d citation(s) in %s\n", len(byLine), file)
	}

	// Then the ledger itself: reread, because the citation pass may have
	// changed it.
	body, err := os.ReadFile(ledgerPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(body), "\n")
	if err := renumberAndMove(lines, p.moves, p.mine); err != nil {
		return err
	}
	return nil
}

// renumberAndMove rewrites the headings and places the branch's sections
// at the end of their chapter, so the ledger still reads in order.
//
// Written as one pass over a fresh parse rather than by editing the
// caller's slice: the sections were located before the citation rewrite
// and their line numbers are stale.
func renumberAndMove(lines []string, moves []move, mine []section) error {
	want := map[string]string{} // title to new label
	for _, m := range moves {
		want[m.sec.title] = m.want
	}
	isMine := map[string]bool{}
	for _, s := range mine {
		isMine[s.title] = true
	}

	fresh := parseSections(lines)

	// Lift every section of this branch out, in file order, renumbering
	// as it goes.
	type block struct {
		major int
		body  []string
	}
	var lifted []block
	keep := make([]bool, len(lines))
	for i := range keep {
		keep[i] = true
	}
	for _, s := range fresh {
		if !isMine[s.title] {
			continue
		}
		body := append([]string(nil), lines[s.start:s.end]...)
		if label, ok := want[s.title]; ok {
			body[0] = fmt.Sprintf("%s %s %s", s.hashes, label, s.title)
		}
		// Trailing blank lines travel with the gap, not the section.
		for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
			body = body[:len(body)-1]
		}
		lifted = append(lifted, block{major: s.major, body: body})
		for i := s.start; i < s.end; i++ {
			keep[i] = false
		}
	}
	if len(lifted) == 0 {
		return fmt.Errorf("no section was lifted, yet there was work to do")
	}

	var out []string
	for i, line := range lines {
		if keep[i] {
			out = append(out, line)
		}
	}

	// Re-insert each at the end of its chapter: before the next `## `
	// heading, or at the end of the file.
	for _, b := range lifted {
		at := chapterEnd(out, b.major)
		tail := append([]string(nil), out[at:]...)
		out = append(out[:at], append(append([]string(nil), b.body...), append([]string{"", ""}, tail...)...)...)
	}

	// One trailing newline, however the lifting left it.
	for len(out) > 1 && strings.TrimSpace(out[len(out)-1]) == "" &&
		strings.TrimSpace(out[len(out)-2]) == "" {
		out = out[:len(out)-1]
	}
	return os.WriteFile(ledgerPath, []byte(strings.Join(out, "\n")), 0o644)
}

// chapterEnd is the index at which a new section of a chapter belongs:
// just past that chapter's last line.
func chapterEnd(lines []string, major int) int {
	start := -1
	prefix := fmt.Sprintf("## %d. ", major)
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			start = i
			break
		}
	}
	if start < 0 {
		return len(lines)
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			// Back up over the blank lines that separate chapters.
			j := i
			for j > start+1 && strings.TrimSpace(lines[j-1]) == "" {
				j--
			}
			return j
		}
	}
	return len(lines)
}

func gitShow(ref string) (string, error) {
	cmd := exec.Command("git", "show", ref)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// touchedLines reports, per file, the 1-based lines this branch added or
// changed against the base -- working tree included, because the entry
// being renumbered is usually not committed yet.
//
// Read from `git diff -U0`'s hunk headers rather than by comparing
// content: two identical lines are indistinguishable by content and only
// one of them may be new.
func touchedLines() (map[string][]int, error) {
	cmd := exec.Command("git", "diff", "-U0", baseRef(), "--")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff against %s: %v: %s",
			baseRef(), err, strings.TrimSpace(errb.String()))
	}

	touched := map[string][]int{}
	fileRe := regexp.MustCompile(`^\+\+\+ b/(.*)$`)
	hunkRe := regexp.MustCompile(`^@@ -\S+ \+(\d+)(?:,(\d+))? @@`)
	var file string
	for _, line := range strings.Split(out.String(), "\n") {
		if m := fileRe.FindStringSubmatch(line); m != nil {
			file = m[1]
			continue
		}
		m := hunkRe.FindStringSubmatch(line)
		if m == nil || file == "" {
			continue
		}
		start, _ := strconv.Atoi(m[1])
		count := 1
		if m[2] != "" {
			count, _ = strconv.Atoi(m[2])
		}
		for i := 0; i < count; i++ {
			touched[file] = append(touched[file], start+i)
		}
	}
	return touched, nil
}
