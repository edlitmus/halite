package states

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// The three states a run can be in. Result is a pointer precisely so that
// "test mode, and this would have changed something" has a representation
// distinct from both success and failure.
func TestTheThreeResultStates(t *testing.T) {
	cases := []struct {
		name              string
		res               Result
		succeeded, failed bool
		str               string
	}{
		{"true", True("Nothing to do here."), true, false, "succeeded"},
		{"false", False("It did not work."), false, true, "failed"},
		{"changed", Changed("Installed nginx.", Change(nil, "1.2")), true, false, "succeeded"},
		{"would change", WouldChange("Would install nginx.", Change(nil, "1.2")), true, false, "would change"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.res.Succeeded(); got != c.succeeded {
				t.Errorf("Succeeded = %v", got)
			}
			if got := c.res.Failed(); got != c.failed {
				t.Errorf("Failed = %v", got)
			}
			if got := c.res.ResultString(); got != c.str {
				t.Errorf("ResultString = %q, want %q", got, c.str)
			}
		})
	}
}

// A nil result counts as a success for requisites, because the state did
// not fail: a `require` on it in test mode must not cascade into failures.
func TestANilResultDoesNotFailARequisite(t *testing.T) {
	r := WouldChange("Would write the file.", Change("", "content"))
	if r.Failed() || !r.Succeeded() {
		t.Error("a test-mode prediction should not read as a failure")
	}
	if r.Result != nil {
		t.Error("WouldChange must leave Result nil")
	}
}

func TestHasChanges(t *testing.T) {
	if True("Nothing to do here.").HasChanges() {
		t.Error("a plain success carries no changes")
	}
	if Changed("Did the thing.", value.NewMap(0)).HasChanges() {
		t.Error("an empty changes mapping is not a change")
	}
	if !Changed("Did the thing.", Change("a", "b")).HasChanges() {
		t.Error("a populated changes mapping is a change")
	}
}

func TestChangeBuildsTheOldNewPair(t *testing.T) {
	c := Change("1.0", "2.0")
	old, _ := c.Get("old")
	new, _ := c.Get("new")
	if old != "1.0" || new != "2.0" {
		t.Errorf("Change = %v", c.StringKeys())
	}
	if got := c.StringKeys(); len(got) != 2 || got[0] != "old" {
		t.Errorf("old should come first, got %v", got)
	}
}

// ---- argument helpers ----

func TestArgumentHelpersCoerceAndDefault(t *testing.T) {
	args := value.MapOf(
		"name", "/etc/rc.conf",
		"count", int64(3),
		"ratio", 1.9,
		"flag", "yes",
		"off", false,
		"null", nil,
		"one", "single",
		"many", []any{"a", int64(2)},
		"m", value.MapOf("k", "v"),
		"notalist", int64(1),
	)

	if got := Str(args, "name", "def"); got != "/etc/rc.conf" {
		t.Errorf("Str = %q", got)
	}
	// A non-string renders rather than falling back, so `mode: 644`
	// reaches the module as "644" instead of the default.
	if got := Str(args, "count", "def"); got != "3" {
		t.Errorf("Str of an int = %q", got)
	}
	for _, name := range []string{"missing", "null"} {
		if got := Str(args, name, "def"); got != "def" {
			t.Errorf("Str(%s) = %q, want the default", name, got)
		}
	}

	if got := Int(args, "count", 0); got != 3 {
		t.Errorf("Int = %d", got)
	}
	if got := Int(args, "ratio", 0); got != 1 {
		t.Errorf("Int of a float = %d", got)
	}
	for _, name := range []string{"missing", "null", "name"} {
		if got := Int(args, name, 42); got != 42 {
			t.Errorf("Int(%s) = %d, want the default", name, got)
		}
	}

	if !Bool(args, "flag", false) || Bool(args, "off", true) {
		t.Error("Bool disagrees with the arguments")
	}
	for _, name := range []string{"missing", "null"} {
		if !Bool(args, name, true) {
			t.Errorf("Bool(%s) should fall back to the default", name)
		}
	}

	// A bare string is a one-item list, which is how `pkgs: nginx` works.
	if got := Strings(args, "one"); len(got) != 1 || got[0] != "single" {
		t.Errorf("Strings of a scalar = %v", got)
	}
	if got := Strings(args, "many"); len(got) != 2 || got[1] != "2" {
		t.Errorf("Strings = %v", got)
	}
	for _, name := range []string{"missing", "null", "notalist"} {
		if got := Strings(args, name); got != nil {
			t.Errorf("Strings(%s) = %v, want nil", name, got)
		}
	}

	if got := Mapping(args, "m"); got == nil || !got.Has("k") {
		t.Errorf("Mapping = %v", got)
	}
	for _, name := range []string{"missing", "name"} {
		if got := Mapping(args, name); got != nil {
			t.Errorf("Mapping(%s) = %v, want nil", name, got)
		}
	}
}

func TestSortedNamesDoesNotDisturbItsInput(t *testing.T) {
	in := []string{"zebra", "apple"}
	if got := SortedNames(in); got != "apple, zebra" {
		t.Errorf("SortedNames = %q", got)
	}
	if in[0] != "zebra" {
		t.Errorf("SortedNames sorted its caller's slice: %v", in)
	}
}

// ---- registry ----

func fixedModule(name string, fn Func) Module {
	module, function, _ := strings.Cut(name, ".")
	return Module{
		Sig: signature.Signature{Module: module, Function: function, Mutates: true, Params: []signature.Param{
			{Name: "name", Type: signature.String, Required: true},
		}},
		Fn: fn,
	}
}

func TestRegistryLookupAndInventory(t *testing.T) {
	r := NewRegistry()
	r.Add(
		fixedModule("file.managed", func(*exec.Context, *value.Map) (Result, error) {
			return True("Nothing to do here."), nil
		}),
		fixedModule("service.running", func(*exec.Context, *value.Map) (Result, error) {
			return True("Nothing to do here."), nil
		}),
	)

	if !r.Has("file.managed") || r.Has("file.nope") {
		t.Error("Has disagrees with the registry")
	}
	if _, ok := r.Lookup("file.managed"); !ok {
		t.Error("Lookup missed a registered function")
	}
	if _, ok := r.Lookup("file.nope"); ok {
		t.Error("Lookup found something unregistered")
	}
	if got := r.Names(); len(got) != 2 || got[0] > got[1] {
		t.Errorf("Names = %v", got)
	}
	if got := r.Modules(); len(got) != 2 {
		t.Errorf("Modules = %v", got)
	}
	if _, err := r.Call(&exec.Context{}, "file.nope", value.NewMap(0)); err == nil {
		t.Error("calling an unregistered function should be an error")
	}
	// A missing required argument is an error rather than an empty name.
	if _, err := r.Call(&exec.Context{}, "file.managed", value.NewMap(0)); err == nil {
		t.Error("a missing required argument should be an error")
	}
}

// A `watch` on a module with no mod_watch must be reportable, so that it
// is not silently downgraded to `require`.
func TestWatchSupportIsVisible(t *testing.T) {
	r := NewRegistry()
	watched := fixedModule("service.running", func(*exec.Context, *value.Map) (Result, error) {
		return True("Already running."), nil
	})
	watched.ModWatch = func(*exec.Context, *value.Map) (Result, error) {
		return Changed("Restarted the service.", Change("running", "restarted")), nil
	}
	r.Add(watched, fixedModule("file.managed", func(*exec.Context, *value.Map) (Result, error) {
		return True("Nothing to do here."), nil
	}))

	if !r.SupportsWatch("service.running") {
		t.Error("service.running defines mod_watch")
	}
	if r.SupportsWatch("file.managed") || r.SupportsWatch("nope.nope") {
		t.Error("SupportsWatch should be false without a mod_watch")
	}

	args := value.MapOf("name", "nginx")
	res, err := r.CallWatch(&exec.Context{}, "service.running", args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Error("CallWatch should run the mod_watch reaction")
	}

	// Without a mod_watch, CallWatch falls back to the function itself.
	res, err = r.CallWatch(&exec.Context{}, "file.managed", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Error("the fallback should have run the plain function")
	}
	if _, err := r.CallWatch(&exec.Context{}, "nope.nope", args); err == nil {
		t.Error("CallWatch on an unknown function should be an error")
	}
}

// ---- the conformance harness itself ----
//
// The harness is what holds every state module to SPEC section 11.6, so it
// has to be shown to catch a dishonest module. Each case below registers a
// module that breaks the contract in exactly one way and asserts the
// harness names it.

// conformingModule is a correct implementation: it writes a value into a
// backing store, honours test mode, and is idempotent.
func conformingModule(store *string) Module {
	m := fixedModule("test.set", nil)
	m.Fn = func(c *exec.Context, args *value.Map) (Result, error) {
		want := Str(args, "name", "")
		if *store == want {
			return True("The value is already set."), nil
		}
		if c.Test {
			return WouldChange("Would set the value.", Change(*store, want)), nil
		}
		old := *store
		*store = want
		return Changed("Set the value.", Change(old, want)), nil
	}
	return m
}

func runHarness(t *testing.T, m Module, setup func() error) []Failure {
	t.Helper()
	r := NewRegistry()
	r.Add(m)
	cf := Conformance{
		Name:  m.Sig.Name(),
		Args:  value.MapOf("name", "wanted"),
		Setup: setup,
	}
	return cf.Check(r, func(test bool) *exec.Context { return &exec.Context{Test: test} })
}

func TestHarnessPassesAConformingModule(t *testing.T) {
	store := "initial"
	failures := runHarness(t, conformingModule(&store), func() error { store = "initial"; return nil })
	if len(failures) != 0 {
		t.Errorf("a conforming module was failed: %v", failures)
	}
	if store != "wanted" {
		t.Errorf("the harness did not leave the change applied: %q", store)
	}
}

// The failure Salt actually has: test mode changes the system anyway.
func TestHarnessCatchesTestModeThatChangesTheSystem(t *testing.T) {
	store := "initial"
	m := fixedModule("test.set", func(c *exec.Context, args *value.Map) (Result, error) {
		want := Str(args, "name", "")
		if store == want {
			return True("The value is already set."), nil
		}
		old := store
		store = want // the bug: no test-mode guard
		if c.Test {
			return WouldChange("Would set the value.", Change(old, want)), nil
		}
		return Changed("Set the value.", Change(old, want)), nil
	})
	failures := runHarness(t, m, func() error { store = "initial"; return nil })
	if !hasPhase(failures, "test mode") {
		t.Fatalf("the harness accepted a module that changes the system in test mode: %v", failures)
	}

	// With a probe the harness names what moved, rather than inferring it
	// from the module's own second answer.
	store = "initial"
	r := NewRegistry()
	r.Add(m)
	cf := Conformance{
		Name:  "test.set",
		Args:  value.MapOf("name", "wanted"),
		Setup: func() error { store = "initial"; return nil },
		Probe: func() (string, error) { return store, nil },
	}
	got := cf.Check(r, func(test bool) *exec.Context { return &exec.Context{Test: test} })
	named := false
	for _, f := range got {
		if strings.Contains(f.Msg, "probe went from") {
			named = true
		}
	}
	if !named {
		t.Errorf("the probe did not report the change: %v", got)
	}
}

func TestProbeIsSatisfiedByAConformingModule(t *testing.T) {
	store := "initial"
	r := NewRegistry()
	r.Add(conformingModule(&store))
	cf := Conformance{
		Name:  "test.set",
		Args:  value.MapOf("name", "wanted"),
		Setup: func() error { store = "initial"; return nil },
		Probe: func() (string, error) { return store, nil },
	}
	if got := cf.Check(r, func(test bool) *exec.Context { return &exec.Context{Test: test} }); len(got) != 0 {
		t.Errorf("a conforming module failed with a probe: %v", got)
	}
}

func TestAFailingProbeIsReported(t *testing.T) {
	store := "initial"
	r := NewRegistry()
	r.Add(conformingModule(&store))
	cf := Conformance{
		Name:  "test.set",
		Args:  value.MapOf("name", "wanted"),
		Setup: func() error { store = "initial"; return nil },
		Probe: func() (string, error) { return "", errBoom{} },
	}
	if !hasPhase(cf.Check(r, func(test bool) *exec.Context { return &exec.Context{Test: test} }), "setup") {
		t.Error("a probe that cannot read the system should be reported")
	}
}

func TestHarnessCatchesADishonestPrediction(t *testing.T) {
	// Test mode says nothing will change, then the apply changes things.
	store := "initial"
	m := fixedModule("test.set", func(c *exec.Context, args *value.Map) (Result, error) {
		want := Str(args, "name", "")
		if c.Test {
			return True("Nothing needs to change."), nil
		}
		if store == want {
			return True("The value is already set."), nil
		}
		old := store
		store = want
		return Changed("Set the value.", Change(old, want)), nil
	})
	failures := runHarness(t, m, func() error { store = "initial"; return nil })
	if !hasPhase(failures, "test mode") {
		t.Errorf("expected a test mode failure, got %v", failures)
	}
}

func TestHarnessCatchesANonIdempotentModule(t *testing.T) {
	// It reports a change every time, which is the behaviour that makes a
	// highstate never converge.
	calls := 0
	m := fixedModule("test.set", func(c *exec.Context, args *value.Map) (Result, error) {
		calls++
		if c.Test {
			return WouldChange("Would set the value.", Change("a", "b")), nil
		}
		return Changed("Set the value.", Change("a", "b")), nil
	})
	failures := runHarness(t, m, nil)
	if !hasPhase(failures, "second run") {
		t.Errorf("expected a second run failure, got %v", failures)
	}
}

func TestHarnessCatchesANilResultOutsideTestMode(t *testing.T) {
	m := fixedModule("test.set", func(c *exec.Context, args *value.Map) (Result, error) {
		return WouldChange("Would set the value.", Change("a", "b")), nil
	})
	failures := runHarness(t, m, nil)
	if !hasPhase(failures, "apply") {
		t.Errorf("expected an apply failure, got %v", failures)
	}
}

func TestHarnessCatchesAnUnusableComment(t *testing.T) {
	for _, comment := range []string{"", "   ", "ok", "did it", "\ttiny"} {
		store := "initial"
		m := fixedModule("test.set", func(c *exec.Context, args *value.Map) (Result, error) {
			want := Str(args, "name", "")
			if store == want {
				return True("The value is already set."), nil
			}
			if c.Test {
				return WouldChange(comment, Change(store, want)), nil
			}
			old := store
			store = want
			return Changed("Set the value.", Change(old, want)), nil
		})
		failures := runHarness(t, m, func() error { store = "initial"; return nil })
		if len(failures) == 0 {
			t.Errorf("the comment %q was accepted", comment)
		}
	}
	// A path or a digit may legitimately open a sentence.
	for _, comment := range []string{"/etc/rc.conf would be created.", "3 packages would be installed."} {
		if err := checkComment(comment); err != nil {
			t.Errorf("%q was wrongly refused: %v", comment, err)
		}
	}
}

func TestHarnessCatchesAHalfChangePair(t *testing.T) {
	store := "initial"
	m := fixedModule("test.set", func(c *exec.Context, args *value.Map) (Result, error) {
		want := Str(args, "name", "")
		if store == want {
			return True("The value is already set."), nil
		}
		if c.Test {
			return WouldChange("Would set the value.", Change(store, want)), nil
		}
		store = want
		// Only `new`, which every dashboard parsing {old, new} will
		// render as a change from nothing.
		return Changed("Set the value.", value.MapOf("value", value.MapOf("new", want))), nil
	})
	failures := runHarness(t, m, func() error { store = "initial"; return nil })
	if !hasPhase(failures, "apply") {
		t.Errorf("expected an apply failure, got %v", failures)
	}
}

func TestHarnessCatchesAnErrorInEachPhase(t *testing.T) {
	// A module that errors rather than returning a failed Result.
	m := fixedModule("test.set", func(c *exec.Context, args *value.Map) (Result, error) {
		return Result{}, errBoom{}
	})
	failures := runHarness(t, m, nil)
	if !hasPhase(failures, "test mode") {
		t.Errorf("expected a test mode failure, got %v", failures)
	}
}

func TestHarnessReportsAnUnregisteredFunction(t *testing.T) {
	cf := Conformance{Name: "nope.nope", Args: value.NewMap(0)}
	failures := cf.Check(NewRegistry(), func(bool) *exec.Context { return &exec.Context{} })
	if !hasPhase(failures, "lookup") {
		t.Errorf("expected a lookup failure, got %v", failures)
	}
}

func TestHarnessReportsAFailingSetup(t *testing.T) {
	store := "initial"
	failures := runHarness(t, conformingModule(&store), func() error { return errBoom{} })
	if !hasPhase(failures, "setup") {
		t.Errorf("expected a setup failure, got %v", failures)
	}
}

// SkipIdempotence is the harness's own escape hatch, so it must not be
// usable without a stated reason.
func TestSkipIdempotenceNeedsAReason(t *testing.T) {
	store := "initial"
	r := NewRegistry()
	r.Add(conformingModule(&store))
	newCtx := func(test bool) *exec.Context { return &exec.Context{Test: test} }
	setup := func() error { store = "initial"; return nil }

	cf := Conformance{Name: "test.set", Args: value.MapOf("name", "wanted"), Setup: setup, SkipIdempotence: true}
	if !hasPhase(cf.Check(r, newCtx), "harness") {
		t.Error("SkipIdempotence with no reason should be refused")
	}

	cf.SkipIdempotenceReason = "the function appends, so a second run legitimately differs"
	if got := cf.Check(r, newCtx); len(got) != 0 {
		t.Errorf("a justified skip was still refused: %v", got)
	}
}

func TestHarnessRunsCleanup(t *testing.T) {
	store := "initial"
	cleaned := false
	r := NewRegistry()
	r.Add(conformingModule(&store))
	cf := Conformance{
		Name:    "test.set",
		Args:    value.MapOf("name", "wanted"),
		Setup:   func() error { store = "initial"; return nil },
		Cleanup: func() { cleaned = true },
	}
	cf.Check(r, func(test bool) *exec.Context { return &exec.Context{Test: test} })
	if !cleaned {
		t.Error("Cleanup did not run")
	}
}

// A module that declares it does not mutate and then does is a signature
// lying to the compiler, which reads the same flag.
func TestHarnessCatchesASignatureThatUnderstatesTheModule(t *testing.T) {
	store := "initial"
	m := conformingModule(&store)
	m.Sig.Mutates = false
	failures := runHarness(t, m, func() error { store = "initial"; return nil })
	if !hasPhase(failures, "signature") {
		t.Errorf("expected a signature failure, got %v", failures)
	}
}

func TestFailureRendersItsPhase(t *testing.T) {
	f := Failure{Phase: "apply", Msg: "reported no changes"}
	if got := f.String(); got != "apply: reported no changes" {
		t.Errorf("String = %q", got)
	}
}

func hasPhase(failures []Failure, phase string) bool {
	for _, f := range failures {
		if f.Phase == phase {
			return true
		}
	}
	return false
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
