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
