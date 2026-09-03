package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The text parsers are written against the tools' real output, so the
// fixtures here are real output, captured. A parser written against a
// guess is the defect these tests exist to prevent.

func TestParsePipList(t *testing.T) {
	got, err := parsePipList(`[{"name": "requests", "version": "2.31.0"},
	                           {"name": "urllib3", "version": "2.2.1"}]`)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("requests"); v != "2.31.0" {
		t.Errorf("requests = %#v", v)
	}
	// The mapping is sorted, so a state comparing two of them reads the
	// same order every run.
	if keys := got.StringKeys(); len(keys) != 2 || keys[0] != "requests" {
		t.Errorf("keys = %v", keys)
	}

	if _, err := parsePipList("not json"); err == nil {
		t.Error("non-JSON output should be an error, not an empty package set")
	}
	if _, err := parsePipList(`{"a": 1}`); err == nil {
		t.Error("a JSON object where a list belongs should be an error")
	}
}

func TestParseNpmList(t *testing.T) {
	got, err := parseNpmList(`{
	  "name": "root",
	  "dependencies": {
	    "left-pad": {"version": "1.3.0"},
	    "semver": {"version": "7.6.0"}
	  }
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("left-pad"); v != "1.3.0" {
		t.Errorf("left-pad = %#v", v)
	}

	// An empty tree is the real output of `npm ls --json` in a directory
	// with nothing installed, and it is not an error.
	for _, src := range []string{`{}`, ``, `{"name": "x"}`} {
		got, err := parseNpmList(src)
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		if got.Len() != 0 {
			t.Errorf("%q -> %v, want no packages", src, got.StringKeys())
		}
	}
}

func TestParseGemList(t *testing.T) {
	// Real `gem list --local` output, including the header line it prints
	// and the several-versions form.
	got := parseGemList(`
*** LOCAL GEMS ***

bundler (2.5.6, 2.4.22)
json (2.7.1)
rake (13.1.0)
`)
	if v, _ := got.Get("bundler"); v != "2.5.6" {
		t.Errorf("bundler = %#v; the newest version is the one a state compares", v)
	}
	if v, _ := got.Get("json"); v != "2.7.1" {
		t.Errorf("json = %#v", v)
	}
	if got.Len() != 3 {
		t.Errorf("gems = %v", got.StringKeys())
	}
}

func TestParseCargoList(t *testing.T) {
	// Real `cargo install --list` output: a crate line, then its binaries
	// indented under it.
	got := parseCargoList(`cargo-wasix v0.1.23:
    cargo-wasix
ripgrep v14.1.0:
    rg
`)
	if v, _ := got.Get("ripgrep"); v != "14.1.0" {
		t.Errorf("ripgrep = %#v", v)
	}
	if got.Len() != 2 {
		t.Errorf("crates = %v; the indented binary names are not crates", got.StringKeys())
	}
}

func TestParseGoEnv(t *testing.T) {
	got := parseGoEnv("GOARCH='amd64'\nGOOS='freebsd'\nGOPATH='/home/ed/go'\n")
	if v, _ := got.Get("GOOS"); v != "freebsd" {
		t.Errorf("GOOS = %#v; the quotes belong to the format, not the value", v)
	}
	if got.Len() != 3 {
		t.Errorf("env = %v", got.StringKeys())
	}
}

func TestParseComposerShow(t *testing.T) {
	got, err := parseComposerShow(`{"installed":[{"name":"monolog/monolog","version":"3.5.0"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("monolog/monolog"); v != "3.5.0" {
		t.Errorf("monolog = %#v", v)
	}
	// A project with nothing installed prints an object with no
	// `installed` key, which is not an error.
	if got, err := parseComposerShow(`{}`); err != nil || got.Len() != 0 {
		t.Errorf("empty project -> %v %v", got, err)
	}
}

// ---- the spec parser, which decides what a state compares ----

func TestParseSpec(t *testing.T) {
	cases := []struct {
		raw, sep, name, version string
	}{
		{"requests", "==", "requests", ""},
		{"requests==2.31.0", "==", "requests", "2.31.0"},
		{"left-pad", "@", "left-pad", ""},
		{"left-pad@1.3.0", "@", "left-pad", "1.3.0"},
		{"@scope/pkg", "@", "", "scope/pkg"},
		{"rake:13.1.0", ":", "rake", "13.1.0"},
	}
	for _, c := range cases {
		got := parseSpec(c.raw, c.sep)
		if got.Name != c.name || got.Version != c.version {
			t.Errorf("parseSpec(%q, %q) = %q/%q, want %q/%q",
				c.raw, c.sep, got.Name, got.Version, c.name, c.version)
		}
		if got.Raw != c.raw {
			t.Errorf("parseSpec kept %q as %q; the tool needs the original", c.raw, got.Raw)
		}
	}
}

// ---- the states ----

// langCtx is a context whose npm and pip answer from a scripted listing,
// so the state logic can be exercised without either tool.
func langCtx(t *testing.T, test bool, listing string) *exec.Context {
	t.Helper()
	c := newCtx(test)
	c.Runner = &exec.RecordingRunner{
		Responses: map[string]exec.Result{
			"npm ls --json --depth=0 --global": {Stdout: listing},
		},
	}
	// The tools are declared present rather than looked up, so this
	// really does exercise the state logic without either of them.
	// Left to PATH, the test failed on a machine with no npm and passed
	// on one that had it, neither for a reason it stated.
	c.Lookup = func(name string) string { return "/usr/bin/" + name }
	return c
}

func TestNpmInstalledPredictsBeforeItActs(t *testing.T) {
	r := New()
	const listing = `{"dependencies": {"left-pad": {"version": "1.3.0"}}}`

	// Already there: success, no changes, nothing run beyond the listing.
	c := langCtx(t, false, listing)
	res, err := r.States.Call(c, "npm.installed", value.MapOf("name", "left-pad"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("an installed package should be a no-op: %+v", res)
	}
	if ran := c.Runner.(*exec.RecordingRunner).RanCommands(); len(ran) != 1 {
		t.Errorf("a converged state ran %v; it should only have read the listing", ran)
	}

	// Missing, in test mode: a prediction and nothing else run.
	c = langCtx(t, true, listing)
	res, err = r.States.Call(c, "npm.installed", value.MapOf("name", "semver"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Errorf("test mode should predict, got %s", res.ResultString())
	}
	if !res.HasChanges() {
		t.Error("the prediction should name the package")
	}
	if ran := c.Runner.(*exec.RecordingRunner).RanCommands(); len(ran) != 1 {
		t.Errorf("test mode ran %v; it must not install anything", ran)
	}

	// Missing, for real: the install runs.
	c = langCtx(t, false, listing)
	if _, err := r.States.Call(c, "npm.installed", value.MapOf("name", "semver")); err != nil {
		t.Fatal(err)
	}
	ran := strings.Join(c.Runner.(*exec.RecordingRunner).RanCommands(), " | ")
	if !strings.Contains(ran, "npm install --global semver") {
		t.Errorf("commands = %s", ran)
	}
}

func TestNpmInstalledComparesTheVersion(t *testing.T) {
	r := New()
	const listing = `{"dependencies": {"left-pad": {"version": "1.3.0"}}}`

	// The right version is a no-op; the wrong one is a change.
	c := langCtx(t, true, listing)
	res, _ := r.States.Call(c, "npm.installed", value.MapOf("name", "left-pad@1.3.0"))
	if res.HasChanges() {
		t.Errorf("the installed version matches: %+v", res)
	}

	c = langCtx(t, true, listing)
	res, _ = r.States.Call(c, "npm.installed", value.MapOf("name", "left-pad@2.0.0"))
	if !res.HasChanges() {
		t.Error("a different version should be a change")
	}
	ch, _ := res.Changes.Get("left-pad")
	old, _ := ch.(*value.Map).Get("old")
	if old != "1.3.0" {
		t.Errorf("the change should name the version found: %#v", old)
	}
}

func TestNpmRemovedIsIdempotent(t *testing.T) {
	r := New()
	const listing = `{"dependencies": {"left-pad": {"version": "1.3.0"}}}`

	c := langCtx(t, false, listing)
	res, _ := r.States.Call(c, "npm.removed", value.MapOf("name", "semver"))
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("removing what is not there is a no-op: %+v", res)
	}

	c = langCtx(t, false, listing)
	res, _ = r.States.Call(c, "npm.removed", value.MapOf("name", "left-pad"))
	if !res.HasChanges() {
		t.Error("removing an installed package is a change")
	}
	ran := strings.Join(c.Runner.(*exec.RecordingRunner).RanCommands(), " | ")
	if !strings.Contains(ran, "npm uninstall --global left-pad") {
		t.Errorf("commands = %s", ran)
	}
}

func TestLangStateNeedsAPackage(t *testing.T) {
	r := New()
	c := langCtx(t, false, `{}`)
	res, err := r.States.Call(c, "npm.installed", value.MapOf("name", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed() {
		t.Errorf("a state with no package should fail, not quietly succeed: %+v", res)
	}
}

// A missing tool is an error naming the tool, not a non-zero exit the
// caller has to interpret.
func TestLangToolMissingSaysSo(t *testing.T) {
	c := newCtx(false)
	_, err := langRun(c, "definitely-not-a-real-tool", "", "--version")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-tool") {
		t.Errorf("the error should name the tool: %v", err)
	}
}

var _ = states.True

// npm omits the version for a package it cannot resolve. A state pinning
// a version against one of those would reinstall on every run and never
// converge, so it refuses and says why.
func TestLangStateRefusesAnUnverifiableVersion(t *testing.T) {
	r := New()
	const listing = `{"dependencies": {"weird": {"overridden": false}}}`

	// Pinned: refused, with the package named.
	c := langCtx(t, false, listing)
	res, err := r.States.Call(c, "npm.installed", value.MapOf("name", "weird@1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed() {
		t.Fatalf("a version that cannot be checked should fail: %+v", res)
	}
	for _, want := range []string{"weird", "did not report a version"} {
		if !strings.Contains(res.Comment, want) {
			t.Errorf("comment %q does not mention %q", res.Comment, want)
		}
	}

	// Unpinned: the package is there, so there is nothing to do.
	c = langCtx(t, false, listing)
	res, _ = r.States.Call(c, "npm.installed", value.MapOf("name", "weird"))
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("an unpinned request is satisfied by its presence: %+v", res)
	}
}
