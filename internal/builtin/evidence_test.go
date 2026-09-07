package builtin

import (
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// rootMutatingModules is every module in the registry with at least one
// function that both changes the system and declares it needs root.
//
// Derived from the signatures rather than listed here, because a list
// here would be one more thing that has to be remembered, and the whole
// point of this file is that the things nobody remembers are the things
// that ship unverified.
func rootMutatingModules(t *testing.T) []string {
	t.Helper()
	r := New()
	sigs := r.Exec.Signatures()
	found := map[string]bool{}
	for _, name := range sigs.Names() {
		sig, ok := sigs.Lookup(name)
		if !ok || !sig.Mutates {
			continue
		}
		for _, p := range sig.Privileges {
			// "root", "the target account, or root", "root, or a
			// delegated zfs permission" — all of them can be root.
			if strings.Contains(p, "root") {
				module, _, _ := strings.Cut(name, ".")
				found[module] = true
				break
			}
		}
	}
	out := make([]string, 0, len(found))
	for m := range found {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// A module that changes a machine as root does not get to ship without
// somebody writing down what has been demonstrated about it.
//
// The default is already `Assumed`, so this guard is not protecting the
// runtime from a wrong answer — it is protecting the table from a
// silence that reads like one. A module absent from the table is a
// module nobody considered, and that is a different fact from a module
// somebody considered and found unverified. The operator reading the
// note at three in the morning is owed the second.
func TestEveryRootMutatingModuleDeclaresItsEvidence(t *testing.T) {
	var missing []string
	for _, module := range rootMutatingModules(t) {
		if _, ok := moduleEvidence[module]; !ok {
			missing = append(missing, module)
		}
	}
	if len(missing) > 0 {
		t.Errorf("these modules change a machine as root and declare no evidence: %s\n"+
			"Add an entry to moduleEvidence in evidence.go saying what has been "+
			"demonstrated, or — far more likely — what is assumed and why.",
			strings.Join(missing, ", "))
	}
}

// A declaration for a module that is not there is a claim about nothing,
// and it goes stale silently: rename the module and the table keeps
// vouching for the old name while the new one is undeclared.
func TestEveryDeclarationNamesAModuleThatExists(t *testing.T) {
	r := New()
	sigs := r.Exec.Signatures()
	known := map[string]bool{}
	for _, name := range sigs.Names() {
		module, _, _ := strings.Cut(name, ".")
		known[module] = true
	}
	for module := range moduleEvidence {
		if !known[module] {
			t.Errorf("moduleEvidence declares %q, which is not a module this build registers", module)
		}
	}
}

// Every declaration says something, at every level.
//
// `Hardware` needs a note as much as `Assumed` does — more, arguably.
// "verified" with no note is the claim an operator cannot check and
// cannot date, and the levels above `Assumed` are the ones somebody will
// lean on.
func TestEveryDeclarationCarriesANote(t *testing.T) {
	for module, e := range moduleEvidence {
		if strings.TrimSpace(e.Note) == "" {
			t.Errorf("%s declares %s with no note; say what was demonstrated, or what is assumed", module, e.Level)
		}
	}
}

// The caveat reaches an operator through a failing mutation, and only
// through a failing mutation.
func TestAFailingRootMutationCarriesTheCaveat(t *testing.T) {
	e := exec.Evidence{Level: exec.Assumed, Note: "nobody has run `aa-enforce`"}
	caveat := e.Caveat("apparmor")
	for _, want := range []string{"apparmor", "may be a defect in halite", "aa-enforce", "DIVERGENCE"} {
		if !strings.Contains(caveat, want) {
			t.Errorf("caveat does not mention %q:\n%s", want, caveat)
		}
	}
	if c := (exec.Evidence{Level: exec.Captured, Note: "x"}).Caveat("jail"); c != "" {
		t.Errorf("a demonstrated module still carries a caveat: %q", c)
	}
	if c := (exec.Evidence{Level: exec.Hardware, Note: "x"}).Caveat("pkg"); c != "" {
		t.Errorf("a hardware-verified module still carries a caveat: %q", c)
	}
}

// What `sys.evidence` and `doctor` read is what this table says, so the
// registry has to actually be carrying it.
func TestTheRegistryCarriesTheDeclarations(t *testing.T) {
	r := New()
	declared := r.Exec.DeclaredEvidence()
	if len(declared) != len(moduleEvidence) {
		t.Fatalf("registry carries %d declarations, table has %d", len(declared), len(moduleEvidence))
	}
	for module, want := range moduleEvidence {
		if got := r.Exec.Evidence(module); got != want {
			t.Errorf("%s: registry has %+v, table has %+v", module, got, want)
		}
	}

	// And a module nobody declared reads as undemonstrated rather than
	// as absent, which is the whole reason Assumed is the zero value.
	if r.Exec.Evidence("no_such_module").Demonstrated() {
		t.Error("an undeclared module reads as demonstrated")
	}
}

// The undemonstrated list is what an operator asks for ahead of time,
// so it has to contain the modules the table says are undemonstrated —
// no more, and no fewer — and nothing that only reads.
//
// What this cannot check is whether a declaration is *true*. Nothing in
// a test can distinguish a module verified on hardware from one whose
// row says so, which is why the levels above Assumed require a note
// naming the machine: the note is the part a person can go and check.
func TestUndemonstratedListsExactlyWhatIsUndemonstrated(t *testing.T) {
	r := New()
	sigs := r.Exec.Signatures()
	got := map[string]bool{}
	for _, m := range r.Exec.UndemonstratedModules() {
		got[m] = true
	}
	for _, module := range rootMutatingModules(t) {
		undemonstrated := !moduleEvidence[module].Demonstrated()
		if undemonstrated != got[module] {
			t.Errorf("%s: table says demonstrated=%v, UndemonstratedModules says %v",
				module, !undemonstrated, got[module])
		}
	}

	// A module that only reads has nothing to warn about, and warning
	// about it is how the list stops being read.
	mutates := map[string]bool{}
	for _, name := range sigs.Names() {
		if sig, ok := sigs.Lookup(name); ok && sig.Mutates {
			module, _, _ := strings.Cut(name, ".")
			mutates[module] = true
		}
	}
	for module := range got {
		if !mutates[module] {
			t.Errorf("UndemonstratedModules lists %q, which changes nothing", module)
		}
	}
}
