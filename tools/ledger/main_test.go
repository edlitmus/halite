package main

import (
	"strings"
	"testing"
)

// A section runs to the next heading at its own level or shallower.
//
// The extent rule is the one place a mistake would be silent and bad: a
// `###` entry owns its `####` subsections, and cutting at the first of
// them would move a heading and leave its body in the middle of somebody
// else's entry. That is worse than the hand editing this tool replaces,
// so it is the first thing checked.
func TestASectionOwnsItsSubsections(t *testing.T) {
	doc := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.1 First
body of first
#### A subsection
still the first section
### 5.2 Second
body of second
## 6. Next chapter
`, "\n"), "\n")

	got := parseSections(doc)
	// A chapter heading -- `## 5. Chapter` -- carries no minor number and
	// is deliberately not a section: only entries are renumbered.
	if len(got) != 2 {
		t.Fatalf("parsed %d sections, want the 2 entries: %v", len(got), got)
	}
	first := got[0]
	if first.label() != "5.1" || first.title != "First" {
		t.Fatalf("the first entry is %s %q", first.label(), first.title)
	}

	// Asserted by content rather than by a line number worked out by
	// hand: the first version of this test expected the wrong index twice
	// and reported the code as broken when it was right.
	body := strings.Join(doc[first.start:first.end], "\n")
	for _, want := range []string{"body of first", "#### A subsection", "still the first section"} {
		if !strings.Contains(body, want) {
			t.Errorf("5.1's extent lost %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Second") {
		t.Errorf("5.1's extent ran into 5.2:\n%s", body)
	}
}

// A citation of 5.14 is not a citation of 5.149.
//
// The renumbering rewrites by label, and a prefix match would turn every
// mention of 5.14 into 5.99 while renumbering 5.149. There are 186
// sections and plenty of such pairs.
func TestALabelDoesNotMatchInsideALongerOne(t *testing.T) {
	re := citationRe("5.14")
	for _, line := range []string{
		"see DIVERGENCE 5.149 for the reason",
		"5.1499",
		"(DIVERGENCE 5.140)",
	} {
		if re.MatchString(line) {
			t.Errorf("citationRe(5.14) matched %q", line)
		}
	}
	for _, line := range []string{
		"DIVERGENCE 5.14.",
		"(DIVERGENCE 5.14)",
		"5.14's own words",
		"in 5.14; the second is",
		"trailing at the end: 5.14",
	} {
		if !re.MatchString(line) {
			t.Errorf("citationRe(5.14) did not match %q", line)
		}
	}
}

// A new section goes at the end of its own chapter, not the end of the
// file.
func TestChapterEndIsTheChaptersEnd(t *testing.T) {
	doc := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.1 First
body

## 6. Next chapter
### 6.1 Something
body
`, "\n"), "\n")

	// Asserted by inserting there and reading the result, because what
	// matters is where the section lands and not what index says so.
	at := chapterEnd(doc, 5)
	if at <= 0 || at > len(doc) {
		t.Fatalf("chapterEnd(5) = %d, out of range for %d lines", at, len(doc))
	}
	inserted := append([]string{}, doc[:at]...)
	inserted = append(inserted, "### 5.2 Added", "added body")
	inserted = append(inserted, doc[at:]...)
	joined := strings.Join(inserted, "\n")

	chapterFive := joined[strings.Index(joined, "## 5."):strings.Index(joined, "## 6.")]
	if !strings.Contains(chapterFive, "### 5.2 Added") {
		t.Errorf("the new section did not land in chapter 5:\n%s", joined)
	}
	if !strings.Contains(chapterFive, "body") {
		t.Errorf("chapter 5 lost its existing body:\n%s", chapterFive)
	}
	if strings.Contains(joined, "added body## 6.") || strings.Contains(joined, "Added\n## 6.") {
		t.Errorf("the chapters ran together with no separator:\n%s", joined)
	}
	// A chapter that is not there puts the section at the end rather than
	// at the start of somebody else's.
	if got := chapterEnd(doc, 9); got != len(doc) {
		t.Errorf("chapterEnd for an absent chapter = %d, want %d", got, len(doc))
	}
}

// headingDepth counts hashes only for a real heading.
func TestHeadingDepthIgnoresWhatIsNotAHeading(t *testing.T) {
	for line, want := range map[string]int{
		"## 5. Chapter":      2,
		"### 5.1 Entry":      3,
		"#### Subsection":    4,
		"#no space":          0,
		"":                   0,
		"text with # inside": 0,
		"#":                  0,
	} {
		if got := headingDepth(line); got != want {
			t.Errorf("headingDepth(%q) = %d, want %d", line, got, want)
		}
	}
}

// A free number in the wrong place still needs moving.
//
// This tool's own first bug, found by running it on its own change: after
// a rebase git had appended the base's new section *after* this branch's,
// leaving 5.151, 5.153, 5.152. Every number was free and unique, so the
// tool reported nothing to do — and the ordering audit rejected the file.
// Position is a separate question from number.
func TestABaseSectionAfterMineCountsAsMisplaced(t *testing.T) {
	doc := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.151 Theirs
### 5.153 Mine
### 5.152 Theirs too, landed last by a rebase
`, "\n"), "\n")
	all := parseSections(doc)
	mine := map[string]bool{"Mine": true}

	if !misplaced(all, mine) {
		t.Error("a base section after mine was not reported as misplaced, which is the " +
			"state that leaves the ledger out of order with every number free")
	}

	// And the ordinary case, where mine is last, is not a move.
	ordered := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.151 Theirs
### 5.152 Theirs too
### 5.153 Mine
`, "\n"), "\n")
	if misplaced(parseSections(ordered), mine) {
		t.Error("a branch whose section is already last was told to move it")
	}

	// A chapter this branch has not touched is not its business, even
	// when that chapter's own numbering looks odd.
	other := strings.Split(strings.TrimPrefix(`
## 4. Another chapter
### 4.9 Theirs
### 4.2 Theirs, out of order already
## 5. Chapter
### 5.153 Mine
`, "\n"), "\n")
	if misplaced(parseSections(other), mine) {
		t.Error("a chapter this branch adds nothing to was reported as misplaced")
	}
}

// buildPlan sees a free number in the wrong place.
//
// The companion to TestABaseSectionAfterMineCountsAsMisplaced, and the
// one that matters: that test exercises `misplaced` directly, so
// disabling its call site in the plan changed nothing it noticed. This
// goes through the decision the tool actually makes.
func TestBuildPlanReportsWorkForAMisplacedSection(t *testing.T) {
	base := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.151 Theirs
### 5.152 Theirs too
`, "\n"), "\n")
	// What a rebase leaves: both sides' additions, the base's landing
	// after this branch's, every number free and unique.
	here := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.151 Theirs
### 5.153 Mine
### 5.152 Theirs too
`, "\n"), "\n")

	p, err := buildPlan(here, base, map[string][]int{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.mine) != 1 || p.mine[0].title != "Mine" {
		t.Fatalf("the branch's sections came out as %v", p.mine)
	}
	if len(p.moves) != 0 {
		t.Errorf("5.153 is free and should need no renumbering; got %v", p.moves)
	}
	if !p.dirty() {
		t.Error("buildPlan reported nothing to do for a section that is correctly " +
			"numbered and in the wrong place, which is the ledger the ordering audit " +
			"rejects")
	}
}

// And a branch whose section is already last and free is left alone.
func TestBuildPlanIsQuietWhenThereIsNothingToDo(t *testing.T) {
	base := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.151 Theirs
`, "\n"), "\n")
	here := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.151 Theirs
### 5.152 Mine
`, "\n"), "\n")

	p, err := buildPlan(here, base, map[string][]int{})
	if err != nil {
		t.Fatal(err)
	}
	if p.dirty() {
		t.Errorf("buildPlan wants to rewrite a branch that is already correct: "+
			"moves=%v misplaced=%v", p.moves, p.misplaced)
	}
}

// A number the base has taken is renumbered to the next free one.
func TestBuildPlanRenumbersACollision(t *testing.T) {
	base := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.151 Theirs
### 5.152 Theirs too
`, "\n"), "\n")
	here := strings.Split(strings.TrimPrefix(`
## 5. Chapter
### 5.151 Theirs
### 5.152 Theirs too
### 5.152 Mine, colliding
`, "\n"), "\n")

	p, err := buildPlan(here, base, map[string][]int{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.moves) != 1 {
		t.Fatalf("expected one renumbering, got %v", p.moves)
	}
	if got := p.moves[0].want; got != "5.153" {
		t.Errorf("the colliding section was given %s, want 5.153", got)
	}
	if p.moves[0].sec.title != "Mine, colliding" {
		t.Errorf("the wrong section was picked: %q", p.moves[0].sec.title)
	}
}

// TestNextFreeCountsThisBranchsOwnEntries is the test the printing version
// could not have. The bug it holds shut was live: `next` read only the
// base's sections, because a number is allocated *against* the base, and so
// on a branch already adding 5.156 it recommended 5.156.
func TestNextFreeCountsThisBranchsOwnEntries(t *testing.T) {
	p := &plan{
		baseMax: map[int]int{5: 155},
		mine: []section{
			{major: 5, minor: 156, title: "first"},
			{major: 5, minor: 157, title: "second"},
		},
	}

	got := nextFree(p)
	if len(got) != 1 {
		t.Fatalf("one chapter has entries, got %d lines: %q", len(got), got)
	}
	if !strings.Contains(got[0], "next free is 5.158") {
		t.Errorf("155 in the base and 156, 157 on the branch means 158 is free:\n%s", got[0])
	}
	// Both, because a branch's second entry is exactly the case that was
	// wrong, and naming one of two is its own small lie.
	for _, want := range []string{"5.156", "5.157"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the line does not say the branch already adds %s:\n%s", want, got[0])
		}
	}
}

// A chapter nobody has touched on this branch should say nothing about the
// branch -- an empty parenthesis reads as a claim that it added something.
func TestNextFreeSaysNothingAboutAnUntouchedChapter(t *testing.T) {
	p := &plan{
		baseMax: map[int]int{4: 13, 5: 155},
		mine:    []section{{major: 5, minor: 156, title: "only in five"}},
	}

	for _, line := range nextFree(p) {
		if strings.HasPrefix(line, "chapter 4:") && strings.Contains(line, "this branch") {
			t.Errorf("chapter 4 has nothing of this branch in it:\n%s", line)
		}
	}
}
