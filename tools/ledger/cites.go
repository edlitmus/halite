package main

// `ledger cites` prints every ledger reference this branch added, beside the
// heading of the section it names, for a person to read.
//
// # Why this exists
//
// The audits check that a cited number **exists**. Nothing checks that the
// section says what the citing sentence claims, and CLAUDE.md has said so for
// months: "they check a cited number exists, not that it is the one meant".
// The cost of that gap is recorded five times over -- 5.127 to 5.129 cited
// five sections that were about something else; 5.164 cited 5.101 for a lesson
// belonging to 5.20 and then ended by saying the gap was recorded in a
// plan.md note that never existed (5.164.1); and 5.665.1's draft cited 5.336
// and 5.432, which are `mount` on the BSDs and an event stream's wait
// ordering, for a starvation hazard belonging to 5.327.
//
// Every one of those was caught by a person opening the section, which 5.164
// says "takes one command". This is that one command, scoped to the diff, on
// the model of `ledger plan`: print the list and let somebody read it.
//
// # What it does not do
//
// It does not fail, and it is not a test. Two mechanical approaches were
// measured first and both were rejected, which is why this prints rather than
// judges:
//
//   - **Ranking by shared vocabulary cannot decide a citation.** The four
//     known-wrong citations above score 0, 2, 2 and 1 distinctive words shared
//     with the section they named; the correct sections for those same
//     sentences score 3, 6 and 4. A gate at "two or fewer" would catch all
//     four and also flag 82 of the tree's 1,383 good citations. The score is
//     printed and sorted on, because ranking is what it can honestly do.
//   - **Widening the prose audit to bare `N.M` numbers is not viable.** The
//     ledger holds 1,204 bare ledger-shaped numbers that resolve to no
//     section, nearly all of them OS and Salt versions: `24.04`, `15.1`,
//     `3008.3`. A bare number cannot be told from a version string, which is
//     why `TestLedgerDivergenceCitationsResolve` matches only a parenthesised
//     one and why an unparenthesised prose citation is unverifiable by
//     construction. Mark citations; this tool shows you the ones you wrote.
//
// So the answer to "is this the right section?" stays a person reading two
// lines. What is mechanical is putting those two lines in front of them.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// reference is one ledger reference on a line this branch added.
type reference struct {
	file    string
	line    int    // 1-based
	label   string // "5.327"
	marked  bool   // written as "DIVERGENCE 5.327" rather than bare
	heading string // the section's title, or "" when the number names none
	overlap int    // distinctive words shared with the cited section
	saying  string // the citing line, trimmed
}

// refShape is anything shaped like a ledger label. The boundaries are checked
// by hand in findRefs rather than with a lookaround, which Go's regexp does not
// have, so that `5.14` does not match inside `5.149` and `1.27.4` is one
// candidate rather than two. What exactly disqualifies a neighbouring
// character is stated where it is decided, because the first version of that
// rule was wrong in a way this comment would have hidden.
var refShape = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?[a-z]?`)

// notACitation is the word before a number that makes it somebody else's
// numbering. `SPEC 2.3` is the lexicon section and the ledger's 2.3 is
// platform modules; `section 11.6` is SPEC's too. Both would otherwise be
// reported, because the ledger's chapters run 1 to 8 and overlap SPEC's.
//
// Version strings are not in this list on purpose -- `24.04`, `3008.3` and
// `1.27.4` are excluded by naming no section, which is cheaper than trying to
// enumerate the words that can precede a version.
var notACitation = regexp.MustCompile(`(?i)(spec|section|chapter|table|figure|item|issue|phase|rfc|appendix|§)\s*$`)

// headingLine matches a numbered ledger heading, whose own number is not a
// citation of itself. Without this, every section a branch adds reports as a
// reference to itself.
var headingLine = regexp.MustCompile(`^#{2,4} \d+\.\d+`)

// findRefs reports the references on one line.
//
// A number marked `DIVERGENCE` is reported whether or not it resolves, because
// one that resolves to nothing is the other defect and the reader should see
// it here too. An unmarked number is reported only when it names a real
// section: that is what separates `(5.74)` and `5.327 is the nearest shape`
// from `FreeBSD 15.1`, without a list of every word a version can follow.
func findRefs(line string, defined map[string]string) []reference {
	if headingLine.MatchString(line) {
		return nil
	}
	var out []reference
	for _, loc := range refShape.FindAllStringIndex(line, -1) {
		start, end := loc[0], loc[1]
		// A digit on either side means this is part of a longer number.
		// A *dot* only means that when a digit follows it: the first
		// version of this rule rejected a dot outright and so dropped
		// every citation at the end of a sentence -- `DIVERGENCE 5.664.`
		// reported nothing. Caught by the test for the marked form, which
		// is the one case where a full stop is likeliest.
		if start > 0 {
			c := line[start-1]
			if digit(c) || (c == '.' && start > 1 && digit(line[start-2])) {
				continue
			}
		}
		if end < len(line) {
			c := line[end]
			if digit(c) || (c == '.' && end+1 < len(line) && digit(line[end+1])) {
				continue
			}
			// A percentage is a measurement, never a citation. Found by
			// running this tool on the entry that introduces it, where
			// "5.9%" -- the false-positive rate of the gate this project
			// rejected -- was reported as naming 5.9, `What a real Salt
			// tree found`.
			if c == '%' {
				continue
			}
		}
		label := line[start:end]
		before := line[:start]
		marked := strings.HasSuffix(strings.TrimRight(before, " ("), "DIVERGENCE")
		if !marked {
			if notACitation.MatchString(before) {
				continue
			}
			if _, ok := defined[label]; !ok {
				continue
			}
		}
		out = append(out, reference{
			label: label, marked: marked,
			heading: defined[label], saying: strings.TrimSpace(line),
		})
	}
	return out
}

// Stop words, and words this project says so often that sharing one means
// nothing. Both lists exist because the score is a ranking: a citation whose
// only shared word is "node" has told the reader nothing.
var (
	stopWords = words(`the a an and or but if then than that this these those is are was were be
been being it its of to in on at by for with from as not no any every each which what when where
who whose how why so because does did do done has have had will would can could may might must
shall should one two three four five six seven eight nine ten first second same other another both
all none only just also still again more most less least much many few own way thing things
something nothing here there now today after before while during until since about into over under
out up down off said says say saying write written writes wrote read reads reading run runs ran
running make makes made making take takes took taken get gets got given give gives found find
finds finding`)

	projectWords = words(`halite salt spec divergence ledger node nodes hub hubs test tests testing
audit audits code comment comments section sections number numbers cited citation citations
freebsd linux windows darwin macos file files build builds built change changes changed fix fixed
fixes defect host hosts module modules state states which would`)
)

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(s) {
		out[w] = true
	}
	return out
}

var wordShape = regexp.MustCompile(`[a-z][a-z_]{4,}`)

// distinctive is the set of words worth comparing: five letters or more, not a
// stop word, not one of this project's constants.
func distinctive(text string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordShape.FindAllString(strings.ToLower(text), -1) {
		if !stopWords[w] && !projectWords[w] {
			out[w] = true
		}
	}
	return out
}

func digit(c byte) bool { return c >= '0' && c <= '9' }

func shared(a, b map[string]bool) int {
	n := 0
	for w := range a {
		if b[w] {
			n++
		}
	}
	return n
}

// paragraphAround is the contiguous run of non-blank lines holding line i.
//
// One rule for both languages: a prose paragraph and a Go comment block are
// both runs of non-blank lines, so neither needs its own extractor. A citation
// alone on a line between blanks scores against itself only, which is the
// honest answer -- there is no claim beside it to compare.
func paragraphAround(lines []string, i int) string {
	lo, hi := i, i
	for lo > 0 && strings.TrimSpace(lines[lo-1]) != "" {
		lo--
	}
	for hi+1 < len(lines) && strings.TrimSpace(lines[hi+1]) != "" {
		hi++
	}
	return strings.Join(lines[lo:hi+1], "\n")
}

// cmdCites is the mode itself.
func cmdCites(p *plan) error {
	return p.cites(os.Stdout)
}

func (p *plan) cites(w io.Writer) error {
	refs, err := p.collectRefs()
	if err != nil {
		return err
	}
	renderCites(w, refs, baseRef())
	return nil
}

// collectRefs is everything that reads git and the disk.
func (p *plan) collectRefs() ([]reference, error) {
	defined, bodies := ledgerHeadings(p.lines)
	if len(defined) == 0 {
		return nil, fmt.Errorf("no numbered sections found in %s; this tool has stopped working", ledgerPath)
	}

	// A file this branch has not committed yet is not in `git diff`'s
	// output, and a new test or a new module is exactly where a fresh
	// citation lands -- so an untracked file counts entirely as added.
	// Found by running this tool on the branch that introduced it and
	// being told, wrongly, that the branch adds no reference: its own
	// doc comment cites nine sections. `plan` and `renumber` share the
	// same blind spot and it is not fixed here; see 5.677.
	scope := map[string][]int{}
	for f, ns := range p.touched {
		scope[f] = ns
	}
	fresh, err := untrackedFiles()
	if err != nil {
		return nil, err
	}
	for _, f := range fresh {
		if _, already := scope[f]; already {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var all []int
		for i := range strings.Split(string(body), "\n") {
			all = append(all, i+1)
		}
		scope[f] = all
	}

	files := make([]string, 0, len(scope))
	for f := range scope {
		files = append(files, f)
	}
	sort.Strings(files)

	var refs []reference
	for _, file := range files {
		if !strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, ".md") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			continue // deleted on this branch
		}
		lines := strings.Split(string(body), "\n")
		seen := map[int]bool{}
		for _, n := range scope[file] {
			if n-1 < 0 || n-1 >= len(lines) || seen[n] {
				continue
			}
			seen[n] = true
			for _, r := range findRefs(lines[n-1], defined) {
				r.file, r.line = file, n
				if body, ok := bodies[r.label]; ok {
					r.overlap = shared(distinctive(paragraphAround(lines, n-1)), distinctive(body))
				} else {
					r.overlap = -1 // names no section; sorts first
				}
				refs = append(refs, r)
			}
		}
	}

	return refs, nil
}

// renderCites writes the list a person reads. Separated from the gathering so
// that the output has a test of its own: the gathering needs a repository and
// the wording does not.
func renderCites(w io.Writer, refs []reference, base string) {
	// Lowest overlap first, so the likeliest mistake is the first line read.
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].overlap != refs[j].overlap {
			return refs[i].overlap < refs[j].overlap
		}
		if refs[i].file != refs[j].file {
			return refs[i].file < refs[j].file
		}
		return refs[i].line < refs[j].line
	})

	if len(refs) == 0 {
		fmt.Fprintf(w, "this branch adds no ledger reference against %s\n", base)
		return
	}
	fmt.Fprintf(w, "%d ledger reference(s) added against %s, least shared vocabulary first.\n", len(refs), base)
	fmt.Fprintf(w, "Open any you did not read. A low score is a prompt, not a verdict.\n\n")
	for _, r := range refs {
		head := r.heading
		score := fmt.Sprintf("overlap %d", r.overlap)
		if r.overlap < 0 {
			head, score = "** names no section in the ledger **", "unresolved"
		}
		fmt.Fprintf(w, "  %-12s %s:%d\n", score, r.file, r.line)
		fmt.Fprintf(w, "  %-12s %s  %s\n", "", r.label, head)
		fmt.Fprintf(w, "  %-12s said: %s\n\n", "", truncate(r.saying, 96))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// subsectionHeading matches every numbered ledger heading, including the
// `#### N.M.K` form.
//
// `parseSections` cannot be reused here. It parses a label into a major and a
// minor integer, because that is what allocating and renumbering need, so a
// three-part number is invisible to it -- and this tool was silently dropping
// citations of 5.164.1 and 5.665.1, the two subsections written this week, when
// it used it. Allocation does not need them and reading does, so this is a
// second, simpler parse rather than a change to the first.
var subsectionHeading = regexp.MustCompile(`^(#{2,4}) (\d+\.\d+(?:\.\d+)?[a-z]?) (.*)$`)

// ledgerHeadings maps every section label to its title and to its full text.
func ledgerHeadings(lines []string) (titles, bodies map[string]string) {
	titles, bodies = map[string]string{}, map[string]string{}
	type mark struct {
		depth int
		label string
		title string
		at    int
	}
	var marks []mark
	for i, line := range lines {
		if m := subsectionHeading.FindStringSubmatch(line); m != nil {
			marks = append(marks, mark{depth: len(m[1]), label: m[2], title: m[3], at: i})
		}
	}
	for i, mk := range marks {
		end := len(lines)
		// A section ends at the next heading of its own depth or shallower,
		// so an entry owns its subsections' text -- the same rule
		// parseSections uses, and for the same reason.
		for _, nxt := range marks[i+1:] {
			if nxt.depth <= mk.depth {
				end = nxt.at
				break
			}
		}
		titles[mk.label] = mk.title
		bodies[mk.label] = mk.title + "\n" + strings.Join(lines[mk.at:end], "\n")
	}
	return titles, bodies
}

// untrackedFiles lists files git does not track yet, honouring .gitignore.
func untrackedFiles() ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git ls-files --others: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	var files []string
	for _, line := range strings.Split(out.String(), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}
