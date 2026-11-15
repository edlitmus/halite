package specaudit

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/repotree"
)

// ledgerHeading matches a numbered ledger section at either depth:
// "### 5.74 ...", "#### 5.663.1 ..." and the "5.74a" form one chapter
// uses.
var ledgerHeading = regexp.MustCompile(`(?m)^#{2,4} (\d+\.\d+(?:\.\d+)?[a-z]?) `)

// sourceCitation matches the form a Go comment uses, which is always
// spelled out: "DIVERGENCE 5.663.1." A bare "(5.74)" is prose's form and
// is checked by TestLedgerDivergenceCitationsResolve; requiring the word
// here is what keeps a version number, a duration or a SPEC clause from
// being read as a ledger citation.
var sourceCitation = regexp.MustCompile(`DIVERGENCE (\d+\.\d+(?:\.\d+)?[a-z]?)`)

// TestSourceDivergenceCitationsResolve holds every `DIVERGENCE N.M` in
// this project's Go source to a section that exists.
//
// **This audit exists because one of these citations was written into
// thin air and the whole suite passed.** `service_self.go` and its test
// cited `DIVERGENCE 5.664` for the rc.subr multi-pid status line while
// 5.664 was still the next free number in chapter 5 — the section was
// planned, cited, and never written. Nothing caught it: the ledger audit
// reads the ledger, the docs audits read `docs/`, and a number sitting in
// a Go comment is read by nobody. It was found by hand, running
// `tools/ledger next` for an unrelated reason and recognising the number.
//
// A citation is the whole of the argument that something was deliberate.
// The comment above `freebsdRCStatusRunning` explains why the parser
// takes a list of pids and not one, and ends by pointing at the evidence;
// a reader who follows that pointer and finds nothing has been told less
// than if it had never been there, because they now have to work out
// whether the section was renumbered, deleted, or never written.
//
// Only that the number resolves is checked, not that it says what the
// comment claims. 5.153 states that limit for the prose citations in its
// own words -- the audit "checks that a cited number **exists**, not that
// it is the one meant" -- and it is no better here. Writing this audit
// produced an instance immediately: the paragraph above cited 5.661 for
// that sentence, which is about a FreeBSD hub and says nothing of the
// kind, and the number resolved.
func TestSourceDivergenceCitationsResolve(t *testing.T) {
	ledger := repoFile(t, ledgerFile)

	defined := map[string]bool{}
	for _, m := range ledgerHeading.FindAllStringSubmatch(ledger, -1) {
		defined[m[1]] = true
	}
	if len(defined) == 0 {
		t.Fatal("no section headings were found in the ledger; this check has stopped checking")
	}

	root, err := repotree.Root()
	if err != nil {
		t.Fatal(err)
	}

	var checked, files int
	walkErr := repotree.Walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		files++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)

		// This file's own comments name 5.664 and 5.153, which are
		// citations like any other and get checked like any other.
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		body := string(raw)
		for i, line := range strings.Split(body, "\n") {
			for _, m := range sourceCitation.FindAllStringSubmatch(line, -1) {
				checked++
				if !defined[m[1]] {
					t.Errorf("%s:%d cites DIVERGENCE %s, which is not a section in %s",
						rel, i+1, m[1], ledgerFile)
				}
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}

	// The count is asserted because the failure this audit was written
	// for is silence. A regexp that stopped matching, or a walk that
	// stopped entering `internal/`, would pass an empty check.
	if checked < 200 {
		t.Errorf("only %d source citations were checked across %d Go files, which is far fewer than "+
			"this tree holds; the scan has probably stopped finding them", checked, files)
	}
	t.Logf("checked %d source citations in %d Go files against %d ledger sections",
		checked, files, len(defined))
}
