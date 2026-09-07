package exec

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/edlitmus/halite/internal/signature"
)

// Evidence is what has actually been demonstrated about a module's
// dealings with the tool it drives.
//
// # Why this exists at runtime rather than only in a document
//
// A module that mutates a machine is trusted with something. Most of
// them work by running another program and reading what it says back,
// and that is the part a unit test cannot establish: a test supplies the
// output and checks the parse, which proves the parser reads whatever
// the test author believed the tool prints.
//
// That belief has been wrong. The `pf` provider compared its rendered
// rule against `pfctl -s rules` on the assumption that pf prints back
// the text it was given; it does not, so no rule ever matched itself,
// both of a real host's rules were reported as added on every run, and
// `firewall.absent` could remove neither. The idempotence test passed
// throughout, because its fixture was written in the module's own
// spelling. DIVERGENCE 5.31.
//
// Recording that in the ledger is necessary and is not enough. An
// operator at three in the morning, looking at a state that did not do
// what it said, is deciding whether the fault is theirs or ours — and
// the answer is in a document on a machine they are not looking at. So
// when a mutating function fails and nobody has demonstrated that this
// module and its tool understand each other, the failure says so.
//
// It never appears on success. A module that works is not made better
// by a caveat on every run, and a warning nobody can act on is a warning
// people learn to skip.
type Evidence struct {
	// Level is how far this module's dealings with its tool have been
	// demonstrated.
	Level EvidenceLevel
	// Note says what was demonstrated, or what is assumed. Required for
	// anything short of Hardware, and it is the sentence an operator
	// reads at three in the morning: name the tool, name the doubt.
	Note string
}

// EvidenceLevel orders what has been shown.
type EvidenceLevel int

const (
	// Assumed is the default, and the default is deliberate: a module
	// nobody has classified has not been demonstrated, and saying
	// otherwise by omission is how a claim nobody made ends up believed.
	//
	// It means the tests were written from documentation, from a manual
	// page, or from what the author expected the tool to print.
	Assumed EvidenceLevel = iota
	// Captured means the tests hold output taken from the real tool,
	// so the parsing is checked against the program rather than against
	// an expectation — but nothing has watched the module change
	// anything.
	Captured
	// Hardware means the module's mutating path has been run against
	// the real tool on a real system, and the Note says which and when.
	Hardware
)

func (l EvidenceLevel) String() string {
	switch l {
	case Hardware:
		return "hardware"
	case Captured:
		return "captured"
	default:
		return "assumed"
	}
}

// Demonstrated reports whether anything has checked this module against
// the program it drives.
func (e Evidence) Demonstrated() bool { return e.Level >= Captured }

// Caveat is what a failing mutation appends, or empty when there is
// nothing to say.
//
// Phrased as a possibility rather than a diagnosis. The module may be
// perfectly correct and the node may be genuinely broken; what an
// operator needs is to know that the first is not ruled out, because
// nothing has ruled it out.
func (e Evidence) Caveat(module string) string {
	if e.Demonstrated() {
		return ""
	}
	note := e.Note
	if note == "" {
		note = "nothing has checked this module against the program it drives"
	}
	return fmt.Sprintf(
		"\n  note: halite's `%s` module has not been demonstrated against the tool it "+
			"drives, so this may be a defect in halite rather than in the node. %s "+
			"docs/DIVERGENCE.md records what is assumed.",
		module, strings.TrimRight(note, "."))
}

// evidenceStore holds a registry's declarations.
type evidenceStore struct {
	mu sync.RWMutex
	by map[string]Evidence
}

// SetEvidence records what has been demonstrated about a module.
//
// Called once per module at registration. A module with no declaration
// is Assumed, which is why the guard in internal/builtin requires one
// from anything that mutates as root: the default is right and being
// there by default is not the same as being decided.
func (r *Registry) SetEvidence(module string, e Evidence) {
	r.evidence.mu.Lock()
	defer r.evidence.mu.Unlock()
	if r.evidence.by == nil {
		r.evidence.by = map[string]Evidence{}
	}
	r.evidence.by[module] = e
}

// Evidence returns what is known about a module.
func (r *Registry) Evidence(module string) Evidence {
	r.evidence.mu.RLock()
	defer r.evidence.mu.RUnlock()
	return r.evidence.by[module]
}

// DeclaredEvidence returns every declaration, for an audit and for
// `sys.doc`.
func (r *Registry) DeclaredEvidence() map[string]Evidence {
	r.evidence.mu.RLock()
	defer r.evidence.mu.RUnlock()
	out := make(map[string]Evidence, len(r.evidence.by))
	for k, v := range r.evidence.by {
		out[k] = v
	}
	return out
}

// UndemonstratedModules lists the modules that mutate and have not been
// demonstrated, in name order.
//
// This is what an operator asks *before* three in the morning, through
// `sys.evidence` and through `doctor`.
func (r *Registry) UndemonstratedModules() []string {
	seen := map[string]bool{}
	for _, name := range r.sigs.Names() {
		sig, ok := r.sigs.Lookup(name)
		if !ok || !sig.Mutates {
			continue
		}
		module, _, _ := strings.Cut(name, ".")
		if seen[module] || r.Evidence(module).Demonstrated() {
			continue
		}
		seen[module] = true
	}
	out := make([]string, 0, len(seen))
	for module := range seen {
		out = append(out, module)
	}
	sort.Strings(out)
	return out
}

// withEvidence appends the caveat to an error from a mutating function.
//
// Only a mutating one, and only a failure. A read that goes wrong is a
// question about the node; a change that goes wrong is a question about
// both, and this is the moment the second half is worth raising.
func (r *Registry) withEvidence(name string, sig signature.Signature, err error) error {
	if err == nil || !sig.Mutates {
		return err
	}
	module, _, _ := strings.Cut(name, ".")
	caveat := r.Evidence(module).Caveat(module)
	if caveat == "" {
		return err
	}
	return fmt.Errorf("%w%s", err, caveat)
}
