package main

import (
	"strings"
	"testing"
)

// Issue 844: `renumber` moved a branch's section to the end of its chapter
// with no blank line before its heading, so it read as the last line of the
// section before it, and put two blank lines before the next chapter's
// `## ` heading where the ledger has one. Both had to be put right by hand,
// on three branches in one day, and the headings-only assertions in
// subsections_test.go could not see either: they compare the headings and
// skip every blank line.
//
// So these compare the whole file, byte for byte, against the base with the
// renumbered block written in by hand where it belongs. That asserts the
// separators around the moved block and also that nothing else in the file
// moved -- the gap the block was lifted out of included.

// separatorsBase is main's ledger in the shape docs/DIVERGENCE.md is written
// in: a blank line after a chapter heading, exactly one before every `###`,
// and exactly one before the next `## `. Chapter 9's last section ends in a
// list, as the real ones often do, because a list's last line is where a
// heading pasted with no blank line is easiest to miss.
const separatorsBase = `# Ledger

## 9. Chapter

### 9.1 A base section
body

### 9.2 Main's second section
main's second body

- a list item
- the last item

## 10. Last chapter

### 10.1 Something later
later body
`

func TestRenumberGivesAMovedSectionTheLedgersSeparators(t *testing.T) {
	cases := []struct {
		name string
		// before is the base line the branch's block is written in front
		// of, and block is that block as the branch wrote it.
		before, block string
		// after is the base line the renumbered block must end up in front
		// of ("" for the end of the file), and want is the block there.
		after, want string
	}{
		{
			name:   "a section with no subsections",
			before: "### 9.2 Main's second section",
			block:  "### 9.2 My section\nmy body\n\n",
			after:  "## 10. Last chapter",
			want:   "### 9.3 My section\nmy body\n\n",
		},
		{
			name:   "a section with subsections",
			before: "### 9.2 Main's second section",
			block: "### 9.2 My section\nmy body\n\n" +
				"### 9.2.1 My subsection\nsubsection body\n\n" +
				"#### 9.2.2 My deeper subsection\ndeeper body\n\n",
			after: "## 10. Last chapter",
			want: "### 9.3 My section\nmy body\n\n" +
				"### 9.3.1 My subsection\nsubsection body\n\n" +
				"#### 9.3.2 My deeper subsection\ndeeper body\n\n",
		},
		{
			// The last chapter has no `## ` after it: its end is the end of
			// the file, which must still end in exactly one newline.
			name:   "a section in the last chapter",
			before: "### 10.1 Something later",
			block:  "### 10.1 My later section\nmy later body\n\n",
			after:  "",
			want:   "\n### 10.2 My later section\nmy later body\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mustReplace := func(body, old, new string) string {
				t.Helper()
				if !strings.Contains(body, old) {
					t.Fatalf("the base has no line %q", old)
				}
				return strings.Replace(body, old, new, 1)
			}

			r := newLedgerRepo(t)
			r.git("checkout", "-q", "main")
			r.write("docs/DIVERGENCE.md", separatorsBase)
			r.git("add", "-A")
			r.git("commit", "-qm", "main's ledger")
			r.git("checkout", "-q", "work")
			r.git("merge", "-q", "main")
			r.write("docs/DIVERGENCE.md", mustReplace(separatorsBase, c.before, c.block+c.before))
			r.git("add", "-A")
			r.git("commit", "-qm", "a colliding section")

			if out, err := r.ledger("renumber"); err != nil {
				t.Fatalf("renumber: %v\n%s", err, out)
			}

			want := separatorsBase + c.want
			if c.after != "" {
				want = mustReplace(separatorsBase, c.after, c.want+c.after)
			}
			if got := r.read("docs/DIVERGENCE.md"); got != want {
				t.Errorf("ledger after renumber:\n%s\nwant:\n%s", visibleBlanks(got), visibleBlanks(want))
			}
		})
	}
}

// visibleBlanks marks every line's end, so that a failure shows a missing or
// doubled blank line rather than two texts that look alike.
func visibleBlanks(body string) string {
	return strings.ReplaceAll(body, "\n", "$\n")
}
