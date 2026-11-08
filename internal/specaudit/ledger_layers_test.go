package specaudit

import (
	"regexp"
	"strings"
	"testing"
)

// 5.2's table is the project's own account of how much of SPEC 31's test
// pyramid exists, and its preamble counts the rows: so many present, so
// many partial, one absent. The counts are what a reader takes away, and
// they are the part that rots, because a row moves in one edit and the
// sentence above it moves in another.
//
// They have rotted three times. Four rows once said "blocked on phase 2"
// long after phase 2 landed; the reproducibility row went on denying a
// second builder for a month after two of them agreed on a published tag
// (5.630); and the edit that fixed the second changed two of the three
// counts, which is exactly the moment a hand-written tally goes stale.
//
// This holds the sentence to the rows. It cannot check that a row is
// *true* — that is a judgement about what somebody has run, and 5.630 is
// the argument for reading rather than trusting it — but a count is
// arithmetic, and arithmetic should not be left to the reader.
func TestLedgerLayerTallyMatchesTheRows(t *testing.T) {
	ledger := repoFile(t, ledgerFile)

	section := ledgerSection(t, ledger, "### 5.2 ")
	verdicts := layerVerdicts(t, section)

	if len(verdicts) == 0 {
		t.Fatal("5.2's table has no rows with a bold verdict; this check has stopped checking")
	}

	counted := map[string]int{}
	for _, verdict := range verdicts {
		counted[verdict]++
	}

	// The heading says how many layers there are, and the preamble says
	// the table covers every layer "beyond the unit layer of 5.1", so the
	// rows are one short of the heading's number by construction.
	if claimed, ok := numberWord(headingLayerCount(t, ledger)); ok {
		if want := len(verdicts) + 1; claimed != want {
			t.Errorf("5.2's heading says %d layers and the table has %d rows plus the unit layer of 5.1, which is %d",
				claimed, len(verdicts), want)
		}
	}

	// The preamble is prose, so a count and the word it counts are often
	// on either side of a line break.
	preamble := regexp.MustCompile(`\s+`).ReplaceAllString(section, " ")

	for _, want := range []struct {
		verdict string
		pattern *regexp.Regexp
	}{
		{"present", regexp.MustCompile(`(\w+) are present`)},
		{"partial", regexp.MustCompile(`(\w+) are partial`)},
		{"absent", regexp.MustCompile(`(\w+) (?:is|are) absent`)},
	} {
		m := want.pattern.FindStringSubmatch(preamble)
		if m == nil {
			t.Errorf("5.2's preamble no longer counts how many layers are %s; the tally it gives "+
				"is what a reader takes away, so it is not optional", want.verdict)
			continue
		}
		claimed, ok := numberWord(m[1])
		if !ok {
			t.Errorf("5.2's preamble says %q layers are %s, which this check cannot read as a number",
				m[1], want.verdict)
			continue
		}
		if claimed != counted[want.verdict] {
			t.Errorf("5.2's preamble says %d layers are %s and %d rows of its table say %s; "+
				"a row moved and the sentence above it did not",
				claimed, want.verdict, counted[want.verdict], want.verdict)
		}
	}
}

// ledgerSection returns one section of the ledger, from its heading to
// the next heading of the same or a higher level.
func ledgerSection(t *testing.T, ledger, heading string) string {
	t.Helper()

	start := strings.Index(ledger, heading)
	if start < 0 {
		t.Fatalf("%s has no section %q; this check is reading nothing", ledgerFile, heading)
	}

	rest := ledger[start+len(heading):]
	next := regexp.MustCompile(`(?m)^#{1,3} `).FindStringIndex(rest)
	if next == nil {
		return rest
	}
	return rest[:next[0]]
}

// layerVerdicts reads the bold verdict out of each row of a layer table:
// `| Fuzz | **present** for three of the eight…` yields "present". A row
// whose status does not open with a bold word is a row this check cannot
// count, and saying so is better than counting it as something else.
func layerVerdicts(t *testing.T, section string) []string {
	t.Helper()

	// `**present.**` and `**present**,` are both written here, so the
	// trailing punctuation can be inside the bold or outside it.
	row := regexp.MustCompile(`(?m)^\| *([^|]+?) *\| *\*\*([a-z]+)[.,]?\*\*`)
	var verdicts []string
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|---") || strings.HasPrefix(line, "| Layer") {
			continue
		}
		m := row.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("5.2 has a row whose status does not begin with a bold verdict, so the tally "+
				"cannot count it: %.60s", line)
			continue
		}
		verdicts = append(verdicts, m[2])
	}
	return verdicts
}

// headingLayerCount returns the number word in 5.2's own heading, which
// is the one count written outside the preamble.
func headingLayerCount(t *testing.T, ledger string) string {
	t.Helper()

	m := regexp.MustCompile(`(?m)^### 5\.2 The (\w+) test layers`).FindStringSubmatch(ledger)
	if m == nil {
		t.Error("5.2's heading no longer names the number of test layers; if that is deliberate, " +
			"this half of the check should go with it")
		return ""
	}
	return m[1]
}

// numberWord reads the small English numbers the ledger writes out. The
// ledger spells these rather than using digits, so a tally check has to
// read the spelling.
func numberWord(word string) (int, bool) {
	words := map[string]int{
		"zero": 0, "no": 0, "one": 1, "two": 2, "three": 3, "four": 4,
		"five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9,
		"ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
		"fourteen": 14, "fifteen": 15, "sixteen": 16,
	}
	n, ok := words[strings.ToLower(strings.TrimSpace(word))]
	return n, ok
}
