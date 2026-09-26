package states

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// ---- the Unchanging contract ----
//
// A state that changes nothing cannot be held to the phases in Check: the
// first of them fails a function that reports success, because a case whose
// setup left nothing to do is testing nothing. `cmd.wait` and `module.wait`
// exist to do nothing until a watch requisite fires, and the `test.*` fakes
// exist to report a fixed answer, so six functions had no case for a reason
// that was really "the harness cannot express this one".
//
// What replaces those phases has to be shown to catch the defects such a
// function can actually have, which is what these are for. DIVERGENCE
// 5.157.

// unchangingModule reports a fixed answer and touches nothing.
func unchangingModule(name string) Module {
	m := fixedModule(name, func(c *exec.Context, args *value.Map) (Result, error) {
		return True("This state does nothing, successfully."), nil
	})
	m.Sig.Mutates = false
	return m
}

func runUnchanging(t *testing.T, m Module, probe func() (string, error)) []Failure {
	t.Helper()
	r := NewRegistry()
	r.Add(m)
	cf := Conformance{
		Name:             m.Sig.Name(),
		Args:             value.MapOf("name", "ignored"),
		Probe:            probe,
		Unchanging:       true,
		UnchangingReason: "this state exists to do nothing",
	}
	return cf.Check(r, func(test bool) *exec.Context { return &exec.Context{Test: test} })
}

func TestUnchangingAcceptsAStateThatDoesNothing(t *testing.T) {
	if got := runUnchanging(t, unchangingModule("test.nothing"), nil); len(got) != 0 {
		t.Errorf("a state that does nothing was failed: %v", got)
	}
}

// The defect this mode exists to catch: a `cmd.wait` that runs its command
// anyway runs it in test mode too, which is an operator asking what a
// highstate would do and having it done.
func TestUnchangingCatchesAStateThatActsAnyway(t *testing.T) {
	var ran int
	m := fixedModule("cmd.waitish", func(c *exec.Context, args *value.Map) (Result, error) {
		ran++ // the bug: it does the work whatever the mode
		return True("This state waited for a watch requisite."), nil
	})

	failures := runUnchanging(t, m, func() (string, error) {
		return strings.Repeat("x", ran), nil
	})
	if !hasPhase(failures, "probe") {
		t.Fatalf("the probe did not catch a state that acted on every run: %v", failures)
	}
	if ran == 0 {
		t.Fatal("the harness never called the function, so this proves nothing")
	}
}

// Without a probe the same defect is invisible, which is why `cmd.wait` and
// `module.wait` are given one. Asserted rather than assumed, because a
// reader could reasonably expect the four-run comparison to catch it: it
// cannot, since a state that hides its effect answers identically each time.
func TestUnchangingWithoutAProbeCannotSeeAHiddenEffect(t *testing.T) {
	var ran int
	m := fixedModule("cmd.waitish", func(c *exec.Context, args *value.Map) (Result, error) {
		ran++
		return True("This state waited for a watch requisite."), nil
	})
	if got := runUnchanging(t, m, nil); len(got) != 0 {
		t.Errorf("without a probe there is nothing to see, so this should pass: %v", got)
	}
	if ran == 0 {
		t.Fatal("the harness never called the function")
	}
}

func TestUnchangingCatchesAReportedChange(t *testing.T) {
	m := fixedModule("test.claimsachange", func(c *exec.Context, args *value.Map) (Result, error) {
		return Changed("This state reported a change.", Change("old", "new")), nil
	})
	failures := runUnchanging(t, m, nil)
	if len(failures) == 0 {
		t.Fatal("a state declared unchanging that reports a change was accepted")
	}
	if !strings.Contains(failures[0].Msg, "reported changes") {
		t.Errorf("the failure does not name the change: %v", failures)
	}
}

func TestUnchangingCatchesANilResult(t *testing.T) {
	m := fixedModule("test.wouldchange", func(c *exec.Context, args *value.Map) (Result, error) {
		if c.Test {
			return WouldChange("This state would change something.", Change("old", "new")), nil
		}
		return Changed("This state changed something.", Change("old", "new")), nil
	})
	failures := runUnchanging(t, m, nil)
	named := false
	for _, f := range failures {
		if strings.Contains(f.Msg, "nil result") {
			named = true
		}
	}
	if !named {
		t.Errorf("a nil result from an unchanging state was not named: %v", failures)
	}
}

// A state whose answer depends on the mode is the shape of every test-mode
// defect in this repository, and for an unchanging state it is the only
// shape the four runs can see without a probe.
func TestUnchangingCatchesAnAnswerThatDependsOnTheMode(t *testing.T) {
	m := fixedModule("test.twofaced", func(c *exec.Context, args *value.Map) (Result, error) {
		if c.Test {
			return True("This state would do nothing at all."), nil
		}
		return True("This state did nothing at all."), nil
	})
	failures := runUnchanging(t, m, nil)
	named := false
	for _, f := range failures {
		if strings.Contains(f.Msg, "commented") {
			named = true
		}
	}
	if !named {
		t.Errorf("a comment that changes with the mode was not reported: %v", failures)
	}
}

func TestUnchangingCatchesAResultThatDependsOnTheMode(t *testing.T) {
	m := fixedModule("test.twofaced", func(c *exec.Context, args *value.Map) (Result, error) {
		if c.Test {
			return True("This state reports the same thing always."), nil
		}
		return False("This state reports the same thing always."), nil
	})
	failures := runUnchanging(t, m, nil)
	named := false
	for _, f := range failures {
		if strings.Contains(f.Msg, "answered") {
			named = true
		}
	}
	if !named {
		t.Errorf("a result that changes with the mode was not reported: %v", failures)
	}
}

// A failure on purpose is conformant, because `test.fail_without_changes`
// exists and fails by design. Which answer a module gives is an ordinary
// unit test's business; that it gives the same one either way is this
// harness's.
func TestUnchangingAcceptsADeliberateFailure(t *testing.T) {
	m := fixedModule("test.failsonpurpose", func(c *exec.Context, args *value.Map) (Result, error) {
		return False("This state fails on purpose."), nil
	})
	if got := runUnchanging(t, m, nil); len(got) != 0 {
		t.Errorf("a state that fails by design was failed for it: %v", got)
	}
}

// The declaration is a claim about the module, so an unstated reason is a
// silent exemption from every other phase.
func TestUnchangingNeedsAStatedReason(t *testing.T) {
	r := NewRegistry()
	r.Add(unchangingModule("test.nothing"))
	cf := Conformance{
		Name:       "test.nothing",
		Args:       value.MapOf("name", "ignored"),
		Unchanging: true,
	}
	if !hasPhase(cf.Check(r, func(test bool) *exec.Context { return &exec.Context{Test: test} }), "harness") {
		t.Error("Unchanging with no reason was accepted")
	}
}

// The two skips mean opposite things and a case that sets both has not
// decided which contract it wants.
func TestUnchangingAndSkipIdempotenceContradict(t *testing.T) {
	r := NewRegistry()
	r.Add(unchangingModule("test.nothing"))
	cf := Conformance{
		Name:                  "test.nothing",
		Args:                  value.MapOf("name", "ignored"),
		Unchanging:            true,
		UnchangingReason:      "this state exists to do nothing",
		SkipIdempotence:       true,
		SkipIdempotenceReason: "it appends",
	}
	failures := cf.Check(r, func(test bool) *exec.Context { return &exec.Context{Test: test} })
	named := false
	for _, f := range failures {
		if strings.Contains(f.Msg, "contradict") {
			named = true
		}
	}
	if !named {
		t.Errorf("a case setting both skips was accepted: %v", failures)
	}
}

// Setup still runs, because an unchanging state can have something to be
// unchanging about -- a directory whose emptiness is the assertion.
func TestUnchangingRunsSetupAndReportsItsFailure(t *testing.T) {
	r := NewRegistry()
	r.Add(unchangingModule("test.nothing"))
	cf := Conformance{
		Name:             "test.nothing",
		Args:             value.MapOf("name", "ignored"),
		Setup:            func() error { return errBoom{} },
		Unchanging:       true,
		UnchangingReason: "this state exists to do nothing",
	}
	if !hasPhase(cf.Check(r, func(test bool) *exec.Context { return &exec.Context{Test: test} }), "setup") {
		t.Error("a setup that failed was not reported")
	}
}

// ---- what "reads as a sentence" means ----
//
// checkComment used to require an upper-case letter, a digit, or a leading
// slash. The slash was there because a `file` comment opens with the path
// it manages, and the same is true right across this project: a
// `ssh_known_hosts` comment opens with the host, a `pam` comment with the
// module. 289 comment constructions in `internal/builtin` open with a
// substituted value or a lower-case letter, and none had ever failed
// because only a function with a conformance case is checked at all.
//
// So the exception is now what it always stood for. These hold both ends of
// it: what it admits, and what it must still refuse.
func TestACommentMayOpenWithTheThingItManages(t *testing.T) {
	for _, comment := range []string{
		"/etc/motd was written.",
		"host.example.com was added to /root/.ssh/known_hosts.",
		"pam_unix.so was added to sshd's auth chain, at the end.",
		"hello-world is already installed.",
		"nginx.service was restarted.",
		"ed@example.com was granted access.",
		`C:\Windows\Temp\x was removed.`,
	} {
		if err := checkComment(comment); err != nil {
			t.Errorf("checkComment(%q) = %v, and this is the house style", comment, err)
		}
	}
}

func TestACommentStillMustNotBeAFragment(t *testing.T) {
	for _, comment := range []string{
		"changed the thing",
		"the rule was removed from the chain",
		"done, with no problems at all",
		"nothing needed to be done here",
		"ok",
		"",
		"   ",
	} {
		if err := checkComment(comment); err == nil {
			t.Errorf("checkComment(%q) accepted a comment that does not read as a sentence", comment)
		}
	}
}

// A one-word identifier carries the full stop, so the punctuation must not
// be mistaken for the dot that makes it an identifier: "done." is still a
// fragment, and "web.example.com." is still a host.
func TestTrailingPunctuationIsNotAnIdentifier(t *testing.T) {
	if err := checkComment("done, and that is all."); err == nil {
		t.Error("a lower-case English word with a comma after it was accepted")
	}
	if err := checkComment("web.example.com was reachable."); err != nil {
		t.Errorf("a host with a full stop was refused: %v", err)
	}
}
