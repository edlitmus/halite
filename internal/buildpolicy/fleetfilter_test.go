package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The `linux` leg's test filter names families, not individual tests.
//
// # What naming one test costs
//
// `fleet.yml`'s `linux` leg selects what it runs with a `-run` alternation.
// Two of its patterns used to be complete test names —
// `TestLiveModprobeLoadsAndPersists` and `TestLiveUdevControlAsRoot` — so
// each family's *other* test was selected by nothing on that leg. And the
// `linux` leg is the only place a Linux-only live test can execute: the
// `freebsd` leg's bare `-run TestLive` selects it and then skips on the
// platform, and the `debian` leg's catch-all selects it and then skips
// because the Debian tests read `HALITE_FLEET_LIVE` while those read
// `HALITE_SYSTEM_LIVE`.
//
// Measured on Fleet run 36116721474: of 124 `TestLive*` functions, 106
// passed on some leg and 18 on none. Four of those 18 —
// `TestLiveModprobeReadsRealModules`, `TestLiveUdevReadsRealDevices` and
// both `TestLiveSnap*` — were Linux tests that could have run here and
// never had. The other fourteen need a machine CI does not have, or want a
// second opt-in. DIVERGENCE 5.147.
//
// # The rule, and why it is this rule
//
// A pattern that is a **family prefix** picks up a sibling added later; a
// pattern that is a **complete test name** cannot. So a complete test name
// in this filter is the defect, and that is what this checks — rather than
// trying to decide which tests "should" run, which depends on the runner's
// tools and cannot be read out of the tree.
//
// It is deliberately blind to one thing: a family nobody has named at all.
// `TestLiveSnap*` was invisible that way and reading four legs' logs by
// hand is what found it. This catches the narrower case that produced
// three of the four.
func TestTheLinuxLegsFilterNamesFamiliesNotTests(t *testing.T) {
	root := repoRoot(t)
	filter := linuxLegFilter(t, root)
	live := liveTestNames(t, root)

	// A pattern may name one test exactly when the sibling must not run
	// here, with the reason. Empty is not a reason.
	allowed := map[string]string{}

	var checked int
	for _, pattern := range filter {
		if !strings.HasPrefix(pattern, "TestLive") {
			t.Errorf("the linux leg selects %q, which is not a live test pattern", pattern)
			continue
		}
		checked++
		if _, exact := live[pattern]; !exact {
			// A prefix that matches no test exactly is a family name,
			// which is what this test wants.
			if matchesAny(pattern, live) {
				continue
			}
			t.Errorf("the linux leg's filter names %q and no live test matches it. A "+
				"pattern that selects nothing is a leg quietly doing less than its "+
				"name says.", pattern)
			continue
		}
		if reason, ok := allowed[pattern]; ok {
			if reason == "" {
				t.Errorf("%s is allowed to be named exactly, with no reason given", pattern)
			}
			continue
		}
		siblings := family(pattern, live)
		if len(siblings) <= 1 {
			// The only test in its family, so naming it exactly costs
			// nothing today. It will stop being the only one silently,
			// which is what the family form protects against, but there
			// is nothing to fail on yet.
			continue
		}
		sort.Strings(siblings)
		t.Errorf("the linux leg's filter names the complete test %s, and %d test(s) share "+
			"its family: %s. Only this leg can run a Linux-only live test, so a sibling "+
			"the pattern does not match runs nowhere -- which is how four tests went "+
			"unrun. Name the family prefix instead, or add %s to this test's `allowed` "+
			"map with the reason its sibling must not run here.",
			pattern, len(siblings)-1, strings.Join(siblings, ", "), pattern)
	}
	if checked == 0 {
		t.Fatal("no pattern was read out of the linux leg's filter; this audit has " +
			"stopped checking anything")
	}
	t.Logf("checked %d pattern(s) in the linux leg's filter against %d live test(s)",
		checked, len(live))
}

// linuxLegFilter is the alternation the `linux` job passes to `-run`.
func linuxLegFilter(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, ".github", "workflows", "fleet.yml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	// The one `-run '...'` in the file that names TestLiveHostname is the
	// linux leg's: the macos leg's alternation names TestLiveMac first and
	// the other two legs pass a bare `-run TestLive`.
	runPattern := regexp.MustCompile(`-run '([^']*TestLiveHostname[^']*)'`)
	m := runPattern.FindSubmatch(body)
	if m == nil {
		t.Fatalf("%s has no -run alternation naming TestLiveHostname, so this audit "+
			"cannot find the linux leg's filter. If the leg was renamed or its filter "+
			"restructured, update this test rather than deleting it.", path)
	}
	var out []string
	for _, p := range strings.Split(string(m[1]), "|") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// liveTestNames is every TestLive* function in internal/builtin.
func liveTestNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	dir := filepath.Join(root, "internal", "builtin")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}
	decl := regexp.MustCompile(`(?m)^func (TestLive[A-Za-z0-9_]*)\(`)
	out := map[string]bool{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range decl.FindAllSubmatch(body, -1) {
			out[string(m[1])] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("no TestLive function was found under %s", dir)
	}
	return out
}

func matchesAny(pattern string, live map[string]bool) bool {
	rx, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	for name := range live {
		if rx.MatchString(name) {
			return true
		}
	}
	return false
}

// family returns the tests that share a module with the named one.
//
// The module is the run of capitalised words after `TestLive` up to the
// point the names diverge, which for `TestLiveModprobeLoadsAndPersists`
// and `TestLiveModprobeReadsRealModules` is `TestLiveModprobe`. Computed
// from the names rather than declared, so a new family needs no
// bookkeeping here.
func family(name string, live map[string]bool) []string {
	var out []string
	for other := range live {
		if sharedPrefixWords(name, other) > 0 {
			out = append(out, other)
		}
	}
	return out
}

// sharedPrefixWords counts the capitalised words two live test names share
// after `TestLive`, which is 0 for names from different modules.
func sharedPrefixWords(a, b string) int {
	wordsA, wordsB := capWords(strings.TrimPrefix(a, "TestLive")), capWords(strings.TrimPrefix(b, "TestLive"))
	n := 0
	for n < len(wordsA) && n < len(wordsB) && wordsA[n] == wordsB[n] {
		n++
	}
	return n
}

func capWords(s string) []string {
	var out []string
	start := 0
	for i := 1; i <= len(s); i++ {
		if i == len(s) || (s[i] >= 'A' && s[i] <= 'Z') {
			out = append(out, s[start:i])
			start = i
		}
	}
	return out
}
