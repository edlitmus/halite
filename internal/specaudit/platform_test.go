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
	// An alias counts as built, because the question this audit asks is
	// whether an operator naming the module gets it. SPEC 15.3's
	// `aptpkg` resolves to 15.2's `pkg` and refuses on a node whose
	// package manager is something else — which is an answer, and not
	// the "this build does not have it" a pending entry would give.
	for module := range registries.Exec.Aliases() {
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

// The ledger's own platform table is held to the registry, both columns.
//
// TestPendingPlatformModulesMatchTheSpec checks the registry against
// SPEC 15.3 and says nothing about DIVERGENCE 2.3, which is prose with
// a table in it — and that table drifted. `ufw` and `netplan` were
// listed in the Present column and left in the Absent column of the
// same row, so the ledger said each of them was both, and the audit
// that exists to stop the ledger lying did not look there.
//
// A reader takes that table for the answer, because it is the only
// place the gap is broken down by platform. So: every name in a Present
// cell is one the build answers to, every name in an Absent cell is one
// it does not, and each of SPEC 15.3's modules appears in exactly one
// cell of the whole table.
func TestTheLedgerPlatformTableMatchesTheRegistry(t *testing.T) {
	doc := repoFile(t, ledgerFile)
	present, absent := ledgerPlatformColumns(t, doc)

	registries := builtin.New()
	built := map[string]bool{}
	for _, module := range registries.Exec.Signatures().Modules() {
		built[module] = true
	}
	for module := range registries.Exec.Aliases() {
		built[module] = true
	}
	pending := exec.PendingPlatformModules()

	for name := range present {
		if !built[name] {
			t.Errorf("%s: DIVERGENCE 2.3 lists it as present and the build does not have it", name)
		}
		if absent[name] {
			t.Errorf("%s: DIVERGENCE 2.3 lists it in both columns of the table", name)
		}
	}
	for name := range absent {
		if built[name] {
			t.Errorf("%s: DIVERGENCE 2.3 lists it as absent and the build ships it", name)
		}
		if _, ok := pending[name]; !ok {
			t.Errorf("%s: DIVERGENCE 2.3 lists it as absent and it is not declared pending", name)
		}
	}

	// And nothing SPEC 15.3 names may be missing from the table
	// altogether, which is how a module comes to be neither claimed nor
	// disclaimed.
	for name := range specPlatformModules(t) {
		if !present[name] && !absent[name] {
			t.Errorf("%s: SPEC 15.3 names it and DIVERGENCE 2.3's table has it in neither column", name)
		}
	}
	t.Logf("DIVERGENCE 2.3 lists %d present and %d absent", len(present), len(absent))
}

// ledgerPlatformColumns reads the two module columns of DIVERGENCE 2.3's
// table. The row label is skipped: "Common Linux" carries no backticks,
// but a row label that gained one would otherwise read as a module.
func ledgerPlatformColumns(t *testing.T, doc string) (present, absent map[string]bool) {
	t.Helper()
	const heading = "### 2.3 Platform modules"
	start := strings.Index(doc, heading)
	if start < 0 {
		t.Fatalf("%s has no section 2.3; this audit is reading a document it was not written for", ledgerFile)
	}
	body := doc[start+len(heading):]
	if end := strings.Index(body, "\n### "); end > 0 {
		body = body[:end]
	}

	present, absent = map[string]bool{}, map[string]bool{}
	rows := 0
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 3 || strings.Contains(cells[0], "---") {
			continue
		}
		if strings.TrimSpace(cells[1]) == "Present" {
			continue // the header
		}
		rows++
		for _, name := range namesIn(cells[1]) {
			present[name] = true
		}
		for _, name := range namesIn(cells[2]) {
			absent[name] = true
		}
	}
	if rows == 0 {
		t.Fatalf("%s section 2.3 has no table rows; this audit has stopped checking anything", ledgerFile)
	}
	return present, absent
}
