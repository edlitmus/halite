package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// `pkg.latest` says which of the two things it did.
//
// # Why this needed a fake provider rather than a test of the helper
//
// `pkgLatestSentence` is a pure function and testing it directly would prove
// nothing about `pkg.latest`: deleting the call to it would leave such a test
// passing. That error has been made twice in this repository in one week --
// `misplaced()` in tools/ledger, and `ufwDryRunDeleteChanged` in the firewall
// provider -- so the assertion goes through `pkgLatest` with a provider whose
// answers are scripted.
//
// `pkgProviders` is a package-level variable and the fake is swapped in for the
// duration of one test. That is safe here and would not be everywhere: nothing
// in this package calls `t.Parallel()`, so the tests are sequential and a
// global cannot be observed half-swapped. A package that grew a parallel test
// would need a seam on the context instead.
type fakeLatestProvider struct {
	installed map[string]string
	latest    map[string]string
	// calls records each Install, as the names it was given. Calls and not
	// a flat list of names: the first version of this appended `names...`
	// to one slice, so two calls and one call were indistinguishable by
	// length and the assertion below passed against a deliberately broken
	// build. A test whose message has to hedge -- it said "call(s) worth of
	// names" -- is a test that is not sure what it is measuring.
	calls [][]string
}

func (f *fakeLatestProvider) Name() string                    { return "fakelatest" }
func (f *fakeLatestProvider) Available(c *exec.Context) bool  { return true }
func (f *fakeLatestProvider) RefreshDB(c *exec.Context) error { return nil }
func (f *fakeLatestProvider) ListPkgs(c *exec.Context) (*value.Map, error) {
	out := value.NewMap(len(f.installed))
	for name, version := range f.installed {
		out.Set(name, version)
	}
	return out, nil
}
func (f *fakeLatestProvider) LatestVersion(c *exec.Context, name string) (string, error) {
	return f.latest[name], nil
}
func (f *fakeLatestProvider) Install(c *exec.Context, names []string, versions map[string]string, refresh bool) error {
	f.calls = append(f.calls, append([]string{}, names...))
	for _, n := range names {
		f.installed[n] = f.latest[n]
	}
	return nil
}
func (f *fakeLatestProvider) Remove(c *exec.Context, names []string, purge bool) error { return nil }

// useFakeLatest swaps the provider list and puts it back.
func useFakeLatest(t *testing.T, f *fakeLatestProvider) {
	t.Helper()
	before := pkgProviders
	pkgProviders = []pkgProvider{f}
	t.Cleanup(func() { pkgProviders = before })
}

// **A package that is not installed is installed, and the comment says so.**
//
// This is the defect: `pkg.latest` compares the newest available version
// against the installed one, and for an absent package the installed one is
// the empty string -- so the state installs it, correctly, and then reported
// that it had been *upgraded*. The change set was right all along
// (`{old: "", new: …}`), which is why nothing that reads changes could see it.
func TestPkgLatestSaysInstalledForAPackageThatWasNotThere(t *testing.T) {
	f := &fakeLatestProvider{
		installed: map[string]string{},
		latest:    map[string]string{"tree": "2.1.1-2"},
	}
	useFakeLatest(t, f)
	r := New()
	args := value.MapOf("name", "tree")

	// Test mode first, because the prediction is read by an operator
	// deciding whether to apply.
	predicted := run(t, r, "pkg.latest", args, true)
	if !strings.Contains(predicted.Comment, "would be installed") {
		t.Errorf("test mode says %q; the package is not installed, so applying "+
			"this would install it", predicted.Comment)
	}
	if strings.Contains(predicted.Comment, "upgraded") {
		t.Errorf("test mode says %q of a package that is not installed", predicted.Comment)
	}

	applied := run(t, r, "pkg.latest", args, false)
	if !applied.Succeeded() || !applied.HasChanges() {
		t.Fatalf("result = %+v", applied)
	}
	if !strings.Contains(applied.Comment, "were installed") {
		t.Errorf("the comment says %q; the package was installed", applied.Comment)
	}
	if strings.Contains(applied.Comment, "upgraded") {
		t.Errorf("the comment says %q of a package it installed", applied.Comment)
	}
	// The change set was always right, and stays right: the absence is what
	// `old` names, and it is the reason no test disagreed with the comment.
	raw, ok := applied.Changes.Get("tree")
	if !ok {
		t.Fatalf("no change is reported for tree: %+v", applied.Changes)
	}
	change, _ := raw.(*value.Map)
	if change == nil {
		t.Fatalf("the change for tree is %T", raw)
	}
	if old, _ := change.Get("old"); old != "" {
		t.Errorf("old = %v, want the empty version an absent package has", old)
	}
	if now, _ := change.Get("new"); now != "2.1.1-2" {
		t.Errorf("new = %v, want 2.1.1-2", now)
	}
}

// A package that is installed and outdated is upgraded, and says that.
func TestPkgLatestSaysUpgradedForAPackageThatWasOutdated(t *testing.T) {
	f := &fakeLatestProvider{
		installed: map[string]string{"tree": "2.1.0-1"},
		latest:    map[string]string{"tree": "2.1.1-2"},
	}
	useFakeLatest(t, f)
	r := New()

	applied := run(t, r, "pkg.latest", value.MapOf("name", "tree"), false)
	if !strings.Contains(applied.Comment, "were upgraded") {
		t.Errorf("the comment says %q; the package was upgraded", applied.Comment)
	}
	if strings.Contains(applied.Comment, "installed") {
		t.Errorf("the comment says %q of a package that was already there", applied.Comment)
	}
}

// Both at once, which is the case a tree naming several packages meets and the
// reason the comment is built from two lists rather than chosen between them.
func TestPkgLatestNamesBothWhenItDoesBoth(t *testing.T) {
	f := &fakeLatestProvider{
		installed: map[string]string{"curl": "8.5.0-1"},
		latest:    map[string]string{"curl": "8.5.0-2", "tree": "2.1.1-2"},
	}
	useFakeLatest(t, f)
	r := New()

	applied := run(t, r, "pkg.latest",
		value.MapOf("pkgs", []any{"curl", "tree"}), false)
	if !applied.Succeeded() || !applied.HasChanges() {
		t.Fatalf("result = %+v", applied)
	}
	for _, want := range []string{"were installed: tree", "were upgraded: curl"} {
		if !strings.Contains(applied.Comment, want) {
			t.Errorf("the comment does not say %q: %s", want, applied.Comment)
		}
	}

	// **Splitting the message must not split the work.** Both packages go to
	// the provider in one call, because `Install` upgrades what is present
	// and installs what is not -- and two calls would be two transactions
	// where the estate asked for one.
	if len(f.calls) != 1 {
		t.Fatalf("the provider was called %d times with %v; both packages belong "+
			"in one Install", len(f.calls), f.calls)
	}
	if len(f.calls[0]) != 2 {
		t.Errorf("the one Install was given %v, want both packages", f.calls[0])
	}
}

// The converged answer is unchanged, and is right for both kinds: a package
// already at its newest is at its newest whether it was installed today or a
// year ago.
func TestPkgLatestConvergesOnAPackageAtItsNewest(t *testing.T) {
	f := &fakeLatestProvider{
		installed: map[string]string{"tree": "2.1.1-2"},
		latest:    map[string]string{"tree": "2.1.1-2"},
	}
	useFakeLatest(t, f)
	r := New()

	res := run(t, r, "pkg.latest", value.MapOf("name", "tree"), false)
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("result = %+v", res)
	}
	if err := states.CommentIsASentence(res.Comment); err != nil {
		t.Errorf("%v", err)
	}
}
