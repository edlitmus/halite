package builtin

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// `pkg`'s Homebrew provider, driven as root against the real brew on
// this Mac -- which is how a node runs it, and the one way it had never
// been run.
//
// # Why this exists
//
// Homebrew refuses root: every command, `brew list` included, ends at
// "Running Homebrew as root is extremely dangerous and no longer
// supported." The live conformance suite left macOS out of `pkg` for
// exactly that reason, and the provider ran brew as whoever ran the
// node, so a real node's every `pkg` call on a Mac failed while `pkg`'s
// evidence -- earned on apt, pkgng and chocolatey -- said Hardware. The
// provider now runs brew as the account that owns it (brewRun).
//
// # What it touches
//
// GNU `hello`, a formula with no dependencies: installed, pinned,
// unpinned and removed. A Mac that already has it is skipped rather
// than used, because removing a package the machine had is not putting
// it back.
//
// # What it establishes
//
//   - every call returns rather than meeting brew's root refusal;
//   - the install landed **owned by brew's owner, not root** -- the
//     failure mode the switch exists to avoid, and the one a passing
//     call would not show: a brew that somehow ran as root would leave
//     a Cellar entry its owner cannot upgrade or remove later;
//   - the pin is visible to brew afterwards, and the removal really
//     removed.
//
// Run it:
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveMacBrewPkgAsRoot -v ./internal/builtin/
func TestLiveMacBrewPkgAsRoot(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to install and remove a Homebrew formula on this Mac")
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_brew_pkg is macOS's, and this is %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Skip("the defect this covers is root's; run it under sudo")
	}
	c := realCtx(t)
	brew := c.Which("brew")
	if brew == "" {
		t.Skip("no brew on the path; this Mac has no Homebrew")
	}
	owner, err := brewBinaryOwner(brew)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("brew at %s, owned by %s (uid %s)", brew, owner.Username, owner.Uid)

	res, err := brewRun(c, exec.Command{Argv: []string{"brew", "--cellar"}})
	if err != nil {
		t.Fatalf("brew --cellar as root: %v", err)
	}
	keg := strings.TrimSpace(res.Stdout) + "/hello"
	if _, err := os.Stat(keg); err == nil {
		t.Skipf("%s already exists; this test installs and removes hello and will not remove one it did not install", keg)
	}

	p := brewProvider{}
	if v, err := p.LatestVersion(c, "hello"); err != nil || v == "" {
		t.Fatalf("LatestVersion(hello) = %q, %v", v, err)
	}

	if err := p.Install(c, []string{"hello"}, nil, false); err != nil {
		t.Fatalf("Install(hello) as root: %v", err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(keg); err == nil {
			_ = p.Unhold(c, "hello")
			if err := p.Remove(c, []string{"hello"}, true); err != nil {
				t.Errorf("cleanup: removing hello: %v", err)
			}
		}
	})

	if _, err := os.Stat(keg); err != nil {
		t.Fatalf("Install reported success and %s is not there: %v", keg, err)
	}
	if got, err := brewBinaryOwner(keg); err != nil {
		t.Errorf("owner of %s: %v", keg, err)
	} else if got.Uid != owner.Uid {
		t.Errorf("%s is owned by %s (uid %s), want brew's owner %s (uid %s)", keg, got.Username, got.Uid, owner.Username, owner.Uid)
	}

	// ListPkgs is an Errorf rather than a Fatal: it is what pkg.installed
	// reads first, so it belongs here, but a Mac with a half-removed cask
	// fails it for a reason that is not this one, and the removal below
	// should still be checked.
	if pkgs, err := p.ListPkgs(c); err != nil {
		t.Errorf("ListPkgs as root: %v", err)
	} else if v, ok := pkgs.Get("hello"); !ok || v == "" {
		t.Errorf("ListPkgs does not list the hello just installed")
	}

	if err := p.Hold(c, "hello"); err != nil {
		t.Fatalf("Hold(hello): %v", err)
	}
	holds, err := p.ListHolds(c)
	if err != nil || !slices.Contains(holds, "hello") {
		t.Errorf("ListHolds = %v, %v; want hello pinned", holds, err)
	}
	if err := p.Unhold(c, "hello"); err != nil {
		t.Fatalf("Unhold(hello): %v", err)
	}

	if err := p.Remove(c, []string{"hello"}, false); err != nil {
		t.Fatalf("Remove(hello) as root: %v", err)
	}
	if _, err := os.Stat(keg); err == nil {
		t.Errorf("Remove reported success and %s is still there", keg)
	}
}
