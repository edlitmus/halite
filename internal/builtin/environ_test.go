package builtin

import (
	"os"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The state converges: it writes what was declared, and the run after
// that reports no change. Against the platform's real store — the
// registry on Windows, a redirected /etc/environment elsewhere — because
// the whole question is whether the store reads back what was written.
func TestEnvironSetenvConvergesInOneChange(t *testing.T) {
	r := New()
	c := environFixture(t)

	args := value.MapOf("name", "HALITE_TEST_ONE", "value", "first")
	res, err := r.States.Call(c, "environ.setenv", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("setting the variable failed: %s", res.Comment)
	}
	if !res.HasChanges() {
		t.Error("setting a variable that was not there reported no change")
	}

	res, err = r.States.Call(c, "environ.setenv", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run reported a change: %v", res.Changes)
	}

	// And the value is really in the store, not only in this process.
	store, err := persistentEnviron("")
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.Items()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := items.Get("HALITE_TEST_ONE"); got != "first" {
		t.Errorf("%s holds %v for HALITE_TEST_ONE, want \"first\"", store.Name(), got)
	}
}

// A removal is reported as a removal. "was set" over a variable that was
// deleted is a report an operator reads once, believes, and is wrong
// about.
func TestEnvironSetenvRemovesAndSaysSo(t *testing.T) {
	r := New()
	c := environFixture(t)

	if _, err := r.States.Call(c, "environ.setenv",
		value.MapOf("name", "HALITE_TEST_GONE", "value", "here")); err != nil {
		t.Fatal(err)
	}

	remove := value.MapOf("name", "HALITE_TEST_GONE", "value", false, "false_unsets", true)
	res, err := r.States.Call(c, "environ.setenv", remove)
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("removing a variable that was there reported no change: %s", res.Comment)
	}
	if !strings.Contains(res.Comment, "was removed from") {
		t.Errorf("a removal is reported as %q", res.Comment)
	}

	// And removing what is already gone is not a change.
	res, err = r.States.Call(c, "environ.setenv", remove)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("removing an absent variable reported a change: %v", res.Changes)
	}
}

// Several variables in one declaration, and the sentence agrees with
// itself in number.
func TestEnvironSetenvTakesAMappingAndCountsCorrectly(t *testing.T) {
	r := New()
	c := environFixture(t)

	res, err := r.States.Call(c, "environ.setenv", value.MapOf(
		"name", "ignored",
		"value", value.MapOf("HALITE_TEST_A", "a", "HALITE_TEST_B", "b"),
	))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("setting two variables failed: %s", res.Comment)
	}
	if res.Changes.Len() != 2 {
		t.Errorf("two variables produced %d change(s)", res.Changes.Len())
	}
	if !strings.Contains(res.Comment, "were set in") {
		t.Errorf("two variables are reported as %q", res.Comment)
	}
	if _, ok := res.Changes.Get("ignored"); ok {
		t.Error("the state ID was taken as a variable name beside the mapping")
	}
}

// Test mode changes nothing, and says what it would have done.
func TestEnvironSetenvInTestModeWritesNothing(t *testing.T) {
	r := New()
	c := environFixture(t)
	c.Test = true

	res, err := r.States.Call(c, "environ.setenv",
		value.MapOf("name", "HALITE_TEST_DRY", "value", "x"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Fatalf("test mode did not report a would-change: %s", res.Comment)
	}
	if !strings.Contains(res.Comment, "would be set in") {
		t.Errorf("test mode reports %q", res.Comment)
	}

	store, _ := persistentEnviron("")
	items, err := store.Items()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := items.Get("HALITE_TEST_DRY"); ok {
		t.Errorf("a test run wrote HALITE_TEST_DRY to %s", store.Name())
	}
	if _, ok := os.LookupEnv("HALITE_TEST_DRY"); ok {
		t.Error("a test run set HALITE_TEST_DRY in this process")
	}
}

// The two Salt options this build does not honour are refused by name,
// rather than accepted and quietly given a different meaning.
func TestEnvironSetenvRefusesTheOptionsItDoesNotHonour(t *testing.T) {
	r := New()
	for _, tc := range []struct{ option, want string }{
		{"clear_all", "clear_all: True is not accepted"},
		{"update_minion", "update_minion: True is not accepted"},
	} {
		c := environFixture(t)
		res, err := r.States.Call(c, "environ.setenv",
			value.MapOf("name", "HALITE_TEST_X", "value", "x", tc.option, true))
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || *res.Result {
			t.Errorf("%s was accepted: %s", tc.option, res.Comment)
		}
		if !strings.Contains(res.Comment, tc.want) {
			t.Errorf("%s is refused with %q", tc.option, res.Comment)
		}
	}

	// The same options set to false are the default behaviour and are
	// not refused: a tree that spells out what it does not want should
	// not be stopped for saying so.
	c := environFixture(t)
	res, err := r.States.Call(c, "environ.setenv", value.MapOf(
		"name", "HALITE_TEST_OK", "value", "x", "clear_all", false, "update_minion", false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Errorf("the options set to false were refused: %s", res.Comment)
	}
}

// A value declared as false without false_unsets is the string, which is
// the distinction a YAML tree cannot otherwise make.
func TestEnvironValueSpellsRemovalOnlyWithFalseUnsets(t *testing.T) {
	for _, tc := range []struct {
		name        string
		raw         any
		falseUnsets bool
		want        string
		remove      bool
	}{
		{"false without the option is the string", false, false, "false", false},
		{"false with the option is a removal", false, true, "", true},
		{"the word false with the option is a removal too", "false", true, "", true},
		{"the word false without it is the string", "false", false, "false", false},
		{"nothing at all is a removal either way", nil, false, "", true},
		{"a number is its own text", int64(8080), false, "8080", false},
	} {
		got, remove := environValue(tc.raw, tc.falseUnsets)
		if got != tc.want || remove != tc.remove {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, got, remove, tc.want, tc.remove)
		}
	}
}

// environFixture returns a node whose persistent environment can be
// written without changing the machine the test runs on.
//
// On Windows that is the real HKCU\Environment, under names no other
// program uses and removed again afterwards: the registry is the thing
// under test, and a fake one would prove nothing about the value types
// the session builder actually reads.
func environFixture(t *testing.T) *exec.Context {
	t.Helper()
	names := []string{
		"HALITE_TEST_ONE", "HALITE_TEST_GONE", "HALITE_TEST_A", "HALITE_TEST_B",
		"HALITE_TEST_DRY", "HALITE_TEST_X", "HALITE_TEST_OK",
	}
	clean := func() {
		store, err := persistentEnviron("")
		if err != nil {
			return
		}
		for _, n := range names {
			_ = store.Unset(n)
			_ = os.Unsetenv(n)
		}
	}
	// Redirected first, so that the cleanup below cannot reach the real
	// /etc/environment on the machine running the test.
	redirectPersistentEnviron(t)
	clean()
	t.Cleanup(clean)
	return newCtx(false)
}
