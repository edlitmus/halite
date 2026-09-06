package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// snapFixture gives a context whose `snap list` says what a test says,
// and which believes snapd is installed.
func snapFixture(t *testing.T, list string) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	runner := &exec.RecordingRunner{Responses: map[string]exec.Result{
		"snap list --color=never --unicode=never": {Stdout: list},
	}}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "snap" {
			return "/usr/bin/snap"
		}
		return ""
	}
	return c, runner
}

// A stock 24.04's `snap list`, which is the fixture the rest of these
// use.
const snapListOutput = `Name               Version          Rev    Tracking       Publisher   Notes
core22             20240408         1380   latest/stable  canonical*  base
lxd                5.0.3-9a1d532    28373  5.0/stable     canonical*  -
snapd              2.63             21759  latest/stable  canonical*  snapd
chromium           124.0.6367.60    2842   latest/stable  canonical*  -
some-editor        1.2.3            42     latest/edge    someone     classic
held-back          0.9              7      latest/stable  someone     disabled,devmode
`

// The table is read by its header, not by column position.
func TestTheSnapListIsReadByItsHeader(t *testing.T) {
	got := parseSnapList(snapListOutput)
	if len(got) != 6 {
		t.Fatalf("read %d snaps, want 6: %v", len(got), got)
	}
	lxd := got["lxd"]
	if lxd.Version != "5.0.3-9a1d532" || lxd.Revision != "28373" || lxd.Channel != "5.0/stable" {
		t.Errorf("lxd read as %+v", lxd)
	}
	if got["core22"].Notes != "base" {
		t.Errorf("core22's notes read as %q", got["core22"].Notes)
	}

	// snapd has renamed and reordered these columns over the years:
	// `Tracking` used to be `Channel` and `Publisher` used to be
	// `Developer`. A parser taking the fourth field as the channel reads
	// a publisher as one on an older node and reports every snap as
	// tracking `canonical*`.
	older := strings.Join([]string{
		"Name    Version   Rev    Developer   Channel        Notes",
		"core    16-2.35   4917   canonical   latest/stable  core",
	}, "\n")
	old := parseSnapList(older)
	if len(old) != 1 {
		t.Fatalf("the older table read %d rows, want 1: %v", len(old), old)
	}
	if old["core"].Channel != "latest/stable" {
		t.Errorf("the older table's channel read as %q, want latest/stable", old["core"].Channel)
	}
	if old["core"].Publisher != "canonical" {
		t.Errorf("the older table's publisher read as %q", old["core"].Publisher)
	}
}

// A row that does not line up with the header is skipped rather than
// guessed at, because guessing which column is missing is how a version
// becomes a channel.
func TestASnapRowThatDoesNotLineUpIsSkipped(t *testing.T) {
	got := parseSnapList(strings.Join([]string{
		"Name    Version   Rev    Tracking       Publisher   Notes",
		"good    1.0       1      latest/stable  someone     -",
		"short   1.0       2",
		"",
		"toolong 1.0       3      latest/stable  someone     -   extra",
	}, "\n"))
	if len(got) != 1 {
		t.Errorf("read %d snaps, want only the well-formed one: %v", len(got), got)
	}
	if _, ok := got["short"]; ok {
		t.Error("a short row was parsed anyway")
	}
	if _, ok := got["toolong"]; ok {
		t.Error("a long row was parsed anyway")
	}
}

// The two notes an operator asks about are broken out, because reading
// them out of a comma-separated field is not a tree's job.
func TestTheNotesAnOperatorAsksAboutAreBrokenOut(t *testing.T) {
	got := parseSnapList(snapListOutput)
	for _, tc := range []struct {
		snap              string
		classic, disabled bool
	}{
		{"some-editor", true, false},
		{"held-back", false, true},
		{"lxd", false, false},
		{"core22", false, false},
	} {
		m := got[tc.snap].asMap()
		if c, _ := m.Get("classic"); c != tc.classic {
			t.Errorf("%s classic = %v, want %v (notes %q)", tc.snap, c, tc.classic, got[tc.snap].Notes)
		}
		if d, _ := m.Get("disabled"); d != tc.disabled {
			t.Errorf("%s disabled = %v, want %v (notes %q)", tc.snap, d, tc.disabled, got[tc.snap].Notes)
		}
	}
	// `devmode` sits beside `disabled` in one of these, so a prefix
	// match would report the wrong flag for the wrong snap.
	if snapHasNote("disabled,devmode", "mode") {
		t.Error("a note was matched as a substring of another")
	}
	if !snapHasNote("disabled,devmode", "devmode") {
		t.Error("the second note in a list was not found")
	}
}

// A node with snapd and no snaps is an empty list, not a failure.
//
// `snap list` exits non-zero and writes to stderr there. Treating that
// as an error makes `snap.list` fail on exactly the node where the
// answer is least surprising.
func TestANodeWithNoSnapsIsAnEmptyListNotAnError(t *testing.T) {
	c, _ := snapFixture(t, "")
	c.Runner.(*exec.RecordingRunner).Responses["snap list --color=never --unicode=never"] = exec.Result{
		Code: 1, Stderr: "error: no matching snaps installed\n",
	}
	if _, err := snapList(c); err == nil {
		t.Error("an unrecognised failure was reported as an empty list")
	}

	c2, _ := snapFixture(t, "")
	c2.Runner.(*exec.RecordingRunner).Responses["snap list --color=never --unicode=never"] = exec.Result{
		Code: 1, Stderr: "No snaps are installed yet. Try 'snap install hello-world'.\n",
	}
	got, err := snapList(c2)
	if err != nil {
		t.Fatalf("a node with no snaps was an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a node with no snaps read %d snaps", len(got))
	}
}

// The state installs what is missing, with the channel it was told.
func TestTheSnapStateInstallsWhatIsMissing(t *testing.T) {
	c, runner := snapFixture(t, snapListOutput)
	res, err := snapInstalledState(c, value.MapOf("name", "hello-world", "channel", "latest/edge"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("a missing snap reported no change: %+v", res)
	}
	want := "snap install --channel=latest/edge hello-world"
	if got := runner.RanCommands(); len(got) != 2 || got[1] != want {
		t.Errorf("ran %v, want the list then [%s]", got, want)
	}

	// One already installed and tracking the right channel changes
	// nothing and runs nothing but the read.
	c, runner = snapFixture(t, snapListOutput)
	res, err = snapInstalledState(c, value.MapOf("name", "lxd", "channel", "5.0/stable"))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("a converged snap reported a change: %+v", res.Changes)
	}
	if !res.Succeeded() {
		t.Errorf("a converged snap did not succeed: %+v", res)
	}
	if len(runner.Ran) != 1 {
		t.Errorf("a converged snap ran %v", runner.RanCommands())
	}

	// With no channel named, an installed snap is left entirely alone —
	// the state holds presence and nothing it was not asked to hold.
	c, runner = snapFixture(t, snapListOutput)
	res, _ = snapInstalledState(c, value.MapOf("name", "lxd"))
	if res.HasChanges() {
		t.Errorf("a snap with no channel named reported a change: %+v", res.Changes)
	}
	if len(runner.Ran) != 1 {
		t.Errorf("ran %v", runner.RanCommands())
	}
}

// A channel change is a refresh, not a reinstall.
func TestASnapChannelChangeIsARefresh(t *testing.T) {
	c, runner := snapFixture(t, snapListOutput)
	res, err := snapInstalledState(c, value.MapOf("name", "lxd", "channel", "latest/stable"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("a channel change reported nothing: %+v", res)
	}
	want := "snap refresh --channel=latest/stable lxd"
	if got := runner.RanCommands(); len(got) != 2 || got[1] != want {
		t.Errorf("ran %v, want the list then [%s]", got, want)
	}
	// And it says both ends, because which channel it came from is the
	// half an operator cannot get from the tree.
	for _, s := range []string{"5.0/stable", "latest/stable"} {
		if !strings.Contains(res.Comment, s) {
			t.Errorf("the comment does not name %s: %s", s, res.Comment)
		}
	}
}

// A version is refused with the reason, and the reason is snapd's rather
// than this build's.
//
// A tree migrating from `pkg.installed` carries `version:` without
// thinking about it. Left undeclared, the signature would answer "is not
// a parameter of this function", which reads as a typo. Silently
// ignoring it would be worse: the tree would say a version is pinned and
// nothing would be.
func TestASnapVersionIsRefusedWithTheReason(t *testing.T) {
	c, runner := snapFixture(t, snapListOutput)
	res, err := snapInstalledState(c, value.MapOf("name", "lxd", "version", "5.0.3"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatalf("a version was accepted: %+v", res)
	}
	for _, want := range []string{"5.0.3", "refreshes", "channel"} {
		if !strings.Contains(res.Comment, want) {
			t.Errorf("the refusal does not mention %q: %s", want, res.Comment)
		}
	}
	if len(runner.Ran) != 0 {
		t.Errorf("a refused state ran %v", runner.RanCommands())
	}

	// And it is a declared parameter, so the refusal is this one rather
	// than the signature's generic answer.
	r := New()
	sig, ok := r.States.Signatures().Lookup("snap.installed")
	if !ok {
		t.Fatal("snap.installed is not registered")
	}
	if _, ok := sig.Param("version"); !ok {
		t.Error("`version` is not declared, so the signature refuses it before this reason is reached")
	}
}

// Classic confinement is declared in the tree or the install fails, and
// the failure says what the flag means.
//
// `snap install` on a classic snap without the flag fails and tells you
// to rerun with --classic, which reads like a formality. It is not one:
// a classic snap runs with the host's filesystem and devices. Noticing
// the message and retrying with the flag would be the obvious
// convenience and would convert a confined install into an unconfined
// one on the store's say-so.
func TestClassicConfinementIsDeclaredAndNotInferred(t *testing.T) {
	c, runner := snapFixture(t, snapListOutput)
	runner.Responses["snap install some-ide"] = exec.Result{
		Code: 1,
		Stderr: "error: This revision of snap \"some-ide\" was published using classic confinement " +
			"and thus may perform arbitrary system changes outside of the security " +
			"sandbox that snaps are usually confined to, which may put your system at " +
			"risk.\n\nIf you understand and want to proceed repeat the command including " +
			"--classic.\n",
	}
	res, err := snapInstalledState(c, value.MapOf("name", "some-ide"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatal("a classic snap installed without the flag being declared")
	}
	if !strings.Contains(res.Comment, "not confinement") {
		t.Errorf("the failure does not say what --classic means: %s", res.Comment)
	}
	// Exactly one install attempt: the list, then the install, and no
	// retry with the flag.
	for _, ran := range runner.RanCommands() {
		if strings.Contains(ran, "--classic") {
			t.Errorf("--classic was applied without the tree declaring it: %v", runner.RanCommands())
		}
	}

	// Declared, it is passed.
	c2, runner2 := snapFixture(t, snapListOutput)
	if _, err := snapInstalledState(c2, value.MapOf("name", "some-ide", "classic", true)); err != nil {
		t.Fatal(err)
	}
	want := "snap install --classic some-ide"
	if got := runner2.RanCommands(); len(got) != 2 || got[1] != want {
		t.Errorf("ran %v, want the list then [%s]", got, want)
	}
}

// Removing says what happened to the data, every time.
//
// snapd keeps a snapshot of a removed snap's data unless told not to,
// and reinstalling restores it. On a managed node that is a surprise:
// removing a snap and installing it again in a later state gets the old
// data back rather than a fresh install.
func TestRemovingASnapSaysWhatBecameOfItsData(t *testing.T) {
	c, runner := snapFixture(t, snapListOutput)
	res, err := snapRemovedState(c, value.MapOf("name", "chromium"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("removing an installed snap reported nothing: %+v", res)
	}
	if !strings.Contains(res.Comment, "snapshot") {
		t.Errorf("the comment does not say the data was kept: %s", res.Comment)
	}
	if got := runner.RanCommands(); len(got) != 2 || got[1] != "snap remove chromium" {
		t.Errorf("ran %v", got)
	}

	// Purged, it says the other thing and passes the flag.
	c, runner = snapFixture(t, snapListOutput)
	res, err = snapRemovedState(c, value.MapOf("name", "chromium", "purge", true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Comment, "discarded") {
		t.Errorf("the comment does not say the data was discarded: %s", res.Comment)
	}
	if got := runner.RanCommands(); len(got) != 2 || got[1] != "snap remove --purge chromium" {
		t.Errorf("ran %v", got)
	}

	// One that is not installed is converged, and nothing runs.
	c, runner = snapFixture(t, snapListOutput)
	res, err = snapRemovedState(c, value.MapOf("name", "not-here"))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("removing an absent snap reported a change: %+v", res.Changes)
	}
	if !res.Succeeded() {
		t.Errorf("removing an absent snap did not succeed: %+v", res)
	}
	if len(runner.Ran) != 1 {
		t.Errorf("removing an absent snap ran %v", runner.RanCommands())
	}
}

// Test mode runs the read and nothing else.
func TestTheSnapStatesInTestModeRunNothing(t *testing.T) {
	for _, tc := range []struct {
		what string
		args *value.Map
		run  func(*exec.Context, *value.Map) bool
	}{
		{"installed", value.MapOf("name", "hello-world", "channel", "latest/stable"), func(c *exec.Context, a *value.Map) bool {
			res, err := snapInstalledState(c, a)
			return err == nil && res.HasChanges()
		}},
		{"removed", value.MapOf("name", "chromium"), func(c *exec.Context, a *value.Map) bool {
			res, err := snapRemovedState(c, a)
			return err == nil && res.HasChanges()
		}},
	} {
		c, runner := snapFixture(t, snapListOutput)
		c.Test = true
		if !tc.run(c, tc.args) {
			t.Errorf("snap.%s in test mode predicted no change where there is one", tc.what)
		}
		for _, ran := range runner.RanCommands() {
			if !strings.HasPrefix(ran, "snap list") {
				t.Errorf("snap.%s in test mode ran %q", tc.what, ran)
			}
		}
	}
}

// A node without snapd is told so by name rather than by a missing
// binary.
func TestANodeWithoutSnapdIsToldSo(t *testing.T) {
	c, _ := snapFixture(t, snapListOutput)
	c.Lookup = func(string) string { return "" }
	res, err := snapInstalledState(c, value.MapOf("name", "lxd"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatal("a node without snapd reported success")
	}
	if !strings.Contains(res.Comment, "snapd") {
		t.Errorf("the failure does not name snapd: %s", res.Comment)
	}
}

// Off Linux the registry refuses by name, both sides.
func TestSnapRefusesOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this is the refusal, and it does not happen on linux")
	}
	r := New()
	if _, err := r.Exec.Call(newCtx(false), "snap.list", value.NewMap(0)); err == nil {
		t.Error("snap.list was accepted off linux")
	} else if !strings.Contains(err.Error(), runtime.GOOS) || !strings.Contains(err.Error(), "linux") {
		t.Errorf("the refusal does not name both sides: %v", err)
	}
	if _, err := r.States.Call(newCtx(true), "snap.installed", value.MapOf("name", "lxd")); err == nil {
		t.Error("snap.installed was accepted off linux")
	}
}
