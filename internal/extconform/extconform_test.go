package extconform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// A conformance harness that has only ever been run against conforming
// extensions establishes nothing: every check here would pass with its
// body deleted. So each rule is driven against an extension that breaks
// exactly that rule, and against one that breaks none.
//
// `testdata/badext` implements the wire by hand rather than through
// `ext`, because the package exists to make these mistakes impossible.
// That makes it a third implementation of the protocol as well as a
// fixture: a rule only the two cooperating implementations agree on is
// a rule the specification does not really carry.

// The modes `testdata/badext` understands. They are literals here and
// constants there; the fixture exits non-zero on a name it does not
// know, so a rename breaks the test loudly rather than quietly handing
// it the conforming behaviour.
const (
	modeGood          = ""
	modeNoName        = "no-name"
	modeNoVersion     = "no-version"
	modeNoFunctions   = "no-functions"
	modeNumericType   = "numeric-type"
	modeUnnamedSig    = "unnamed-signature"
	modeNoResult      = "no-result"
	modeTwoResults    = "two-results"
	modeWrongID       = "wrong-id"
	modeAnyProtocol   = "any-protocol"
	modeAnyKind       = "any-kind"
	modeIgnoreShutdwn = "ignores-shutdown"
	modeUnknownIsOK   = "unknown-is-ok"
	modePrintsToStdou = "prints-to-stdout"
	modeStrictFields  = "strict-fields"
	modeSlowStart     = "slow-start"
	modeBuffers       = "buffers"
)

var (
	buildOnce sync.Once
	badPath   string
	buildErr  error
)

func badExtension(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "badext-*")
		if err != nil {
			buildErr = err
			return
		}
		name := "badext"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		badPath = filepath.Join(dir, name)
		build := exec.Command("go", "build", "-o", badPath, "./testdata/badext")
		build.Stderr = os.Stderr
		buildErr = build.Run()
	})
	if buildErr != nil {
		t.Fatalf("building the fixture: %v", buildErr)
	}
	return badPath
}

// check runs the harness against one mode and returns the results by
// rule name.
func check(t *testing.T, mode string, adjust func(*Options)) map[string]Result {
	t.Helper()
	opts := Options{
		Path: badExtension(t),
		Kind: "module",
		Env:  []string{"BADEXT_MODE=" + mode},
		// Short: every failing mode here is a deliberate hang or an
		// immediate answer, and the suite should not wait ten seconds
		// per hang.
		Timeout: 2 * time.Second,
	}
	if adjust != nil {
		adjust(&opts)
	}
	results, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("the harness could not run: %v", err)
	}
	byRule := map[string]Result{}
	for _, r := range results {
		byRule[r.Rule] = r
	}
	return byRule
}

// mustFail asserts the named rule failed and that the detail says
// something an author could act on.
func mustFail(t *testing.T, byRule map[string]Result, rule string, mentions ...string) {
	t.Helper()
	got, ok := byRule[rule]
	if !ok {
		t.Fatalf("%s was not reported at all; reported: %v", rule, ruleNames(byRule))
	}
	if got.Status != Fail {
		t.Fatalf("%s is %s, want fail (detail: %s)", rule, got.Status, got.Detail)
	}
	if got.Why == "" {
		t.Errorf("%s failed and says nothing about why the rule exists", rule)
	}
	for _, want := range mentions {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("%s detail is %q and does not mention %q", rule, got.Detail, want)
		}
	}
}

func ruleNames(byRule map[string]Result) []string {
	var out []string
	for name := range byRule {
		out = append(out, name)
	}
	return out
}

// The baseline. An extension that conforms fails nothing, or every
// check below is measuring the harness rather than the extension.
func TestAConformingExtensionPassesEverything(t *testing.T) {
	byRule := check(t, modeGood, nil)
	for rule, got := range byRule {
		if got.Status == Fail {
			t.Errorf("%s failed on a conforming extension: %s", rule, got.Detail)
		}
	}
	// And it really ran: a harness reporting nothing would also report
	// no failures.
	pass, fail, skip := countOf(byRule)
	if pass < 8 {
		t.Errorf("only %d rules passed (fail %d, skip %d); the harness checked almost nothing",
			pass, fail, skip)
	}
}

func TestItCatchesAMissingName(t *testing.T) {
	mustFail(t, check(t, modeNoName, nil), "handshake/names-itself")
}

func TestItCatchesAMissingVersion(t *testing.T) {
	mustFail(t, check(t, modeNoVersion, nil), "handshake/declares-a-version")
}

func TestItCatchesAnExtensionThatAnnouncesNothing(t *testing.T) {
	byRule := check(t, modeNoFunctions, nil)
	mustFail(t, byRule, "handshake/announces-functions")
	// And the rules that depend on a function are skipped rather than
	// reported as passing, which would be a quiet lie.
	for _, rule := range []string{"handshake/parameters-are-typed", "call/answers-with-one-result"} {
		if got := byRule[rule]; got.Status != Skip {
			t.Errorf("%s is %s, want skip", rule, got.Status)
		}
	}
}

// The defect of DIVERGENCE 5.79, as a rule. A type serialised as the
// integer an enum is arrives at the host as a refused signature and an
// extension reporting no functions, which is several steps from the
// cause.
func TestItCatchesAParameterTypeSentAsANumber(t *testing.T) {
	byRule := check(t, modeNumericType, nil)
	mustFail(t, byRule, "handshake/parameters-are-typed", "message", "not a string")
	if !strings.Contains(byRule["handshake/parameters-are-typed"].Why, "number") {
		t.Errorf("the reason does not mention the number: %q",
			byRule["handshake/parameters-are-typed"].Why)
	}
}

func TestItCatchesASignatureNamingNeither(t *testing.T) {
	mustFail(t, check(t, modeUnnamedSig, nil), "handshake/signatures-are-named")
}

// A handler that falls off the end without answering is the failure the
// host reports as a hang, several minutes later.
func TestItCatchesACallThatIsNeverAnswered(t *testing.T) {
	mustFail(t, check(t, modeNoResult, nil), "call/answers-with-one-result", "timeout")
}

// Two results put the stream out of step, so every later answer goes to
// the wrong call. It surfaces here as the id of the next answer.
func TestItCatchesACallAnsweredTwice(t *testing.T) {
	byRule := check(t, modeTwoResults, nil)
	if byRule["call/a-failed-call-leaves-it-usable"].Status != Fail &&
		byRule["call/an-unknown-function-is-an-error"].Status != Fail {
		t.Errorf("a doubled result was not caught; reported %v", byRule)
	}
}

func TestItCatchesAResultWithTheWrongID(t *testing.T) {
	mustFail(t, check(t, modeWrongID, nil), "call/the-result-carries-the-call-id")
}

func TestItCatchesAnExtensionThatAnswersAnyProtocol(t *testing.T) {
	mustFail(t, check(t, modeAnyProtocol, nil), "protocol/refuses-another-version")
}

func TestItCatchesAnExtensionThatAnswersAnyKind(t *testing.T) {
	mustFail(t, check(t, modeAnyKind, nil), "protocol/refuses-another-kind")
}

func TestItCatchesAnExtensionThatIgnoresShutdown(t *testing.T) {
	mustFail(t, check(t, modeIgnoreShutdwn, nil), "lifecycle/shutdown-ends-it")
}

// An extension that succeeds at a function it does not have succeeds at
// a misspelt one too.
func TestItCatchesAnUnknownFunctionAnsweredSuccessfully(t *testing.T) {
	mustFail(t, check(t, modeUnknownIsOK, nil), "call/an-unknown-function-is-an-error")
}

// The commonest mistake of all, and it breaks the stream before a frame
// is read: the failure has to name stdout or an author will look
// anywhere else.
func TestItCatchesAPrintToStdout(t *testing.T) {
	byRule := check(t, modePrintsToStdou, nil)
	got, ok := byRule["handshake/answers"]
	if !ok || got.Status != Fail {
		t.Fatalf("a print to stdout was not caught: %+v", got)
	}
	if !strings.Contains(got.Why, "stdout") {
		t.Errorf("the reason does not mention stdout: %q", got.Why)
	}
}

// The rule the whole compatibility policy of SPEC 24.7 rests on, and
// the one most likely to be got wrong by accident: a strict decoder is
// a reasonable instinct, and this is the one place it is wrong.
func TestItCatchesAnExtensionThatRefusesAnUnknownField(t *testing.T) {
	mustFail(t, check(t, modeStrictFields, nil), "protocol/ignores-an-unknown-field")
}

// A slow start is not a defect, and it is not buffering.
//
// The first frame of a session is the only one whose wait includes
// starting the process: exec, the dynamic loader, a runtime or an
// interpreter coming up, and on macOS the first-exec assessment of a
// binary the test has just linked. Under a full `make check` that
// took longer than the two seconds this suite gives one exchange, and
// a conforming extension failed handshake/answers with the advice to
// flush -- which it already did. The fixture sleeps for longer than an
// exchange before reading anything, which is that load spike made
// deterministic: it fails every time against a harness that times the
// handshake by the exchange timeout, and passes against one that gives
// the start its own allowance.
func TestASlowStartIsNotAFailure(t *testing.T) {
	byRule := check(t, modeSlowStart, nil)
	for rule, got := range byRule {
		if got.Status == Fail {
			t.Errorf("%s failed on an extension that only started slowly: %s", rule, got.Detail)
		}
	}
	// Not covered here: the refusal rules pass on silence, so the old
	// harness also passed them for a slow extension that would have
	// accepted the wrong version, had it been waited for. A conforming
	// fixture refuses either way and cannot show that half.
	if pass, _, _ := countOf(byRule); pass < 8 {
		t.Errorf("only %d rules passed; the slow start was not waited out", pass)
	}
}

// The other half of the same distinction: a start allowance that was
// only a bigger number would make the harness wait longer and still
// say "buffering" about a process that never got going, and it would
// lose nothing for a buffering one. What establishes buffering is
// closing stdin and watching the held frames arrive -- a runtime
// flushes on exit -- so a buffering extension is named as one, with
// evidence, and nothing else is.
func TestItCatchesAnExtensionThatBuffersItsOutput(t *testing.T) {
	// A short start allowance, because a buffering extension waits it
	// out in full and the suite should not. The diagnosis does not
	// depend on it: a start slower than this still ends in the held
	// frames arriving when stdin closes.
	byRule := check(t, modeBuffers, func(o *Options) { o.StartTimeout = 2 * time.Second })
	mustFail(t, byRule, "handshake/answers", "only once its stdin was closed", "flush after every frame")
	t.Logf("handshake/answers: %s", byRule["handshake/answers"].Detail)
}

// And the converse: a process that had not read the hello when the
// allowance ran out is not called a buffering one. This is the slow
// starter given an allowance shorter than its start, which is exactly
// the case the old message got wrong. Closing stdin alone would not
// tell it apart -- the hello is still in the pipe, so it would answer
// after the close just as a buffering one does -- which is why the
// probe looks at what is left in the pipe first.
func TestAStartLongerThanTheAllowanceIsNotCalledBuffering(t *testing.T) {
	byRule := check(t, modeSlowStart, func(o *Options) { o.StartTimeout = time.Second })
	got := byRule["handshake/answers"]
	if got.Status != Fail {
		t.Fatalf("handshake/answers is %s, want fail (detail: %s)", got.Status, got.Detail)
	}
	t.Logf("handshake/answers: %s", got.Detail)
	if strings.Contains(got.Detail, "buffer") {
		t.Errorf("a process that had not finished starting was diagnosed as buffering: %q", got.Detail)
	}
	// Positively, not just "not buffering": a vaguer message that
	// avoids the wrong word is still a guess, and the probe exists to
	// replace guesses with what the pipe shows.
	if !strings.Contains(got.Detail, "had not read the hello") ||
		!strings.Contains(got.Detail, "still starting") {
		t.Errorf("the detail does not say the hello was never read: %q", got.Detail)
	}
}

// The kind rules cannot be checked without being told what the kind is,
// and a skip says so rather than passing quietly.
func TestTheKindRuleSkipsWhenNoKindIsGiven(t *testing.T) {
	byRule := check(t, modeGood, func(o *Options) { o.Kind = "" })
	got := byRule["protocol/refuses-another-kind"]
	if got.Status != Skip {
		t.Errorf("it is %s, want skip", got.Status)
	}
	if !strings.Contains(got.Detail, "kind") {
		t.Errorf("the skip does not say why: %q", got.Detail)
	}
}

// A file that will not start is an error rather than a page of
// failures, because every rule would fail for the same reason.
func TestAnExtensionThatWillNotStartIsAnError(t *testing.T) {
	_, err := Run(context.Background(), Options{Path: filepath.Join(t.TempDir(), "nothing")})
	if err == nil {
		t.Fatal("a missing file produced results")
	}
}

func countOf(byRule map[string]Result) (pass, fail, skip int) {
	var list []Result
	for _, r := range byRule {
		list = append(list, r)
	}
	return Counts(list)
}

// Failed is what an exit code is made of, so it has to mean what it
// says.
func TestFailedReportsAnyFailure(t *testing.T) {
	if Failed([]Result{{Status: Pass}, {Status: Skip}}) {
		t.Error("pass and skip reported a failure")
	}
	if !Failed([]Result{{Status: Pass}, {Status: Fail}}) {
		t.Error("a failure was not reported")
	}
}
