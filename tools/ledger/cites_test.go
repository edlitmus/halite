package main

import (
	"strings"
	"testing"
)

// fakeLedger is the shape the real one has, small enough to reason about:
// two entries whose vocabularies do not overlap, and a subsection.
var fakeLedger = strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 9.327 Webhook ingress: a delivery twice, a sender starved
An anonymous caller could starve the real sender. The rate limit was the first
thing the handler did, from one token bucket per path, before anything was
authenticated, so requests from anybody who knew the path took the allowance
and the signed deliveries were refused.
### 9.336 mount spoke Linux on the BSDs: defaults and -o remount
The mount module passed Linux option strings to the BSDs, where remount is
spelled differently and the defaults keyword does not exist.
#### 9.336.1 And the fstab half of it
The fstab writer had the same assumption.
### 9.340 Something else entirely
Unrelated prose about certificates and expiry windows.
### 9.9 A section whose number also reads as a percentage
Here so that the percentage rule has something to be wrong about: with no
section of this number, an unresolved bare number is excluded anyway and the
rule would pass its test while doing nothing.
`, "\n"), "\n")

func refsOn(t *testing.T, line string) []reference {
	t.Helper()
	titles, _ := ledgerHeadings(fakeLedger)
	return findRefs(line, titles)
}

func labelsOn(t *testing.T, line string) []string {
	t.Helper()
	var out []string
	for _, r := range refsOn(t, line) {
		out = append(out, r.label)
	}
	return out
}

func TestABareNumberNamingASectionIsReported(t *testing.T) {
	// The form that cost 5.665.1 its wrong citation: no parentheses, no
	// DIVERGENCE marker. `TestLedgerDivergenceCitationsResolve` cannot see
	// it, which is the reason this tool reads lines itself.
	got := labelsOn(t, "9.327 is the nearest recorded shape: a shared allowance.")
	if len(got) != 1 || got[0] != "9.327" {
		t.Errorf("reported %v, want just 9.327", got)
	}
}

func TestAMarkedNumberIsReportedEvenWhenItNamesNothing(t *testing.T) {
	// A citation of a section nobody wrote is the other defect, and the
	// reader should meet it in this list rather than in CI.
	// Assembled rather than written out, so this file does not contain a
	// marked citation of a number nobody has used -- which the tool would
	// report, correctly and for ever, at the top of every run. The same
	// trick, for the same reason, is `lexiconAllowMarker` in
	// internal/buildpolicy/lexicon.go, which is "lexicon" + ":allow" so
	// that the lexicon's own code does not match its own escape hatch.
	got := refsOn(t, "// the rc.subr line is "+"DIVERGENCE"+" 9.664.")
	if len(got) != 1 {
		t.Fatalf("reported %d references, want 1: %v", len(got), got)
	}
	if got[0].label != "9.664" || got[0].heading != "" {
		t.Errorf("got %q naming %q, want 9.664 naming nothing", got[0].label, got[0].heading)
	}
	if !got[0].marked {
		t.Error("a DIVERGENCE-prefixed number was not recorded as marked")
	}
}

func TestAnUnmarkedNumberNamingNothingIsNotReported(t *testing.T) {
	// This is what keeps the version strings out. Enumerating the words a
	// version can follow was tried and abandoned: the ledger holds 1,204
	// bare ledger-shaped numbers that name no section, nearly all of them
	// releases.
	for _, line := range []string{
		"the development host is FreeBSD 15.1 on amd64",
		"Ubuntu 24.04.5, kernel 6.18.7",
		"native FreeBSD salt-call 3008.5 on the host",
		"go1.27.4 is what the toolchain pins",
		"at 0.4% per run it would be invisible in three",
	} {
		if got := labelsOn(t, line); got != nil {
			t.Errorf("%q reported %v, want nothing", line, got)
		}
	}
}

func TestAnotherDocumentsNumberingIsNotReported(t *testing.T) {
	// SPEC's chapters overlap the ledger's -- SPEC 2.3 is the lexicon and
	// the ledger's 2.3 is platform modules -- so a SPEC reference would
	// otherwise be reported as a citation of a section it has nothing to
	// do with.
	for _, line := range []string{
		"SPEC 9.327 says every limit is enforced before allocation",
		"section 9.327 of the handbook",
		"chapter 9.327",
		"issue 9.327",
	} {
		if got := labelsOn(t, line); got != nil {
			t.Errorf("%q reported %v, want nothing", line, got)
		}
	}
	// And the ledger's own number on the same line still is.
	got := labelsOn(t, "SPEC 15.3's table, and the ledger's 9.327, disagree")
	if len(got) != 1 || got[0] != "9.327" {
		t.Errorf("reported %v, want just the ledger's 9.327", got)
	}
}

func TestASectionsOwnHeadingIsNotACitationOfItself(t *testing.T) {
	// Without this every entry a branch adds reports as a reference to
	// itself, and the list a person is meant to read fills with noise.
	for _, line := range []string{
		"### 9.327 Webhook ingress: a delivery twice, a sender starved",
		"#### 9.336.1 And the fstab half of it",
	} {
		if got := labelsOn(t, line); got != nil {
			t.Errorf("%q reported %v, want nothing", line, got)
		}
	}
}

func TestASubsectionLabelResolves(t *testing.T) {
	// `parseSections` parses a label into two integers, so it cannot see
	// `N.M.K` at all. This tool used it at first and silently dropped
	// every citation of a subsection.
	got := refsOn(t, "the fstab half is 9.336.1, which says so")
	if len(got) != 1 {
		t.Fatalf("reported %d references, want 1: %v", len(got), got)
	}
	if got[0].heading != "And the fstab half of it" {
		t.Errorf("9.336.1 resolved to %q", got[0].heading)
	}
}

func TestALongerLabelIsNotMatchedInsideIt(t *testing.T) {
	// 9.34 is not a section here and 9.340 is; a prefix match would
	// report the wrong one, or nothing.
	got := labelsOn(t, "and 9.340 is unrelated")
	if len(got) != 1 || got[0] != "9.340" {
		t.Errorf("reported %v, want just 9.340", got)
	}
}

// The ranking's one claim: a citation of the wrong section shares less
// vocabulary with it than the right section does with the same sentence.
//
// This is the whole justification for sorting the list, and it is also the
// reason the tool does not fail on a low score. Measured on the real cases,
// the classes overlap -- the four known-wrong citations score 0, 2, 2 and 1
// against correct sections scoring 3, 6 and 4 -- so the number orders the
// reading and decides nothing.
func TestTheWrongSectionSharesLessVocabularyThanTheRight(t *testing.T) {
	_, bodies := ledgerHeadings(fakeLedger)

	// The sentence as it was actually published in 5.665.1's draft, which
	// cited mount on the BSDs for a starvation hazard.
	claim := `A semaphore of 64 that counted the long-lived subscribe stream would let a
burst of 64 short requests starve the one stream a node cannot operate without,
so the allowance is taken by requests that do not matter.`

	wrong := shared(distinctive(claim), distinctive(bodies["9.336"]))
	right := shared(distinctive(claim), distinctive(bodies["9.327"]))
	if !(wrong < right) {
		t.Errorf("the wrong section scored %d and the right one %d; the ranking claims nothing if that is not an inequality", wrong, right)
	}
	t.Logf("starvation claim: 9.336 (mount) scores %d, 9.327 (a starved sender) scores %d", wrong, right)
}

func TestTheParagraphIsTheRunOfNonBlankLinesAroundTheCitation(t *testing.T) {
	// One extractor for prose and for Go comments, because both are runs
	// of non-blank lines. A citation in the wrong paragraph would be
	// scored against a claim nobody made.
	lines := []string{
		"first paragraph about certificates",
		"and its second line",
		"",
		"// the comment block",
		"// citing 9.327 here",
		"",
		"third paragraph",
	}
	got := paragraphAround(lines, 4)
	if !strings.Contains(got, "the comment block") || !strings.Contains(got, "citing 9.327") {
		t.Errorf("the paragraph is %q", got)
	}
	for _, leaked := range []string{"certificates", "third paragraph"} {
		if strings.Contains(got, leaked) {
			t.Errorf("the paragraph ran into %q:\n%s", leaked, got)
		}
	}
}

func TestTheReportNamesEveryReferenceAndSaysWhatTheLineClaimed(t *testing.T) {
	// The output is the product -- a person reads it -- so its three facts
	// are asserted: where, which section, and what the line said.
	var out strings.Builder
	renderCites(&out, []reference{
		{file: "internal/transport/client.go", line: 214, label: "9.336",
			heading: "mount spoke Linux on the BSDs", overlap: 0,
			saying: "// 9.336 is the same shape: a starved lifeline"},
	}, "origin/main")
	got := out.String()
	for _, want := range []string{
		"internal/transport/client.go:214",
		"9.336",
		"mount spoke Linux on the BSDs",
		"a starved lifeline",
		"overlap 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report lost %q:\n%s", want, got)
		}
	}
}

func TestAnUnresolvedReferenceIsReadFirstAndSaysSo(t *testing.T) {
	// Sorted above every scored reference, because a number naming no
	// section is certainly wrong where a low score is only suspicious.
	var out strings.Builder
	renderCites(&out, []reference{
		{file: "a.go", line: 1, label: "9.10", heading: "A real one", overlap: 0},
		{file: "b.go", line: 2, label: "9.664", overlap: -1, marked: true},
	}, "origin/main")
	got := out.String()
	if !strings.Contains(got, "names no section") {
		t.Errorf("an unresolved reference was not called out:\n%s", got)
	}
	if strings.Index(got, "9.664") > strings.Index(got, "9.10") {
		t.Errorf("the unresolved reference was not read first:\n%s", got)
	}
}

func TestTheReportSaysSoWhenThereIsNothing(t *testing.T) {
	// Silence would read as "the tool is broken", which is the failure
	// mode a reviewer cannot tell from "nothing to check".
	var out strings.Builder
	renderCites(&out, nil, "origin/main")
	if !strings.Contains(out.String(), "no ledger reference") {
		t.Errorf("want an explicit nothing-to-do line, got:\n%s", out.String())
	}
}

func TestCitesFailsWhenItCanFindNoSections(t *testing.T) {
	// The same guard the audits carry: a parser that silently matches
	// nothing reports a clean branch forever.
	p := &plan{lines: []string{"no headings here"}, touched: map[string][]int{}}
	if _, err := p.collectRefs(); err == nil {
		t.Error("an empty ledger produced no error")
	}
}

func TestAPercentageIsNotACitation(t *testing.T) {
	// Found by running the tool on the branch that wrote it: the rate
	// "5.9%" quoted in 5.677 was reported as a citation of the section
	// those digits name. The fixture uses chapter 9, which the ledger does
	// not have, so this file's own labels do not come back as references
	// every time the tool is run -- but a percentage has to be excluded by
	// the rule, not by failing to resolve, which is what the guard below
	// makes sure of.
	titles, _ := ledgerHeadings(fakeLedger)
	if _, ok := titles["9.9"]; !ok {
		t.Fatal("the fixture lost its 9.9; this test would then pass for the wrong reason")
	}
	for _, line := range []string{
		"and also flags 82 of 1,383 citations, 9.9%",
		"five failures in 1200 runs, 9.9% of them",
	} {
		if got := labelsOn(t, line); got != nil {
			t.Errorf("%q reported %v, want nothing", line, got)
		}
	}
	// The same digits cited rather than measured still report.
	if got := labelsOn(t, "the envelope is 9.9, which says so"); len(got) != 1 {
		t.Errorf("a real citation of 9.9 reported %v", got)
	}
}

// The acknowledgement gate. Its tests are the reason the logic is in Go and
// the hook is four lines of shell.
func TestABranchWithNoReferenceIsNotAsked(t *testing.T) {
	// The common push. Asking about an empty list would be the fastest way
	// to teach everybody to use --no-verify.
	if err := requireAck(&strings.Builder{}, nil, "", nil); err != nil {
		t.Errorf("an empty list was refused: %v", err)
	}
}

func TestAnUnresolvedReferenceCannotBeAcknowledged(t *testing.T) {
	// It is not a judgement call: the number names no section. Offering to
	// accept a keystroke for it would be accepting a keystroke for a fact.
	refs := []reference{
		{file: "a.go", line: 7, label: "9.664", overlap: -1, marked: true},
	}
	err := requireAck(&strings.Builder{}, refs, "9.664", strings.NewReader("9.664\n"))
	if err == nil {
		t.Fatal("a dangling reference was acknowledged")
	}
	if !strings.Contains(err.Error(), "a.go:7") || !strings.Contains(err.Error(), "9.664") {
		t.Errorf("the refusal does not name where or what: %v", err)
	}
}

func TestTheFirstLabelIsWhatConfirms(t *testing.T) {
	refs := []reference{
		{file: "a.go", line: 1, label: "9.327", heading: "First", overlap: 0},
		{file: "b.go", line: 2, label: "9.336", heading: "Second", overlap: 5},
	}
	if err := requireAck(&strings.Builder{}, refs, "", strings.NewReader("9.327\n")); err != nil {
		t.Errorf("the right label was refused: %v", err)
	}
	// A second label from further down the list does not confirm: the
	// point is the lowest-scoring one, which is the likeliest mistake.
	if err := requireAck(&strings.Builder{}, refs, "", strings.NewReader("9.336\n")); err == nil {
		t.Error("a label from further down the list was accepted")
	}
	for _, reply := range []string{"y\n", "yes\n", "\n", "9.32\n"} {
		if err := requireAck(&strings.Builder{}, refs, "", strings.NewReader(reply)); err == nil {
			t.Errorf("%q was accepted as an acknowledgement", reply)
		}
	}
}

func TestWhitespaceAroundTheLabelIsForgiven(t *testing.T) {
	// A pasted label carries spaces, and refusing that would teach nothing
	// except that the gate is fussy.
	refs := []reference{{file: "a.go", line: 1, label: "9.327", heading: "First", overlap: 0}}
	if err := requireAck(&strings.Builder{}, refs, "", strings.NewReader("  9.327  \n")); err != nil {
		t.Errorf("a padded label was refused: %v", err)
	}
}

func TestWithNoTerminalTheEnvironmentCarriesIt(t *testing.T) {
	// A scripted push, CI, or an agent. stdin is not available to ask on --
	// git has the refs being pushed on it -- so there has to be another way
	// in that is not "skip the check when nobody is watching".
	refs := []reference{{file: "a.go", line: 1, label: "9.327", heading: "First", overlap: 0}}
	if err := requireAck(&strings.Builder{}, refs, "9.327", nil); err != nil {
		t.Errorf("the right label in %s was refused: %v", ackEnv, err)
	}
	err := requireAck(&strings.Builder{}, refs, "", nil)
	if err == nil {
		t.Fatal("a non-interactive push with no acknowledgement was allowed")
	}
	if !strings.Contains(err.Error(), ackEnv) {
		t.Errorf("the refusal does not say how to acknowledge: %v", err)
	}
	// And a wrong value says so differently from an absent one, because
	// the two mistakes have different fixes.
	err = requireAck(&strings.Builder{}, refs, "9.999", nil)
	if err == nil || !strings.Contains(err.Error(), "9.999") {
		t.Errorf("a wrong %s was not reported as wrong: %v", ackEnv, err)
	}
}

func TestThePromptSaysHowManyThereAre(t *testing.T) {
	// So that a long list is not mistaken for a short one, and so the
	// number in the prompt matches what was printed above it.
	refs := []reference{
		{file: "a.go", line: 1, label: "9.327", heading: "First", overlap: 0},
		{file: "b.go", line: 2, label: "9.336", heading: "Second", overlap: 5},
	}
	var out strings.Builder
	if err := requireAck(&out, refs, "", strings.NewReader("9.327\n")); err != nil {
		t.Fatalf("requireAck: %v", err)
	}
	if !strings.Contains(out.String(), "2 in all") {
		t.Errorf("the prompt does not say how many: %q", out.String())
	}
}
