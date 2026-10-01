package builtin

import (
	"errors"
	"os/user"
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
