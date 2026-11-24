package main

import (
	"strings"
	"testing"
)

// Issue 833: `renumber` moved a branch's section to the end of its chapter and
// left its `### N.M.k` subsections where they were.
//
// It went wrong twice in one day, in two shapes, and the first two tests below
// are one each. They are driven through the built tool in a real repository,
// because the defect was in what the ledger looked like afterwards, and the
// three pieces involved -- the extent of a section, the citation pass that had
// been renumbering subsection headings by accident, and the move -- only
// disagree when all three run.

// chapterNineBase is main's ledger once it has taken 9.2 and 9.3, and given its
// own 9.2 a subsection. Chapter 10 follows, so that "the end of the chapter" is
// not the end of the file.
const chapterNineBase = `## 9. Chapter
### 9.1 A base section
body

### 9.2 Main's second section
main's body, which cites its own 9.2.1

### 9.2.1 Main's subsection
main's subsection body

### 9.3 Main's third section
main's third body

## 10. Later chapter
### 10.1 Something later
later body
`

// mainTakesNineTwoAndThree puts chapterNineBase and a file citing main's own
// 9.2 and 9.2.1 on main, and brings the branch up to it, so that none of those
// lines is this branch's.
func mainTakesNineTwoAndThree(r *ledgerRepo) {
	r.t.Helper()
	r.git("checkout", "-q", "main")
	r.write("docs/DIVERGENCE.md", chapterNineBase)
	r.write("theirs.go", "package x\n\n// main's reasons are 9.2 and 9.2.1\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "main takes 9.2 and 9.3")
	r.git("checkout", "-q", "work")
	r.git("merge", "-q", "main")
}

// headingsOf lists the chapter and numbered headings of a ledger, in order, so
// a test can assert the whole shape at once rather than a heading at a time.
func headingsOf(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") || subsectionHeading.MatchString(line) {
			out = append(out, line)
		}
	}
	return out
}

func assertHeadings(t *testing.T, body string, want []string) {
	t.Helper()
	got := headingsOf(body)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("headings after renumber:\n  %s\nwant:\n  %s\n\nledger:\n%s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "), body)
	}
}

// Case (a), PR 832's shape: the branch added 9.2 with two subsections, main
// took 9.2 and 9.3 first, and the branch's block sits before main's sections.
// It must become 9.4, 9.4.1, 9.4.2, together, at the end of chapter 9.
func TestRenumberMovesAndRenumbersASectionsSubsections(t *testing.T) {
	r := newLedgerRepo(t)
	mainTakesNineTwoAndThree(r)

	mine := `### 9.2 My section
my body, see 9.2.1 and 9.2.2

### 9.2.1 My first subsection
first subsection body

### 9.2.2 My second subsection
second subsection body

`
	r.write("docs/DIVERGENCE.md", strings.Replace(chapterNineBase,
		"### 9.2 Main's second section", mine+"### 9.2 Main's second section", 1))
	r.write("mine.go", "package y\n\n// see 9.2 and 9.2.1\n// and 9.2.2.\n")
	r.git("add", "-A")
	r.git("commit", "-qm", "a colliding section with two subsections")

	out, err := r.ledger("plan")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	for _, want := range []string{
		"9.2.1 -> 9.4.1  My first subsection",
		"9.2.2 -> 9.4.2  My second subsection",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan does not say it will move %q:\n%s", want, out)
		}
	}

	if out, err := r.ledger("renumber"); err != nil {
		t.Fatalf("renumber: %v\n%s", err, out)
	}

	ledger := r.read("docs/DIVERGENCE.md")
	assertHeadings(t, ledger, []string{
		"## 9. Chapter",
		"### 9.1 A base section",
		"### 9.2 Main's second section",
		"### 9.2.1 Main's subsection",
		"### 9.3 Main's third section",
		"### 9.4 My section",
		"### 9.4.1 My first subsection",
		"### 9.4.2 My second subsection",
		"## 10. Later chapter",
		"### 10.1 Something later",
	})
	// Bodies travel with their headings, not merely the headings.
	for _, pair := range [][2]string{
		{"### 9.4.1 My first subsection", "first subsection body"},
		{"### 9.4.2 My second subsection", "second subsection body"},
		{"### 9.2.1 Main's subsection", "main's subsection body"},
	} {
		if !strings.Contains(ledger, pair[0]+"\n"+pair[1]) {
			t.Errorf("%q is not followed by its body %q:\n%s", pair[0], pair[1], ledger)
		}
	}

	if !strings.Contains(ledger, "my body, see 9.4.1 and 9.4.2") {
		t.Errorf("the branch's citations of its own subsections were not rewritten:\n%s", ledger)
	}
	if !strings.Contains(ledger, "main's body, which cites its own 9.2.1") {
		t.Errorf("main's citation of its own 9.2.1 was rewritten:\n%s", ledger)
	}
	if got := r.read("mine.go"); got != "package y\n\n// see 9.4 and 9.4.1\n// and 9.4.2.\n" {
		t.Errorf("mine.go's citations came out as:\n%s", got)
	}
	if got := r.read("theirs.go"); !strings.Contains(got, "9.2 and 9.2.1") {
		t.Errorf("main's own citations were repointed:\n%s", got)
	}
}

// Case (b), the shape after a later merge of main: the branch's 9.4 and its
// subsections were last in the chapter when written, the merge put main's
// newer sections after them, and every number is free. Nothing is renumbered,
// so the whole block has to move -- and the parent used to move alone, ending
// up after its own subsections.
func TestRenumberMovesASectionWithItsSubsectionsWhenOnlyPositionIsWrong(t *testing.T) {
	r := newLedgerRepo(t)
	mainTakesNineTwoAndThree(r)

	mine := `### 9.4 My section
my body, see 9.4.1

### 9.4.1 My first subsection
first subsection body

### 9.4.2 My second subsection
second subsection body

`
	r.write("docs/DIVERGENCE.md", strings.Replace(chapterNineBase,
		"### 9.2 Main's second section", mine+"### 9.2 Main's second section", 1))
	r.git("add", "-A")
	r.git("commit", "-qm", "a free number, in the wrong place")

	out, err := r.ledger("plan")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	for _, want := range []string{"9.4.1  My first subsection", "9.4.2  My second subsection"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan does not list the subsection %q it will move:\n%s", want, out)
		}
	}

	if out, err := r.ledger("renumber"); err != nil {
		t.Fatalf("renumber: %v\n%s", err, out)
	}
	ledger := r.read("docs/DIVERGENCE.md")
	assertHeadings(t, ledger, []string{
		"## 9. Chapter",
		"### 9.1 A base section",
		"### 9.2 Main's second section",
		"### 9.2.1 Main's subsection",
		"### 9.3 Main's third section",
		"### 9.4 My section",
		"### 9.4.1 My first subsection",
		"### 9.4.2 My second subsection",
		"## 10. Later chapter",
		"### 10.1 Something later",
	})
	if !strings.Contains(ledger, "### 9.4.2 My second subsection\nsecond subsection body") {
		t.Errorf("the second subsection lost its body:\n%s", ledger)
	}
}

// A branch that adds only a subsection, under a section main already has, has
// added no section: there is nothing of its own to move or renumber.
func TestASubsectionUnderMainsSectionIsLeftAlone(t *testing.T) {
	r := newLedgerRepo(t)
	mainTakesNineTwoAndThree(r)

	withNote := strings.Replace(chapterNineBase, "### 9.3 Main's third section\nmain's third body\n",
		"### 9.3 Main's third section\nmain's third body\n\n"+
			"### 9.3.1 My note on main's section\nnote body, on 9.3.1\n", 1)
	r.write("docs/DIVERGENCE.md", withNote)
	r.git("add", "-A")
	r.git("commit", "-qm", "a subsection under main's 9.3")

	out, err := r.ledger("renumber")
	if err != nil {
		t.Fatalf("renumber: %v\n%s", err, out)
	}
	if got := r.read("docs/DIVERGENCE.md"); got != withNote {
		t.Errorf("a branch adding only a subsection had its ledger rewritten:\n%s\nwant:\n%s\nrenumber said:\n%s",
			got, withNote, out)
	}
}

// The same subsection, on a branch that also adds a section colliding with the
// parent's number. Renumbering the branch's 9.3 to 9.4 must not take the
// subsection under main's 9.3 with it: its heading is on a line this branch
// added and contains "9.3.", and the citation pass used to rewrite it to 9.4.1,
// under a 9.3.
func TestRenumberingMySectionLeavesMySubsectionOfMainsAlone(t *testing.T) {
	r := newLedgerRepo(t)
	mainTakesNineTwoAndThree(r)

	ledger := strings.Replace(chapterNineBase, "### 9.3 Main's third section\nmain's third body\n",
		"### 9.3 Main's third section\nmain's third body\n\n"+
			"### 9.3.1 My note on main's section\nnote body, on 9.3.1\n\n"+
			"### 9.3 My colliding section\nmine, which has no subsection, beside main's 9.3.1\n", 1)
	r.write("docs/DIVERGENCE.md", ledger)
	r.git("add", "-A")
	r.git("commit", "-qm", "a subsection under main's 9.3, and a colliding 9.3")

	if out, err := r.ledger("renumber"); err != nil {
		t.Fatalf("renumber: %v\n%s", err, out)
	}
	got := r.read("docs/DIVERGENCE.md")
	assertHeadings(t, got, []string{
		"## 9. Chapter",
		"### 9.1 A base section",
		"### 9.2 Main's second section",
		"### 9.2.1 Main's subsection",
		"### 9.3 Main's third section",
		"### 9.3.1 My note on main's section",
		"### 9.4 My colliding section",
		"## 10. Later chapter",
		"### 10.1 Something later",
	})
	// 9.3.1 is main's 9.3's subsection: the branch's 9.3 has none, so a
	// citation of 9.3.1 cannot mean it, even on the branch's own line.
	for _, want := range []string{"note body, on 9.3.1", "beside main's 9.3.1"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q was rewritten:\n%s", want, got)
		}
	}
}

// And when the branch's colliding 9.3 has a 9.3.1 of its own, the two 9.3.1
// headings are told apart by which section they sit under, not by their text,
// which is identical up to the title. Both are on lines this branch added and
// both cite "9.3.1", so a citation rewrite cannot be what renumbers headings:
// only the move knows which parent each belongs to.
func TestTwoSubsectionsWithOneNumberGoWithTheirOwnParents(t *testing.T) {
	r := newLedgerRepo(t)
	mainTakesNineTwoAndThree(r)

	ledger := strings.Replace(chapterNineBase, "### 9.3 Main's third section\nmain's third body\n",
		"### 9.3 Main's third section\nmain's third body\n\n"+
			"### 9.3.1 My note on main's section\nnote body\n\n"+
			"### 9.3 My colliding section\nmine\n\n"+
			"### 9.3.1 My colliding section's subsection\nits body\n", 1)
	r.write("docs/DIVERGENCE.md", ledger)
	r.git("add", "-A")
	r.git("commit", "-qm", "two 9.3.1s, under two 9.3s")

	if out, err := r.ledger("renumber"); err != nil {
		t.Fatalf("renumber: %v\n%s", err, out)
	}
	assertHeadings(t, r.read("docs/DIVERGENCE.md"), []string{
		"## 9. Chapter",
		"### 9.1 A base section",
		"### 9.2 Main's second section",
		"### 9.2.1 Main's subsection",
		"### 9.3 Main's third section",
		"### 9.3.1 My note on main's section",
		"### 9.4 My colliding section",
		"### 9.4.1 My colliding section's subsection",
		"## 10. Later chapter",
		"### 10.1 Something later",
	})
}

// A citation of N.M.k moves with N.M only when N.M has a subsection k.
func TestACitationOfASubsectionMatchesOnlyTheSectionsOwnSubsections(t *testing.T) {
	re := citationRe("9.14", []int{1, 2})
	for _, line := range []string{
		"see 9.14.1 for the first",
		"(see 9.14.2)",
		"9.14.2.",
		"plain 9.14, still",
		"the end: 9.14.",
	} {
		if !re.MatchString(line) {
			t.Errorf("citationRe(9.14, [1 2]) did not match %q", line)
		}
	}
	for _, line := range []string{
		"9.14.3 is somebody else's",
		"9.14.12 is not one of them",
		"9.149.1",
	} {
		if re.MatchString(line) {
			t.Errorf("citationRe(9.14, [1 2]) matched %q", line)
		}
	}
	if bare := citationRe("9.14", nil); bare.MatchString("9.14.1") {
		t.Error("a section with no subsections claimed a citation of 9.14.1")
	}
	if got := re.ReplaceAllString("9.14, 9.14.2 and 9.14.", "9.99${1}${2}"); got != "9.99, 9.99.2 and 9.99." {
		t.Errorf("rewritten as %q", got)
	}
}

// A `###` subsection is inside its parent's extent, at either depth, and a
// subsection of another number is not.
func TestASectionOwnsItsNumberedSubsectionsAtItsOwnDepth(t *testing.T) {
	doc := strings.Split(`## 9. Chapter
### 9.1 First
### 9.1.1 Same depth
#### 9.1.2 Deeper
### 9.2 Second
### 9.1.3 Stray, after somebody else`, "\n")
	got := parseSections(doc)
	if len(got) != 2 {
		t.Fatalf("parsed %d sections, want 2: %v", len(got), got)
	}
	first, second := got[0], got[1]
	if first.end != 4 || len(first.subs) != 2 {
		t.Errorf("9.1 ends at %d with %d subsections, want 4 and 2: %+v", first.end, len(first.subs), first)
	}
	if len(second.subs) != 0 || second.end != 5 {
		t.Errorf("9.2 claimed 9.1.3, or ran past it: %+v", second)
	}
}
