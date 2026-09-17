package builtin

import (
	"fmt"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// `mac_defaults`, driven as root against the two paths the unprivileged
// live test cannot reach.
//
// # Why these are separate from live_mac_defaults_test.go
//
// That file covers the invoking user writing their own domain, which is
// the whole of what a Mac without root can show. It is also not the
// reason every mutating function in this module declares
// `Privileges: ["root"]`. Two things do:
//
//   - `user`, which maps to the command's `RunAs` — setuid and setgid
//     to another account with its full group set, in
//     internal/exec/credential_unix.go. Becoming somebody else needs
//     root, and nothing had ever watched it happen. The failure this
//     rules out is the quiet one: the write succeeds, reports a change,
//     converges on a second run, and lands in *root's* preference store
//     rather than the account the tree named. Every assertion passes and
//     the user's Dock never changes.
//   - a system domain under `/Library/Preferences`, which is root-owned.
//     `defaults` takes an absolute path as a domain, and that is the
//     spelling a state managing machine-wide preferences uses.
//
// # What they touch
//
// Domains named `com.halite.selftest.user.<pid>` in the invoking
// account's store, and `/Library/Preferences/com.halite.selftest.system.<pid>`.
// Nothing else on the machine has either name, and a cleanup removes
// both whether the body passed or not.
//
// # Why they skip without root
//
// `HALITE_SYSTEM_LIVE=1` as well, and then `sudo`. Run them with
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveMacDefaults -v ./internal/builtin/
//
// `SUDO_USER` names the account to become, so the test does not have to
// be told twice who is running it, and refuses rather than guessing when
// it is not there.

func macDefaultsLiveRoot(t *testing.T) (*exec.Context, *user.User) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to run mac_defaults against the real `defaults`")
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_defaults is macOS's, and this is %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Skip("this drives the root-only paths; run it under sudo")
	}
	name := os.Getenv("SUDO_USER")
	if name == "" || name == "root" {
		t.Skip("SUDO_USER names the account to become, and it is not set to one")
	}
	target, err := user.Lookup(name)
	if err != nil {
		t.Fatalf("SUDO_USER %q: %v", name, err)
	}
	c := realCtx(t)
	if c.Which("defaults") == "" {
		t.Fatal("HALITE_SYSTEM_LIVE is set and there is no `defaults`; this is not a Mac")
	}
	return c, target
}

// The write lands in the named account's store, and not in root's.
func TestLiveMacDefaultsWritesAnotherAccountsDomain(t *testing.T) {
	c, target := macDefaultsLiveRoot(t)

	domain := fmt.Sprintf("com.halite.selftest.user.%d", os.Getpid())
	t.Cleanup(func() {
		_ = macDefaultsDelete(c, domain, "", target.Username)
		_ = macDefaultsDelete(c, domain, "", "")
	})

	if err := macDefaultsWrite(c, domain, "orientation", "string", "left", target.Username); err != nil {
		t.Fatalf("write as %s: %v", target.Username, err)
	}

	asTarget, err := macDefaultsExport(c, domain, target.Username)
	if err != nil {
		t.Fatalf("export as %s: %v", target.Username, err)
	}
	got, ok := asTarget.Get("orientation")
	if !ok {
		t.Fatalf("%s's domain %s has no orientation after writing one", target.Username, domain)
	}
	if !macDefaultsEqual(got, "left") {
		t.Errorf("read back %#v as %s, want %q", got, target.Username, "left")
	}

	// The claim that matters: root's own store was not what was written.
	asRoot, err := macDefaultsExport(c, domain, "")
	if err == nil {
		if _, leaked := asRoot.Get("orientation"); leaked {
			t.Errorf("the write meant for %s landed in root's preference store: "+
				"RunAs did not become the account", target.Username)
		}
	}

	// cfprefsd flushes when it chooses, so the backing file may not be
	// there yet. If it is, it belongs to the account, not to root.
	plist := target.HomeDir + "/Library/Preferences/" + domain + ".plist"
	if info, err := os.Stat(plist); err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("%s: no stat", plist)
		}
		want, err := strconv.ParseUint(target.Uid, 10, 32)
		if err != nil {
			t.Fatalf("uid %q: %v", target.Uid, err)
		}
		if uint64(stat.Uid) != want {
			t.Errorf("%s is owned by uid %d, want %d (%s)",
				plist, stat.Uid, want, target.Username)
		}
	}

	// And it comes back out of the account's store the same way.
	if err := macDefaultsDelete(c, domain, "orientation", target.Username); err != nil {
		t.Fatalf("delete as %s: %v", target.Username, err)
	}
	after, err := macDefaultsExport(c, domain, target.Username)
	if err != nil {
		t.Fatalf("export after delete: %v", err)
	}
	if _, still := after.Get("orientation"); still {
		t.Errorf("delete as %s left the key set", target.Username)
	}
}

// A machine-wide domain, which is a path and is root's to write.
func TestLiveMacDefaultsWritesASystemDomain(t *testing.T) {
	c, _ := macDefaultsLiveRoot(t)

	domain := fmt.Sprintf("/Library/Preferences/com.halite.selftest.system.%d", os.Getpid())
	t.Cleanup(func() {
		_ = macDefaultsDelete(c, domain, "", "")
		_ = os.Remove(domain + ".plist")
	})

	if err := macDefaultsWrite(c, domain, "PolicyBanner", "string", "halite selftest", ""); err != nil {
		t.Fatalf("write %s: %v", domain, err)
	}

	dict, err := macDefaultsExport(c, domain, "")
	if err != nil {
		t.Fatalf("export %s: %v", domain, err)
	}
	got, ok := dict.Get("PolicyBanner")
	if !ok {
		t.Fatalf("%s has no PolicyBanner after writing one", domain)
	}
	if !macDefaultsEqual(got, "halite selftest") {
		t.Errorf("read back %#v, want %q", got, "halite selftest")
	}

	// A system domain is a real file, and this one is root's.
	plist := domain + ".plist"
	info, err := os.Stat(plist)
	if err != nil {
		t.Fatalf("%s: %v", plist, err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
		t.Errorf("%s is owned by uid %d, want root", plist, stat.Uid)
	}

	// Removing the whole domain empties it. It does *not* remove the
	// file: macOS leaves an empty binary plist where the domain was, and
	// `defaults export` on it prints `<dict/>`. A test that demanded the
	// file be gone would be asserting something `defaults` never
	// promised -- the domain holding nothing is the state a tree asked
	// for.
	if err := macDefaultsDelete(c, domain, "", ""); err != nil {
		t.Fatalf("delete %s: %v", domain, err)
	}
	empty, err := macDefaultsExport(c, domain, "")
	if err != nil {
		t.Fatalf("export after deleting the domain: %v", err)
	}
	if empty.Len() != 0 {
		t.Errorf("%s still holds %d preference(s) after being deleted", domain, empty.Len())
	}

	// Deleting a domain that is already gone is not an error.
	if err := macDefaultsDelete(c, domain, "", ""); err != nil {
		t.Errorf("delete on an already-gone domain was an error: %v", err)
	}
}
