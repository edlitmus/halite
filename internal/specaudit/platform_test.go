package specaudit

import (
	"regexp"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/exec"
)

// specPlatformModules reads SPEC 15.3's inventory out of the table.
func specPlatformModules(t *testing.T) map[string]bool {
	t.Helper()
	spec := repoFile(t, specFile)
	start := strings.Index(spec, "### 15.3 Platform modules")
	if start < 0 {
		t.Fatal("SPEC.md has no 15.3; this audit is reading the wrong file")
	}
	end := strings.Index(spec[start:], "15.4")
	if end < 0 {
		t.Fatal("SPEC 15.3 has no end")
	}

	named := regexp.MustCompile("`([a-z0-9_]+)`")
	out := map[string]bool{}
	for _, line := range strings.Split(spec[start:start+end], "\n") {
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|---") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 2 || strings.Contains(cells[0], "Platform") {
			continue
		}
		for _, m := range named.FindAllStringSubmatch(cells[1], -1) {
			out[m[1]] = true
		}
	}
	if len(out) == 0 {
		t.Fatal("no modules were read out of SPEC 15.3")
	}
	return out
}

// TestPendingPlatformModulesMatchTheSpec holds the refusal table to the
// specification in both directions.
//
// SPEC 15.3 names 65 platform modules and this build registers a
// handful of them. The rest were absent rather than refused, so a tree naming `aptpkg`
// got "not a function this build ships" — which reads as a typo, and
// sends an operator looking for a spelling error instead of a gap. They
// are declared as pending now, and this is what stops that table
// drifting from the inventory it mirrors: a module that arrives cannot
// stay listed as pending, and one added to the specification cannot be
// quietly missed.
func TestPendingPlatformModulesMatchTheSpec(t *testing.T) {
	inSpec := specPlatformModules(t)
	pending := exec.PendingPlatformModules()
	registries := builtin.New()

	built := map[string]bool{}
	for _, module := range registries.Exec.Signatures().Modules() {
		built[module] = true
	}

	for name := range inSpec {
		_, isPending := pending[name]
		switch {
		case built[name] && isPending:
			t.Errorf("%s is registered and still listed as pending; an operator "+
				"is told it does not exist while it answers", name)
		case !built[name] && !isPending:
			t.Errorf("SPEC 15.3 names %s, this build does not have it, and it is "+
				"not declared pending — so it reads as a typo", name)
		}
	}

	for name := range pending {
		if !inSpec[name] {
			t.Errorf("%s is declared pending and SPEC 15.3 does not name it", name)
		}
	}

	t.Logf("SPEC 15.3 names %d modules, %d pending", len(inSpec), len(pending))
}

// Every pending module says what it waits on, because the reason is the
// whole of what an operator is given.
func TestEveryPendingPlatformModuleSaysWhy(t *testing.T) {
	for name, m := range exec.PendingPlatformModules() {
		if strings.TrimSpace(m.When) == "" {
			t.Errorf("%s is pending with no reason", name)
		}
		if strings.TrimSpace(m.Platform) == "" {
			t.Errorf("%s is pending with no platform", name)
		}
	}
}
