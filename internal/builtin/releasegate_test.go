//go:build releasegate

// The release gate: a module that changes a machine as root and has
// never been run against the tool it drives does not go into a release.
//
// # Why a gate rather than a warning
//
// Everything else this build does about unverified modules informs
// somebody: `doctor` warns, `sys.evidence` answers, a failing mutation
// carries the caveat. All of that reaches an operator who is already
// looking. A release reaches operators who are not — a fleet upgrades,
// and whatever shipped is now running as root on every host in it.
//
// A gate is the one control here that cannot make things worse. It has
// exactly one failure mode, which is that a release does not happen,
// and a release that does not happen breaks nothing. Weighed against
// what it prevents — a module that has never met its tool, shipped to
// machines whose operators have no reason to suspect it — that is a
// trade with no downside worth the name.
//
// # It is behind a build tag on purpose
//
// Ordinary development must not be blocked by this. A module written on
// a Tuesday is undemonstrated on a Tuesday; that is the normal and
// correct state of new work, and a gate that fired there would be
// worked around within the week. It fires once, at the point where the
// decision is actually being made.
//
//	make release-gate
//
// # There is no override
//
// Not because an override could never be right, but because the
// override is what gets used at five on a Friday. The two ways past
// this are the two that leave an operator no worse off: demonstrate the
// module against the real tool and say so in evidence.go, or take it
// out of the build.
package builtin

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestReleaseGateNoUnverifiedRootMutatingModule(t *testing.T) {
	r := New()
	var blocking []string
	for _, m := range r.Trust() {
		if m.Root && !m.Demonstrated {
			blocking = append(blocking, m.Module)
		}
	}
	sort.Strings(blocking)
	if len(blocking) == 0 {
		t.Logf("every module that changes a machine as root has been run against its tool")
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\n%d module(s) change a machine as root and have never been run "+
		"against the tool they drive:\n\n", len(blocking))
	for _, module := range blocking {
		e := r.Exec.Evidence(module)
		note := e.Note
		if note == "" {
			note = "no declaration at all; nobody has considered this module"
		}
		fmt.Fprintf(&b, "  %-10s %s\n", module, note)
	}
	b.WriteString("\nThis blocks the release and nothing else. Two ways past it, both of\n" +
		"which leave an operator better off than shipping:\n\n" +
		"  1. Run the module against the real tool on a real machine, then raise its\n" +
		"     level in internal/builtin/evidence.go and say in the note which machine.\n" +
		"  2. Take it out of the build.\n\n" +
		"Raising the level without doing the work is the one option that is worse than\n" +
		"not having this gate, because it converts an unknown into a claim.\n")
	t.Fatal(b.String())
}
