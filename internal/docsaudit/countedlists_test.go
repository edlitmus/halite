package docsaudit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A heading that says how many things follow is right about how many
// follow.
//
// # Why this exists
//
// `docs/migrating-from-salt.md` opens a section with **"The five
// differences that will actually bite"** and then lists them as `###`
// subsections. `docs/from-salt.md` does the same thing in a lead
// sentence over a numbered list. Both numbers are promises about the
// section under them, and both are exactly the kind of prose that goes
// stale: the day `user.present`'s group semantics changed there were six
// differences and the heading still said five.
//
// # Why this is a named list rather than a pattern
//
// A number word in a heading usually is not a count of what follows.
// `docs/DIVERGENCE.md` alone has "5.40 `pkg`: five more functions",
// "5.47 `pam`: two include mechanisms" and a dozen more, none of which
// promise anything about their subsections. A regular expression over
// headings would report those, and an audit that reports correct prose
// gets silenced rather than obeyed.
//
// So the two constructs that do make the promise are named here, and a
// third has to be added deliberately. That is a real limit: a new
// counted list somewhere else is not covered. It is the honest trade,
// and the count of what was checked is logged so the limit is visible.
//
// DIVERGENCE 5.145.
func TestACountedListHasThatManyItems(t *testing.T) {
	root := repoRoot(t)

	cases := []struct {
		page string
		// headingSuffix is the stable tail of the heading making the
		// promise. The number word in front of it is what has to match.
		headingSuffix string
		// itemPrefix is what one item of the list starts with.
		itemPrefix string
		// through ends the section; empty means the next heading of the
		// same level.
		through string
	}{
		{
			page: filepath.Join("docs", "migrating-from-salt.md"),
			// Matched on the part that does not carry the number, so that
			// a wrong count fails as a wrong count rather than as a
			// missing heading -- which is how the first version of this
			// test behaved, and it made the failure read like the section
			// had been deleted.
			headingSuffix: " differences that will actually bite",
			itemPrefix:    "### ",
			through:       "## ",
		},
	}

	words := map[string]int{
		"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
		"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12,
	}
	numberWord := regexp.MustCompile(`\b(one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve)\b`)

	for _, c := range cases {
		data, err := os.ReadFile(filepath.Join(root, c.page))
		if err != nil {
			t.Fatalf("%s: %v", c.page, err)
		}
		body := string(data)
		at := strings.Index(body, c.headingSuffix)
		if at < 0 {
			t.Errorf("%s no longer contains a heading ending %q. If it was reworded, "+
				"reword it here too; if the list is gone, take this case out.",
				c.page, c.headingSuffix)
			continue
		}
		lineStart := strings.LastIndex(body[:at], "\n") + 1
		heading := body[lineStart : at+len(c.headingSuffix)]
		m := numberWord.FindStringSubmatch(heading)
		if m == nil {
			t.Errorf("%s: %q states no number, so this case is checking nothing", c.page, heading)
			continue
		}
		want := words[m[1]]

		rest := body[at+len(c.headingSuffix):]
		if end := strings.Index(rest, "\n"+c.through); end >= 0 {
			rest = rest[:end]
		}
		got := 0
		for _, line := range strings.Split(rest, "\n") {
			if strings.HasPrefix(line, c.itemPrefix) {
				got++
			}
		}
		if got != want {
			t.Errorf("%s: %q promises %d, and %d item(s) follow it. A reader counts the "+
				"headings, finds a different number from the one promised, and stops "+
				"trusting the page. Change the word or the list.",
				c.page, heading, want, got)
		}
	}

	// from-salt.md makes the same promise in a lead sentence over a
	// numbered list, which is a different shape, so it is checked as one.
	data, err := os.ReadFile(filepath.Join(root, "docs", "from-salt.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	const lead = "Six things bite in practice."
	if !strings.Contains(body, lead) {
		t.Errorf("docs/from-salt.md no longer contains %q; this audit is checking nothing "+
			"about that list", lead)
	} else {
		items := regexp.MustCompile(`(?m)^\d+\. \*\*`).FindAllString(body[strings.Index(body, lead):], -1)
		if len(items) != 6 {
			t.Errorf("docs/from-salt.md says %q and %d numbered item(s) follow",
				lead, len(items))
		}
	}
	t.Logf("checked %d counted heading(s) and one counted lead sentence", len(cases))
}
