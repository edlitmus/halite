package builtin

import (
	"runtime"
	"strings"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
)

// The two gates `liveCase.skipReason` grew for the last tranche, tested here
// because deleting either of them fails nothing else.
//
// # Why this is worth a unit test at all
//
// Every other field on `liveCase` fails visibly when it stops working: a case
// whose `platforms` check vanished runs on the wrong platform and errors, and a
// case whose `needs` check vanished runs without its tool and errors. These two
// fail the other way round. Removing the `needsRebootGate` branch does not
// break a test — it lets `reboot.scheduled` schedule a real reboot on any
// machine with `HALITE_CONFORMANCE_LIVE=1`, which includes every lab instance
// the tofu configuration raises. Removing the `unavailable` branch turns a
// machine whose `aa-*` tools cannot read its own profile tree from a skip into a
// failure that reads like a defect in `apparmor.mode`.
//
// So the assertion is made where it costs nothing: in the ordinary suite, on
// every platform, with no machine offered up. `t.Setenv` is what makes that
// possible -- the gate is read from the environment, so the environment is what
// a test sets.
//
// DIVERGENCE 5.157.

func TestLiveCaseRebootGateIsHonoured(t *testing.T) {
	c := newCtx(false)

	// Nothing else set, so the gate is the only thing that can produce a
	// skip. The first version of this test also named an absent tool, to
	// check the ordering at the same time -- and deleting the gate then
	// failed it on the wrong assertion, because the tool check answered
	// instead. One thing at a time; the ordering is the subtest below.
	bare := liveCase{needsRebootGate: true}

	t.Setenv("HALITE_REBOOT_LIVE", "")
	why := bare.skipReason(c)
	if why == "" {
		t.Fatal("a case marked needsRebootGate ran with HALITE_REBOOT_LIVE unset; " +
			"that variable is the only thing standing between the conformance suite " +
			"and a real reboot scheduled on every lab instance")
	}
	if !strings.Contains(why, "HALITE_REBOOT_LIVE") {
		t.Errorf("the skip does not name the variable to set: %q", why)
	}
	t.Setenv("HALITE_REBOOT_LIVE", "1")
	if why := bare.skipReason(c); why != "" {
		t.Errorf("HALITE_REBOOT_LIVE is set and the case still skips: %q", why)
	}

	// The ordering. The gate is asked before the tool checks, so a case
	// needing a tool this machine lacks reports the gate rather than the
	// tool: the message should say what the run is not permitted to do
	// rather than what the machine happens not to have.
	t.Setenv("HALITE_REBOOT_LIVE", "")
	withTool := liveCase{needsRebootGate: true, needs: []string{"a-tool-no-machine-has"}}
	if why := withTool.skipReason(c); !strings.Contains(why, "HALITE_REBOOT_LIVE") {
		t.Errorf("a gated case on a machine missing its tool reports %q, "+
			"which is the machine's shortcoming and not this run's permission", why)
	}
}

func TestLiveCaseUnavailableIsConsulted(t *testing.T) {
	c := newCtx(false)
	const reason = "this machine's profile tree cannot be read by the aa-* tools"

	// No platforms, no needs: every other check passes, so the only thing
	// that can produce a skip is the predicate.
	asked := false
	lc := liveCase{unavailable: func(*hexec.Context) string {
		asked = true
		return reason
	}}
	if why := lc.skipReason(c); why != reason {
		t.Errorf("skipReason = %q, want the predicate's own words %q", why, reason)
	}
	if !asked {
		t.Error("the predicate was never called")
	}

	// An empty answer is not a skip. A predicate that returned "" and was
	// treated as a reason would skip every case carrying one, silently,
	// which is the failure this half guards.
	if why := (liveCase{unavailable: func(*hexec.Context) string { return "" }}).skipReason(c); why != "" {
		t.Errorf("a predicate that found nothing wrong produced the skip %q", why)
	}
}

// The last tranche's cases are constructed on every platform and gated by
// their `platforms` field, never omitted.
//
// This is the rule `sysctl.present` broke: its case was built inside an
// `if key != ""` and so did not exist on Windows, where the accounting then
// read it as having no case at all. The accounting subtest catches that for a
// function with no case anywhere; it cannot catch a case that exists on three
// platforms and not on the fourth if some other case covers the same function.
// Counting them here does.
func TestLastTrancheCasesAreBuiltEverywhere(t *testing.T) {
	for _, group := range []struct {
		what  string
		cases []liveCase
		want  int
	}{
		{"identity", conformanceIdentityCases(), 3},
		{"package systems", conformancePkgSysCases(), 7},
		{"confinement", conformanceConfineCases(), 4},
	} {
		if got := len(group.cases); got != group.want {
			t.Errorf("the %s group built %d cases on %s, and builds %d elsewhere",
				group.what, got, runtime.GOOS, group.want)
		}
		for _, lc := range group.cases {
			if lc.Name == "" {
				t.Errorf("a case in the %s group names no state function", group.what)
			}
			// A case with no Probe cannot be seen to have left the system
			// alone in test mode; the harness infers it from the module's
			// own answers instead, which is the weaker check. Every case in
			// this tranche acts on the machine, so every one has a probe.
			if lc.Probe == nil {
				t.Errorf("%s has no Probe, and it changes the machine", lc.name())
			}
			if lc.Cleanup == nil {
				t.Errorf("%s has no Cleanup, and it changes the machine", lc.name())
			}
		}
	}
}
