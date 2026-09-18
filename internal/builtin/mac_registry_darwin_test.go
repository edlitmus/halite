//go:build darwin

package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The macOS assertions that have to go through the registry, and
// therefore only hold on a Mac.
//
// `exec.Registry.Call` checks a signature's `Platforms` **before** it
// binds arguments, so on any other node these calls come back with
// "runs on darwin, and this node is freebsd" and never reach the
// behaviour under test. That is the registry working: a tree calling
// `mac_softwareupdate.update` on Linux should be told exactly that.
//
// It is also why these three cannot live beside their neighbours.
// Their whole point is the layer above `Fn` -- argument validation,
// and what a refusal reads like once it has passed -- so calling `Fn`
// directly, which is how the platform-independent tests in those files
// reach the same functions, would remove the thing being tested. A
// build tag keeps them honest: on darwin they run, and everywhere else
// they do not exist rather than passing without asserting anything.
//
// Both macOS legs of CI (`test (macos-15)` and `race (macos-15)`) run
// them on every push.

// Every refusal is reachable with the arguments a tree actually passes.
//
// # What this is guarding
//
// A refusal registers so that a tree carrying the name from Salt gets
// an explanation instead of "unknown function", which reads as a typo.
// That only works if the call reaches the refusal — and a signature
// declaring no parameters rejects `name=...` during argument
// validation, one layer above, with "argument \"name\" is not a
// parameter of this function". Which also reads as a typo.
//
// Calling `m.Fn` directly, as the test above does, cannot see this:
// `Fn` is past the validation. This goes through the registry, which is
// the path a tree takes.
func TestMacSoftwareUpdateRefusalsAreReachableAsATreeCallsThem(t *testing.T) {
	r := New()
	c := newCtx(false)

	cases := []struct {
		fn   string
		args *value.Map
		want string
	}{
		{"ignore", value.MapOf("name", "macOS Sequoia 15.6.1-24G90"), "removed from macOS"},
		{"list_ignored", value.NewMap(0), "removed from macOS"},
		{"reset_ignored", value.NewMap(0), "removed from macOS"},
		{"update", value.MapOf("name", "macOS Sequoia 15.6.1-24G90", "restart", true),
			"halite does not install"},
		{"update_all", value.MapOf("recommended", true, "restart", true),
			"halite does not install"},
	}

	for _, tc := range cases {
		name := "mac_softwareupdate." + tc.fn
		_, err := r.Exec.Call(c, name, tc.args)
		if err == nil {
			t.Errorf("%s did not refuse", name)
			continue
		}
		if strings.Contains(err.Error(), "is not a parameter of this function") {
			t.Errorf("%s rejected the arguments before reaching its refusal, so a tree "+
				"carrying it from Salt is told it made a typo: %v", name, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: the refusal does not say %q: %v", name, tc.want, err)
		}
	}
}

// Installing is refused by name, and the refusal says whose decision it
// is.
//
// The distinction matters to whoever reads it: the `ignore` functions
// next door refuse because macOS took the option away, and nothing can
// give a tree what it asked for. `softwareupdate --install` works. This
// module declines to run it, and an operator who cannot tell those
// apart will go looking for a macOS release note that does not exist.
func TestMacSoftwareUpdateInstallRefusesAndSaysWhose(t *testing.T) {
	r := New()
	// `update` takes a label and `update_all` does not, the same split
	// Salt has.
	for fn, args := range map[string]*value.Map{
		"update":     value.MapOf("name", "macOS Sequoia 15.6.1-24G90"),
		"update_all": value.MapOf("recommended", true),
	} {
		name := "mac_softwareupdate." + fn
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered; a tree carrying it from Salt would get "+
				"`unknown function`, which reads as a typo", name)
		}
		if sig.Mutates {
			t.Errorf("%s refuses and still declares Mutates", name)
		}

		_, err := r.Exec.Call(newCtx(false), name, args)
		if err == nil {
			t.Fatalf("%s did not refuse", name)
		}
		for _, want := range []string{
			"halite does not install",
			"halite's decision, not a",
			"download",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the refusal does not say %q: %v", fn, want, err)
			}
		}
	}
}

// Downloading still runs, and no longer takes the install path's
// `restart`.
func TestMacSoftwareUpdateDownloadRunsAndHasNoRestart(t *testing.T) {
	r := New()
	sig, ok := r.Exec.Signatures().Lookup("mac_softwareupdate.download")
	if !ok {
		t.Fatal("mac_softwareupdate.download is not registered")
	}
	if !sig.Mutates {
		t.Error("download fetches a payload and does not declare Mutates")
	}
	for _, p := range sig.Params {
		if p.Name == "restart" {
			t.Error("download still takes `restart`, which only ever meant anything " +
				"to the install path")
		}
	}

	runner := &exec.RecordingRunner{}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "softwareupdate" {
			return "/usr/sbin/softwareupdate"
		}
		return ""
	}
	if _, err := r.Exec.Call(c, "mac_softwareupdate.download",
		value.MapOf("name", "Security Update 2020-004")); err != nil {
		t.Fatalf("download: %v", err)
	}
	ran := runner.RanCommands()
	if len(ran) != 1 || !strings.Contains(ran[0], "--download") {
		t.Fatalf("download ran %v", ran)
	}
	if strings.Contains(ran[0], "--install") {
		t.Errorf("download ran an install: %s", ran[0])
	}
}

// What `security default-keychain` says for an account that has none.
// Captured from the real `security` on macOS 27.0 (build 26A5425a),
// running as root.
const securityNoDefaultKeychain = "security: SecKeychainCopyDefault: A default keychain " +
	"could not be found.\n"

// An account with no default keychain gets an answer, not an error.
//
// root is such an account on a stock Mac, and root is what halite-node
// runs as, so this is the path every real node takes.
func TestMacKeychainDefaultKeychainIsEmptyForAnAccountWithoutOne(t *testing.T) {
	r := New()
	c, _ := macKeychainCtx(t, map[string]exec.Result{
		"security default-keychain": {Code: 1, Stderr: securityNoDefaultKeychain},
	})

	got, err := r.Exec.Call(c, "mac_keychain.default_keychain", value.NewMap(0))
	if err != nil {
		t.Fatalf("default_keychain on an account with none was an error, which is what "+
			"every halite node running as root would get: %v", err)
	}
	if got != "" {
		t.Errorf("default_keychain returned %#v, want the empty string", got)
	}
}
