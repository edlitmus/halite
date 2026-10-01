package builtin

import (
	"errors"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// brewCtx is a context on a machine whose brew is at the Apple-silicon
// path, answering every command with an empty JSON object -- which every
// brew reader here takes as "nothing", so each method runs to the end
// and records every command it would have run.
func brewCtx(t *testing.T, euid int, owner *user.User) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	oldEuid, oldOwner := brewEuid, brewOwner
	t.Cleanup(func() { brewEuid, brewOwner = oldEuid, oldOwner })
	brewEuid = func() int { return euid }
	brewOwner = func(path string) (*user.User, error) {
		if path != "/opt/homebrew/bin/brew" {
			t.Errorf("brew's owner was asked of %q, not the brew on the path", path)
		}
		if owner == nil {
			return nil, errors.New("no such file")
		}
		return owner, nil
	}
	c := newCtx(false)
	c.Grains = value.MapOf("os", "MacOS", "os_family", "MacOS")
	c.Lookup = func(name string) string {
		if name == "brew" {
			return "/opt/homebrew/bin/brew"
		}
		return ""
	}
	r := &exec.RecordingRunner{Default: exec.Result{Stdout: "{}"}}
	c.Runner = r
	return c, r
}

// everyBrewMethod calls each method of the provider that runs brew. It
// is the list to extend when the provider grows one: a method missing
// from here is a method the root test below cannot see bypass brewRun.
func everyBrewMethod(t *testing.T, c *exec.Context) {
	t.Helper()
	p := brewProvider{}
	calls := map[string]func() error{
		"ListPkgs":      func() error { _, err := p.ListPkgs(c); return err },
		"Install":       func() error { return p.Install(c, []string{"hello"}, nil, true) },
		"Remove":        func() error { return p.Remove(c, []string{"hello"}, true) },
		"LatestVersion": func() error { _, err := p.LatestVersion(c, "hello"); return err },
		"RefreshDB":     func() error { return p.RefreshDB(c) },
		"Hold":          func() error { return p.Hold(c, "hello") },
		"Unhold":        func() error { return p.Unhold(c, "hello") },
		"ListHolds":     func() error { _, err := p.ListHolds(c); return err },
		"Upgrade":       func() error { _, err := p.Upgrade(c, true); return err },
		"ListUpgrades":  func() error { _, err := p.ListUpgrades(c, true); return err },
	}
	for name, call := range calls {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func envHas(env []string, key string) (string, bool) {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v, true
		}
	}
	return "", false
}

// The defect: as root, every brew command was run as root, and Homebrew
// refuses root outright. Each one must run as the account that owns
// brew, from that account's home, and without root's HOME alongside the
// one the credential switch supplies.
func TestBrewAsRootRunsEveryCommandAsTheAccountThatOwnsBrew(t *testing.T) {
	alice := &user.User{Uid: "501", Gid: "20", Username: "alice", HomeDir: "/Users/alice"}
	c, r := brewCtx(t, 0, alice)
	t.Setenv("HOME", "/var/root")

	everyBrewMethod(t, c)

	if len(r.Ran) == 0 {
		t.Fatal("no brew command was run, so nothing here was checked")
	}
	for _, cmd := range r.Ran {
		if cmd.RunAs != "alice" {
			t.Errorf("%s: ran as %q, want alice", cmd, cmd.RunAs)
		}
		if cmd.Dir != "/Users/alice" {
			t.Errorf("%s: ran in %q, want alice's home", cmd, cmd.Dir)
		}
		if home, ok := envHas(cmd.Env, "HOME"); ok {
			t.Errorf("%s: carries HOME=%s, which the switch to alice should supply instead", cmd, home)
		}
		if _, ok := envHas(cmd.Env, "HOMEBREW_NO_AUTO_UPDATE"); !ok {
			t.Errorf("%s: lost brew's environment", cmd)
		}
	}
}

// A node that is not root cannot become anybody, and runs brew as
// itself -- with its own HOME, which brew refuses to run without.
func TestBrewAsAnOrdinaryAccountRunsAsItself(t *testing.T) {
	c, r := brewCtx(t, 501, nil)
	t.Setenv("HOME", "/Users/alice")

	everyBrewMethod(t, c)

	for _, cmd := range r.Ran {
		if cmd.RunAs != "" || cmd.Dir != "" {
			t.Errorf("%s: RunAs %q, Dir %q; a non-root node should switch to nobody", cmd, cmd.RunAs, cmd.Dir)
		}
		if home, _ := envHas(cmd.Env, "HOME"); home != "/Users/alice" {
			t.Errorf("%s: HOME = %q, want the caller's", cmd, home)
		}
	}
}

// A state's `runas` is the operator's choice and wins over brew's owner,
// the way it does for every other command.
func TestBrewHonoursAnExplicitRunAs(t *testing.T) {
	alice := &user.User{Uid: "501", Gid: "20", Username: "alice", HomeDir: "/Users/alice"}
	c, r := brewCtx(t, 0, alice)
	c.RunAs = "bob"

	if _, err := (brewProvider{}).ListPkgs(c); err != nil {
		t.Fatal(err)
	}
	if got := r.Ran[0].RunAs; got != "bob" {
		t.Errorf("ran as %q, want the context's bob", got)
	}
}

// A brew that root owns cannot be run as its owner, because that is root.
// It is refused by name, before brew is asked to refuse less clearly.
func TestBrewOwnedByRootIsRefusedBeforeRunning(t *testing.T) {
	root := &user.User{Uid: "0", Gid: "0", Username: "root", HomeDir: "/var/root"}
	c, r := brewCtx(t, 0, root)

	_, err := (brewProvider{}).ListPkgs(c)
	if err == nil || !strings.Contains(err.Error(), "owned by root") {
		t.Fatalf("err = %v, want the owned-by-root refusal", err)
	}
	if len(r.Ran) != 0 {
		t.Errorf("ran %v after refusing", r.RanCommands())
	}
}

// Every fixture under testdata/brew/macos26 was captured on 2026-10-01 on
// macOS 26.7.1 (arm64) with Homebrew 7.0.7, as the account that owns it,
// by a script that saved each command's argv, stdout, stderr and exit
// status. That Mac had 389 formulae and 18 Caskroom entries, four of
// which Homebrew calls "not installed" (DIVERGENCE 5.189).
//
// Both are cut down from the capture, mechanically, because the full
// listing names everything installed on somebody's machine, internal
// taps included, and is 1.7 MB. info-installed.stdout is
//
//	jq '{formulae: [.formulae[] | select(.full_name as $n | ["fish-shell/fish-beta-4/fish","glib","readline","curl","icu4c@78","jq","hashicorp/tap/boundary"] | index([$n]))], casks: [.casks[] | select(.token as $t | ["goreleaser","1password-cli","chatgpt"] | index([$t]))]}'
//
// -- whole entries, unedited, chosen for two formulae with two versions
// installed (fish, whose linked one is the older; glib), one with two and
// neither linked (readline), two keg-only (curl, icu4c@78), two from taps,
// and three casks, one from a tap. list-versions.stdout is the same
// formulae's lines from `brew list --versions`; its exit status and
// stderr are as captured, which is the failure.
var brewFixtureDir = filepath.Join("testdata", "brew", "macos26")

func brewFixtureCtx(t *testing.T, fixtures ...string) *exec.Context {
	t.Helper()
	old := brewEuid
	t.Cleanup(func() { brewEuid = old })
	brewEuid = func() int { return 501 }
	c := newCtx(false)
	c.Grains = value.MapOf("os", "MacOS", "os_family", "MacOS")
	c.Lookup = func(name string) string {
		if name == "brew" {
			return "/opt/homebrew/bin/brew"
		}
		return ""
	}
	c.Runner = newCapturedRunner(t, brewFixtureDir, fixtures...)
	return c
}

// The defect: one cask Homebrew cannot load made `brew list --versions`
// exit 1, and the provider failed with it. The listing must come back,
// formulae and casks both.
func TestBrewListPkgsSurvivesACaskBrewCannotLoad(t *testing.T) {
	c := brewFixtureCtx(t, "info-installed", "list-versions")
	got, err := (brewProvider{}).ListPkgs(c)
	if err != nil {
		t.Fatalf("ListPkgs: %v", err)
	}
	want := map[string]string{
		"fish":     "4.0b1", // linked, and older than the 4.6.0 beside it
		"glib":     "2.90.0",
		"readline": "8.3.6", // two installed, neither linked: the newest
		"curl":     "8.22.0",
		"icu4c@78": "78.3",
		"jq":       "1.8.2",
		"boundary": "0.21.3", // hashicorp/tap/boundary, by its short name
		// casks
		"1password-cli": "2.39.0",
		"chatgpt":       "26.623.141536",
		"goreleaser":    "2.18.2", // goreleaser/tap/goreleaser
	}
	for name, v := range want {
		if g, _ := got.Get(name); g != v {
			t.Errorf("%s = %#v, want %q", name, g, v)
		}
	}
	if got.Len() != len(want) {
		t.Errorf("listed %d packages, want %d: %v", got.Len(), len(want), got.Keys())
	}
}

// The listing this replaces is the oracle for formulae: on the full
// capture the two agreed for all 389, and they must agree for the
// formulae kept here. `brew list --versions` printed kegs in directory
// order and its parser took the last field.
func TestBrewListPkgsAgreesWithTheListItReplaces(t *testing.T) {
	c := brewFixtureCtx(t, "info-installed", "list-versions")
	got, err := (brewProvider{}).ListPkgs(c)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(capturedFixture(t, brewFixtureDir, "list-versions", "stdout")), "\n")
	if len(lines) != 7 {
		t.Fatalf("list-versions has %d lines, want the 7 formulae", len(lines))
	}
	for _, line := range lines {
		f := strings.Fields(line)
		if g, _ := got.Get(f[0]); g != f[len(f)-1] {
			t.Errorf("%s: ListPkgs says %#v, `brew list --versions` said %q", f[0], g, f[len(f)-1])
		}
	}
}

func TestBrewListPkgsRefusesWhatIsNotBrewJSON(t *testing.T) {
	if _, err := parseBrewInstalled("Error: something brew said\n"); err == nil {
		t.Error("a non-JSON answer was accepted")
	}
	if _, err := parseBrewInstalled(`[]`); err == nil {
		t.Error("a JSON array was accepted as brew's object")
	}
}

// The info-* fixtures for single names were captured the same day and
// are whole: `brew info --json=v2` for jq (installed) and hello (not),
// a name brew has nothing for, and hello again with HOME unset --
// a real refusal from brew that needs no root to produce, standing in
// for the root refusal that showed this defect (DIVERGENCE 5.190).

func TestBrewLatestVersionReadsTheStableVersion(t *testing.T) {
	c := brewFixtureCtx(t, "info-jq", "info-available")
	for name, want := range map[string]string{"jq": "1.8.2", "hello": "2.12.3"} {
		got, err := (brewProvider{}).LatestVersion(c, name)
		if err != nil || got != want {
			t.Errorf("LatestVersion(%s) = %q, %v; want %q", name, got, err, want)
		}
	}
}

// brew has no such package: exit 1 and `No available formula`. That is
// an answer -- nothing to install -- and not a failure.
func TestBrewLatestVersionIsEmptyForANameBrewDoesNotHave(t *testing.T) {
	c := brewFixtureCtx(t, "info-nosuch")
	got, err := (brewProvider{}).LatestVersion(c, "no-such-formula-halite")
	if err != nil || got != "" {
		t.Errorf("LatestVersion = %q, %v; want an empty version and no error", got, err)
	}
}

// The defect: brew could not look, also exit 1, and that came back as
// "no such package". It must come back as brew's own reason.
func TestBrewLatestVersionFailsWhenBrewCannotAnswer(t *testing.T) {
	c := brewFixtureCtx(t, "info-nohome")
	got, err := (brewProvider{}).LatestVersion(c, "hello")
	if err == nil {
		t.Fatalf("LatestVersion = %q, nil; brew refused to run and said so", got)
	}
	if !strings.Contains(err.Error(), "$HOME must be set to run brew") {
		t.Errorf("err = %v; want brew's own reason in it", err)
	}
}

func TestBrewSaysUnavailableOnlyForItsNotFoundErrors(t *testing.T) {
	for stderr, want := range map[string]bool{
		capturedFixture(t, brewFixtureDir, "info-nosuch", "stderr"): true,
		// The other two spellings in Homebrew's exceptions.rb, the tap
		// variant with its trailing note. Taken from the source, not
		// captured: no tap here was missing to make brew print them.
		"Error: No available formula or cask with the name \"x\".\n":                                         true,
		"Error: No available formula or cask with the name \"t/x/y\".\nThis command requires the tap t/x.\n": true,
		capturedFixture(t, brewFixtureDir, "info-nohome", "stderr"):                                          false,
		"Error: Running Homebrew as root is extremely dangerous and no longer supported.\n":                  false,
		"": false,
	} {
		if got := brewSaysUnavailable(stderr); got != want {
			t.Errorf("brewSaysUnavailable(%q) = %v, want %v", stderr, got, want)
		}
	}
}

// What the defect cost at the state: `pkg.latest` reads an empty latest
// version as "nothing newer", so a brew that could not answer for one
// name reported that name up to date. The pairing here -- the listing
// answers, `brew info hello` does not -- is two real captures put
// together, not one run: every failure produced on a real Mac also broke
// the listing, which the state reads first and fails on correctly. It is
// what an unreachable formula API would look like while the local
// listing still works, and it has not been watched happening.
func TestBrewPkgLatestFailsWhenBrewCannotSayWhatIsLatest(t *testing.T) {
	c := brewFixtureCtx(t, "info-installed", "info-nohome")
	res, err := pkgLatest(c, value.MapOf("name", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result {
		t.Fatalf("pkg.latest succeeded (%q) while brew could not say what is latest", res.Comment)
	}
	if !strings.Contains(res.Comment, "$HOME must be set to run brew") {
		t.Errorf("comment = %q; want brew's reason in it", res.Comment)
	}
}
