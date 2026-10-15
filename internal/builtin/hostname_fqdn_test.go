package builtin

import (
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The state, on each platform's own persistent record, moving a node to
// and from a fully qualified name (DIVERGENCE 5.290).
//
// The persistent half is each platform's fixture from hostnameFixture:
// rc.conf through `sysrc -n hostname` on FreeBSD, scutil's HostName on
// macOS (the set answer as captured on a macOS 15.7.9 runner), and a
// redirected /etc/hostname elsewhere. The running half is osHostname,
// replaced, so the test can say what the kernel answers without renaming
// the machine it runs on. Everything runs in test mode: the decision
// under test is which halves differ, and nothing is set.

func fakeRunningHostname(t *testing.T, name string) {
	t.Helper()
	old := osHostname
	osHostname = func() (string, error) { return name, nil }
	t.Cleanup(func() { osHostname = old })
}

func hostnameStateChanges(t *testing.T, running, persistent, want string) (changed map[string]bool, where string, ok bool) {
	t.Helper()
	c, where, set := hostnameFixture(t)
	fakeRunningHostname(t, running)
	set(persistent)
	c.Test = true
	res, err := New().States.Call(c, "hostname.system", value.MapOf("name", want))
	if err != nil {
		t.Fatal(err)
	}
	changed = map[string]bool{}
	if res.Changes != nil {
		for _, k := range res.Changes.Keys() {
			if s, ok := k.(string); ok {
				changed[s] = true
			}
		}
	}
	return changed, where, res.Succeeded()
}

// **The defect.** A node that has `web1`, running and configured, and a
// state asking for `web1.example.com`: both halves must change. Before
// this, both compared equal and the state reported converged.
func TestAQualifiedNameIsAppliedToANodeThatHasTheShortOne(t *testing.T) {
	skipOffPlatform(t, unixOnly)
	changed, where, _ := hostnameStateChanges(t, "web1", "web1", "web1.example.com")
	if !changed["running"] {
		t.Errorf("the running name web1 was not changed to web1.example.com: %v", changed)
	}
	if !changed[where] {
		t.Errorf("%s holding web1 was not changed to web1.example.com: %v", where, changed)
	}
}

// Only the half that lacks the domain changes. A node configured with
// the qualified name and running the short one has its running name set;
// one running the qualified name and configured with the short one has
// its configuration rewritten, which is the half that would otherwise
// take the domain away at the next boot.
func TestOnlyTheHalfWithoutTheDomainChanges(t *testing.T) {
	skipOffPlatform(t, unixOnly)

	changed, where, _ := hostnameStateChanges(t, "web1", "web1.example.com", "web1.example.com")
	if !changed["running"] || changed[where] {
		t.Errorf("running web1, %s web1.example.com: want only the running name changed, got %v", where, changed)
	}

	changed, where, _ = hostnameStateChanges(t, "web1.example.com", "web1", "web1.example.com")
	if changed["running"] || !changed[where] {
		t.Errorf("running web1.example.com, %s web1: want only %s changed, got %v", where, where, changed)
	}
}

// **The case the old symmetry was for, kept.** A node that has the
// qualified name satisfies a state asking for the short one, in both
// halves, and the state reports converged rather than stripping the
// domain.
func TestAShortNameStateLeavesAQualifiedNodeAlone(t *testing.T) {
	skipOffPlatform(t, unixOnly)
	changed, _, ok := hostnameStateChanges(t, "web1.example.com", "web1.example.com", "web1")
	if len(changed) != 0 {
		t.Errorf("a node that has web1.example.com was changed by a state asking for web1: %v", changed)
	}
	if !ok {
		t.Error("the converged state did not succeed")
	}

	// And with the halves differing in form, neither is short of what
	// was asked for.
	changed, _, _ = hostnameStateChanges(t, "web1", "web1.example.com", "web1")
	if len(changed) != 0 {
		t.Errorf("running web1, configured web1.example.com, asked web1: changed %v", changed)
	}
}

// Moving between domains changes both halves, as before.
func TestMovingBetweenDomainsChangesTheName(t *testing.T) {
	skipOffPlatform(t, unixOnly)
	changed, where, _ := hostnameStateChanges(t, "web1.example.com", "web1.example.com", "web1.internal")
	if !changed["running"] || !changed[where] {
		t.Errorf("a move from example.com to internal did not change both halves: %v", changed)
	}
}
