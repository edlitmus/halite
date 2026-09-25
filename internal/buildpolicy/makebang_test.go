package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every Makefile target that stamps a build or installs one must refuse
// a make that cannot evaluate `!=` (DIVERGENCE 5.154).
//
// GNU make before 4.0 -- macOS's /usr/bin/make is 3.81 -- reads
// `GIT_VERSION != git describe` as an assignment to a variable named
// "GIT_VERSION !" and carries on, so every `!=` value is empty: a build
// with no version, no commit and no SOURCE_DATE_EPOCH, and an install
// whose CONFDIR, STATEDIR and SERVICEDIR are empty strings. Nothing
// failed. make-supports-bang fails instead, and it only protects the
// targets that list it, so the list is checked here against the recipes:
// a target whose recipe uses $(BUILDFLAGS) or installs, and does not name
// the guard, is one a Mac would run with blanks.
func TestEveryStampingTargetRefusesAMakeWithoutBang(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(b)
	if !regexp.MustCompile(`(?m)^MAKE_BANG_PROBE\s*!=\s*echo supported$`).MatchString(mk) {
		t.Fatal("the Makefile has no MAKE_BANG_PROBE set with !=")
	}
	if !regexp.MustCompile(`(?m)^make-supports-bang:\s*$`).MatchString(mk) {
		t.Fatal("the Makefile has no make-supports-bang target")
	}

	rule := regexp.MustCompile(`(?m)^([a-z][a-z0-9-]*):([^=\n]*)$`)
	locs := rule.FindAllStringSubmatchIndex(mk, -1)
	checked := 0
	for i, loc := range locs {
		name, prereqs := mk[loc[2]:loc[3]], mk[loc[4]:loc[5]]
		end := len(mk)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		recipe := mk[loc[1]:end]
		stamps := strings.Contains(recipe, "$(BUILDFLAGS)")
		installs := strings.HasPrefix(name, "install")
		if !stamps && !installs {
			continue
		}
		checked++
		if !strings.Contains(" "+prereqs+" ", " make-supports-bang ") {
			t.Errorf("target %s %s and does not depend on make-supports-bang", name,
				map[bool]string{true: "uses $(BUILDFLAGS)", false: "installs"}[stamps])
		}
	}
	if checked < 10 {
		t.Errorf("found only %d stamping or installing targets; the rule reader has stopped matching the Makefile", checked)
	}
}

// The version stamp is a function of the commit alone (DIVERGENCE 5.155).
// `git describe --always` without a fixed --abbrev picks the length of
// the abbreviated hash from the repository's object count, so the same
// commit was stamped with seven characters on one clone and eight on
// another, and the digest followed.
func TestTheVersionStampHasAFixedAbbreviation(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^GIT_VERSION\s*!=\s*(git describe[^|]*)`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("GIT_VERSION is not set from git describe")
	}
	if !regexp.MustCompile(`--abbrev=\d+`).MatchString(m[1]) {
		t.Errorf("GIT_VERSION is %q, with no fixed --abbrev", strings.TrimSpace(m[1]))
	}
}
