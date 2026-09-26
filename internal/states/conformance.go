package states

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// Conformance is the shared harness of SPEC section 11.6 that every state
// module must pass.
//
// It asserts three things a state module has to get right and that nothing
// else in the system can check for it:
//
//   - In test mode the function makes no change, returns a nil result when
//     it would change something, populates changes with the prediction,
//     and populates comment with a human sentence.
//   - A second run against the state the first run produced reports no
//     changes. A module that is not idempotent is a module that will churn
//     a fleet forever.
//   - When the system already matches, the function reports success with
//     no changes rather than a change of nothing.
//
// Salt has no such harness, which is exactly why test=True in Salt is
// unreliable for a nontrivial fraction of its modules.
type Conformance struct {
	// Name is the `module.function` under test.
	Name string
	// Args are the arguments to apply.
	Args *value.Map
	// Setup puts the system into the "not yet applied" state. It runs
	// before each phase, and must be idempotent itself.
	Setup func() error
	// Cleanup runs after the case.
	Cleanup func()
	// Probe reports the observable state the function manages, as a string
	// to compare. It is optional, and it is the direct check that test
	// mode changed nothing: the harness otherwise has to infer that from
	// the module's own answers.
	Probe func() (string, error)
	// SkipIdempotence marks a function whose second run legitimately
	// differs, such as one that appends. It must be justified in the case
	// where it is set.
	SkipIdempotence bool
	// SkipIdempotenceReason is required whenever SkipIdempotence is set.
	SkipIdempotenceReason string
	// Unchanging marks a function that never changes anything, and
	// switches the harness to the contract such a function actually has.
	//
	// The phases above all assume a state with something to do: phase 1
	// fails a function that reports success, because a case whose setup
	// left nothing to change is testing nothing. That assumption held for
	// every case until `cmd.wait` and `module.wait`, whose whole purpose
	// is to do nothing unless a watch requisite fires, and the `test.*`
	// fakes that exist to report a fixed answer. Six functions were
	// listed as having no case for a reason that was really "the harness
	// cannot express this one".
	//
	// What is checked instead is what can go wrong with such a function:
	// it must never return nil, never report a change, and answer
	// identically whether or not test mode is on. The last is the one
	// that matters -- a `cmd.wait` that ran its command under `--test`
	// would be the same defect as any other state changing the system in
	// test mode, and with a Probe the harness sees it directly.
	//
	// Whether the answer is success or failure is deliberately not
	// checked: `test.fail_without_changes` fails on purpose, and which
	// answer a module gives is an ordinary unit test's business.
	Unchanging bool
	// UnchangingReason is required whenever Unchanging is set, because
	// "this state changes nothing" is a claim about the module, and a
	// wrong one silently exempts it from every phase above.
	UnchangingReason string
}

// Failure is one conformance violation.
type Failure struct {
	Phase string
	Msg   string
}

func (f Failure) String() string { return f.Phase + ": " + f.Msg }

// Check runs the harness against a registry and returns every violation.
func (cf Conformance) Check(r *Registry, newContext func(test bool) *exec.Context) []Failure {
	var failures []Failure
	fail := func(phase, format string, args ...any) {
		failures = append(failures, Failure{Phase: phase, Msg: fmt.Sprintf(format, args...)})
	}
	if cf.Cleanup != nil {
		defer cf.Cleanup()
	}

	mod, ok := r.Lookup(cf.Name)
	if !ok {
		fail("lookup", "%s is not registered", cf.Name)
		return failures
	}
	if cf.SkipIdempotence && cf.SkipIdempotenceReason == "" {
		fail("harness", "SkipIdempotence needs a stated reason")
	}
	if cf.Unchanging {
		if cf.UnchangingReason == "" {
			fail("harness", "Unchanging needs a stated reason")
		}
		if cf.SkipIdempotence {
			fail("harness", "Unchanging and SkipIdempotence contradict each other: "+
				"a function that changes nothing cannot have a second run that legitimately differs")
		}
		return append(failures, cf.checkUnchanging(r, newContext)...)
	}

	// Phase 1: test mode against a system that does not yet match.
	if cf.Setup != nil {
		if err := cf.Setup(); err != nil {
			fail("setup", "%v", err)
			return failures
		}
	}
	var beforeTest string
	if cf.Probe != nil {
		var err error
		if beforeTest, err = cf.Probe(); err != nil {
			fail("setup", "probe failed: %v", err)
			return failures
		}
	}
	testRes, err := r.Call(newContext(true), cf.Name, cf.Args)
	if err != nil {
		fail("test mode", "returned an error: %v", err)
		return failures
	}
	if testRes.Result != nil && *testRes.Result {
		// The module says the system already matched. That is legitimate
		// only if the setup genuinely left nothing to do, and a harness
		// case that does that is testing nothing.
		fail("test mode", "reported success with nothing to do; the setup should leave a change to make")
	}
	if testRes.Result == nil && !testRes.HasChanges() {
		fail("test mode", "returned a nil result but predicted no changes; the prediction is the point of test mode")
	}
	if err := checkComment(testRes.Comment); err != nil {
		fail("test mode", "%v", err)
	}

	// Phase 1b: test mode is asked the same question again, with no setup
	// in between. A function that honoured test mode changed nothing, so
	// it must still say the system does not match. One that quietly
	// applied its change now reports there is nothing to do, which is how
	// the contract's central promise is caught without the harness
	// needing to know anything about the system being managed.
	//
	// Probe, when a case supplies one, checks the same thing directly.
	repeat, err := r.Call(newContext(true), cf.Name, cf.Args)
	if err != nil {
		fail("test mode", "returned an error when asked a second time: %v", err)
		return failures
	}
	if repeat.Result != nil && *repeat.Result {
		fail("test mode", "changed the system: a second test-mode run reports there is nothing left to do")
	}
	if cf.Probe != nil {
		after, err := cf.Probe()
		if err != nil {
			fail("test mode", "probe failed: %v", err)
		} else if after != beforeTest {
			fail("test mode", "changed the system: the probe went from %q to %q", beforeTest, after)
		}
	}

	// Phase 2: apply for real, and confirm the prediction was honest.
	if cf.Setup != nil {
		if err := cf.Setup(); err != nil {
			fail("setup", "%v", err)
			return failures
		}
	}
	applyRes, err := r.Call(newContext(false), cf.Name, cf.Args)
	if err != nil {
		fail("apply", "returned an error: %v", err)
		return failures
	}
	if applyRes.Result == nil {
		fail("apply", "returned a nil result outside test mode; nil means `would change`")
	}
	if applyRes.Failed() {
		fail("apply", "failed: %s", applyRes.Comment)
		return failures
	}
	if !applyRes.HasChanges() {
		fail("apply", "reported no changes, but test mode predicted some")
	}
	if err := checkComment(applyRes.Comment); err != nil {
		fail("apply", "%v", err)
	}
	if err := checkChangeShape(applyRes.Changes); err != nil {
		fail("apply", "%v", err)
	}

	if cf.SkipIdempotence {
		return failures
	}

	// Phase 3: a second run changes nothing.
	secondRes, err := r.Call(newContext(false), cf.Name, cf.Args)
	if err != nil {
		fail("second run", "returned an error: %v", err)
		return failures
	}
	if !secondRes.Succeeded() {
		fail("second run", "failed: %s", secondRes.Comment)
	}
	if secondRes.HasChanges() {
		fail("second run", "reported changes on an already-applied state: %s", renderChanges(secondRes.Changes))
	}

	// Phase 4: test mode against a system that already matches reports
	// success rather than a nil result, so that a test run does not
	// invent work.
	testAgain, err := r.Call(newContext(true), cf.Name, cf.Args)
	if err != nil {
		fail("test mode, already applied", "returned an error: %v", err)
		return failures
	}
	if testAgain.Result == nil {
		fail("test mode, already applied", "predicted a change against a system that already matches")
	}
	if testAgain.HasChanges() {
		fail("test mode, already applied", "predicted changes: %s", renderChanges(testAgain.Changes))
	}

	// The declared test mode honesty must match what the harness saw.
	if mod.Sig.TestMode == signature.TestNotApplicable && applyRes.HasChanges() {
		fail("signature", "declares test_mode not_applicable but the function changes the system")
	}
	if mod.Sig.Mutates == false && applyRes.HasChanges() {
		fail("signature", "declares mutates false but the function changed the system")
	}
	return failures
}

// checkComment holds a comment to being a human sentence, because a
// comment is what an operator reads when a state did something
// unexpected.
func checkComment(comment string) error {
	trimmed := strings.TrimSpace(comment)
	if trimmed == "" {
		return fmt.Errorf("returned an empty comment; SPEC section 11.6 requires a human sentence")
	}
	if len(trimmed) < 10 {
		return fmt.Errorf("comment %q is too short to be a sentence", trimmed)
	}
	first := []rune(trimmed)[0]
	if !unicode.IsUpper(first) && !unicode.IsDigit(first) && !startsWithIdentifier(trimmed) {
		return fmt.Errorf("comment %q should read as a sentence", trimmed)
	}
	return nil
}

// startsWithIdentifier reports whether a comment opens with the thing the
// state manages rather than with a word.
//
// The rule above used to be "upper case, a digit, or a slash", and the
// slash was there because a `file` comment opens with the path. That is the
// house style throughout: `ssh_known_hosts` says "host.example.com was
// added to …", `pam` says "pam_unix.so was added to …", `snap` opens with
// the snap's name. A grep of this package finds 289 comment constructions
// opening with a substituted value or a lower-case letter, and the reason
// none of them had ever failed is that only the functions with a
// conformance case are checked at all -- so the check and the code it
// checks disagreed, and the disagreement was invisible.
//
// The slash was the same exception written narrowly, so this is that
// exception widened to what it was always standing for: a first word that
// is an identifier, a path or a hostname rather than English. What stays
// rejected is the shape the check exists for -- a comment that opens with
// a lower-case English word, which is either a fragment ("changed",
// "done") or a sentence somebody started in the middle ("the rule was
// removed").
func startsWithIdentifier(comment string) bool {
	word := comment
	if i := strings.IndexFunc(comment, unicode.IsSpace); i > 0 {
		word = comment[:i]
	}
	// Trailing sentence punctuation is not part of the identifier, and a
	// one-word comment carries it: "web.example.com."
	word = strings.TrimRight(word, ".,:;!?")
	if word == "" {
		return false
	}
	return strings.ContainsAny(word, "./\\-_:@")
}

// checkChangeShape holds the changes mapping to Salt's {old, new} shape,
// which every dashboard and returner parses.
func checkChangeShape(changes *value.Map) error {
	if changes == nil {
		return nil
	}
	for _, e := range changes.Entries() {
		sub, ok := e.Val.(*value.Map)
		if !ok {
			// A scalar change value is accepted; Salt emits both shapes.
			continue
		}
		hasOld, hasNew := sub.Has("old"), sub.Has("new")
		if hasOld != hasNew && sub.Len() <= 2 {
			return fmt.Errorf("change %q has only one of old and new; both belong in the pair",
				value.KeyString(e.Key))
		}
	}
	return nil
}

func renderChanges(m *value.Map) string {
	if m == nil || m.Len() == 0 {
		return "{}"
	}
	var parts []string
	for _, e := range m.Entries() {
		parts = append(parts, fmt.Sprintf("%s=%v", value.KeyString(e.Key), e.Val))
	}
	return strings.Join(parts, " ")
}

// checkUnchanging is the contract of a state that changes nothing: it is
// what the phases in Check reduce to when there is never anything to do.
//
// The interesting defect for such a function is not idempotence, which is
// free, but a difference between test mode and a real run. `cmd.wait` runs
// its command only when a watch requisite fires, and a `cmd.wait` that ran
// it anyway would run it under `--test` too -- an operator asking what a
// highstate would do, and having it done. So every check here is applied
// to both modes and then the two answers are compared, and a Probe is the
// direct form of the same question.
//
// Four runs rather than two because the alternation is the point: a
// function that changes something on its first call and not its second
// would pass a single pair.
func (cf Conformance) checkUnchanging(r *Registry, newContext func(test bool) *exec.Context) []Failure {
	var failures []Failure
	fail := func(phase, format string, args ...any) {
		failures = append(failures, Failure{Phase: phase, Msg: fmt.Sprintf(format, args...)})
	}

	if cf.Setup != nil {
		if err := cf.Setup(); err != nil {
			fail("setup", "%v", err)
			return failures
		}
	}
	var before string
	if cf.Probe != nil {
		var err error
		if before, err = cf.Probe(); err != nil {
			fail("setup", "probe failed: %v", err)
			return failures
		}
	}

	phases := []struct {
		name string
		test bool
	}{
		{"test mode", true},
		{"apply", false},
		{"test mode, repeated", true},
		{"second run", false},
	}
	var answers []Result
	for _, phase := range phases {
		res, err := r.Call(newContext(phase.test), cf.Name, cf.Args)
		if err != nil {
			fail(phase.name, "returned an error: %v", err)
			return failures
		}
		if res.Result == nil {
			fail(phase.name, "returned a nil result, which means `would change`, from a state "+
				"this case declares changes nothing (%s)", cf.UnchangingReason)
		}
		if res.HasChanges() {
			fail(phase.name, "reported changes: %s. This case declares the state changes nothing (%s), "+
				"so either the declaration is wrong or the state is",
				renderChanges(res.Changes), cf.UnchangingReason)
		}
		if err := checkComment(res.Comment); err != nil {
			fail(phase.name, "%v", err)
		}
		answers = append(answers, res)
	}

	// The comparison, which is the whole reason for running four times.
	// Comment as well as result: a state that changes nothing has nothing
	// to describe differently, and a comment that varies with the mode is
	// a sign of a branch that does.
	for i, res := range answers[1:] {
		name := phases[i+1].name
		if res.ResultString() != answers[0].ResultString() {
			fail(name, "answered %q where the first test-mode run answered %q; "+
				"a state that changes nothing has no reason to answer differently",
				res.ResultString(), answers[0].ResultString())
		}
		if res.Comment != answers[0].Comment {
			fail(name, "commented %q where the first test-mode run commented %q",
				res.Comment, answers[0].Comment)
		}
	}

	if cf.Probe != nil {
		after, err := cf.Probe()
		if err != nil {
			fail("probe", "probe failed: %v", err)
		} else if after != before {
			fail("probe", "the system changed from %q to %q across four runs of a state "+
				"that changes nothing (%s)", before, after, cf.UnchangingReason)
		}
	}
	return failures
}
