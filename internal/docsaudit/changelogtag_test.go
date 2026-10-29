package docsaudit

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// changelogFitsTag says whether a changelog is one a release of version
// may be tagged from: a `## <version> — <date>` heading, and nothing
// still listed under `## Unreleased`.
//
// Issue 313: v0.1.1 was tagged with its entries under "Unreleased", and
// the heading was fixed afterwards, so the changelog at the tagged commit
// described no release. docs/releasing.md asked for the rename before
// tagging; nothing checked that it had happened. A claim nobody checks
// is the thing this project calls a defect, so `make release-gate` does,
// through TestReleaseGateChangelogNamesTheTag.
//
// Only headings are read: the check is that the entries moved, not what
// they say.
func changelogFitsTag(changelog, version string) error {
	heading := regexp.MustCompile(`(?m)^## ` + regexp.QuoteMeta(version) + ` — \d{4}-\d{2}-\d{2}$`)
	if !heading.MatchString(changelog) {
		return fmt.Errorf("CHANGELOG.md has no %q heading of the form `## %s — YYYY-MM-DD`", version, version)
	}
	inUnreleased := false
	for _, line := range strings.Split(changelog, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			inUnreleased = line == "## Unreleased"
		case inUnreleased && strings.HasPrefix(line, "### "):
			return fmt.Errorf("`## Unreleased` still lists %q: move it under `## %s` before tagging",
				strings.TrimPrefix(line, "### "), version)
		}
	}
	return nil
}

// What v0.1.1 was actually tagged with, headings only, from
// `git show v0.1.1:CHANGELOG.md`: no 0.1.1 heading, and six entries under
// Unreleased. The check must refuse it, or it would not have caught the
// defect it exists for.
func TestChangelogCheckRefusesWhatV011WasTaggedWith(t *testing.T) {
	raw, err := os.ReadFile("testdata/changelog-v0.1.1-as-tagged.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := changelogFitsTag(string(raw), "0.1.1"); err == nil {
		t.Fatal("the changelog v0.1.1 was tagged with passed the check")
	}
}

func TestChangelogCheckAcceptsARenamedHeading(t *testing.T) {
	const ok = "# Changelog\n\n## Unreleased\n\n## 0.1.2 — 2026-11-01\n\n### Something\n\ntext\n\n## 0.1.1 — 2026-10-12\n\n### Older\n"
	if err := changelogFitsTag(ok, "0.1.2"); err != nil {
		t.Fatalf("a renamed heading and an empty Unreleased was refused: %v", err)
	}
	for name, bad := range map[string]string{
		"no heading":           "## Unreleased\n\n## 0.1.1 — 2026-10-12\n",
		"entries left behind":  "## Unreleased\n\n### Left\n\n## 0.1.2 — 2026-11-01\n",
		"heading without date": "## 0.1.2\n",
		"another version":      "## 0.1.20 — 2026-11-01\n",
	} {
		if err := changelogFitsTag(bad, "0.1.2"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
