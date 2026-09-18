package specaudit

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Two branches appending to the ledger both take the next number, and
// whichever merges second is wrong.
//
// That is not hypothetical: it happened while a conformance harness and
// a grains differential were in flight at once, and both wrote 5.82. The
// files do not touch, so the merge was clean; the citation check reads
// headings into a map, so a duplicate was invisible to it; and the whole
// suite passed with two sections of the same number. A reference to
// "5.82" then meant whichever one the reader found first.
//
// Numbers are unique and ascending here, so the second branch has to
// notice.
func TestLedgerSectionNumbersAreUniqueAndOrdered(t *testing.T) {
	ledger := repoFile(t, ledgerFile)

	heading := regexp.MustCompile(`(?m)^(#{2,4}) (\d+)\.(\d+)([a-z]?) `)
	matches := heading.FindAllStringSubmatch(ledger, -1)
	if len(matches) == 0 {
		t.Fatal("no section headings were found; this check has stopped checking")
	}

	type section struct {
		label  string
		major  int
		minor  int
		suffix string
	}
	seen := map[string]int{}
	var previous section
	var started bool

	for _, m := range matches {
		major, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		minor, err := strconv.Atoi(m[3])
		if err != nil {
			continue
		}
		label := m[2] + "." + m[3] + m[4]

		if count := seen[label]; count > 0 {
			t.Errorf("section %s appears %d times; two changes in flight both took it, "+
				"and a citation of it now means whichever a reader finds first",
				label, count+1)
		}
		seen[label]++

		current := section{label: label, major: major, minor: minor, suffix: m[4]}
		// Ordering holds within a chapter. A new chapter starts over,
		// which is the document's own shape rather than a hole in this.
		if started && current.major == previous.major {
			switch {
			case current.minor < previous.minor:
				t.Errorf("section %s follows %s; the ledger reads in order and a reader "+
					"looking for the newest entry reads the end", current.label, previous.label)
			case current.minor == previous.minor && current.suffix <= previous.suffix:
				t.Errorf("section %s follows %s", current.label, previous.label)
			}
		}
		previous, started = current, true
	}

	t.Logf("checked %d sections", len(matches))
	if !strings.Contains(ledger, "## 6.") {
		t.Error("the ledger has lost its later chapters; this check is reading the wrong file")
	}
}

// A section lives inside the chapter its number names.
//
// The check above holds ordering *within* a chapter, which is the
// document's own shape: chapter 6 starting at 6.1 after chapter 5 ended
// at 5.118 is correct, not a regression. The cost of that allowance is
// that a section filed under the wrong chapter is never compared to
// anything, and so is invisible to it.
//
// That is not hypothetical either. 5.113 was appended to the end of the
// file, six hundred lines past the end of chapter 5 and after chapter
// 8 -- and the ordering check passed it, because the heading before it
// was an 8.x and the majors differ. The entry a reader would look for
// between 5.112 and 5.114 was not there, and the audit that exists to
// keep this document readable in order said nothing.
//
// The first cut of this check said nothing either, which is the more
// useful half of the story. It matched chapters and sections with one
// expression, and `## 8. Suggested order` does not fit the shape a
// section does -- a dot and a space where a section has a dot and a
// digit -- so no chapter heading ever matched, every section was
// skipped for want of a chapter to compare against, and it reported
// "0 misfiled" over a document with a section deliberately misfiled in
// it. Hence the two explicit patterns below, and hence the check that
// chapters were found at all: a count of zero is the failure mode this
// kind of audit has, and it looks exactly like a pass.
func TestLedgerSectionsSitInTheChapterTheyAreNumberedFor(t *testing.T) {
	ledger := repoFile(t, ledgerFile)

	chapterLine := regexp.MustCompile(`^## (\d+)\. `)
	sectionLine := regexp.MustCompile(`^#{3,4} (\d+)\.(\d+[a-z]?) `)

	chapter := ""
	chapters, sections, misfiled := 0, 0, 0
	for _, line := range strings.Split(ledger, "\n") {
		if m := chapterLine.FindStringSubmatch(line); m != nil {
			chapter, chapters = m[1], chapters+1
			continue
		}
		m := sectionLine.FindStringSubmatch(line)
		if m == nil || chapter == "" {
			continue
		}
		sections++
		if m[1] != chapter {
			misfiled++
			t.Errorf("section %s.%s sits under chapter %s; a reader looking for it "+
				"between its neighbours does not find it, and the ordering check "+
				"cannot see it because the majors differ", m[1], m[2], chapter)
		}
	}

	if chapters == 0 || sections == 0 {
		t.Fatalf("found %d chapters and %d sections; this check has stopped checking",
			chapters, sections)
	}
	t.Logf("checked %d sections across %d chapters, %d misfiled", sections, chapters, misfiled)
}
