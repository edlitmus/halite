//go:build !windows

package permtest

import (
	"os"
	"testing"
)

// OpenToEveryone makes the file readable by every account.
func OpenToEveryone(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
}

// MakePrivate makes the file readable by its owner alone.
func MakePrivate(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

// refuseRoot skips a test that root cannot be made to fail.
//
// Root holds CAP_DAC_OVERRIDE, so a mode that denies access denies it to
// everyone except the account setting it. A caller that chmods a
// directory to 0500 as root and then asserts a refusal gets a write that
// succeeds and an assertion that fails, naming the code under test —
// which is what happened the first time the suite ran in a container,
// and cost an hour of looking at the wrong thing.
//
// Skipped rather than failed: running the suite as root is a reasonable
// thing to do in a container, and one test that cannot express itself
// there should not stop the other four thousand. Skipped rather than
// quietly passed for the reason this package exists at all — a test that
// cannot make the condition it is testing for is not testing for it, and
// the honest report of that is a skip that says so.
//
// The containers this project ships do not need it: contrib/docker/race
// runs as an ordinary account precisely so this does not fire.
func refuseRoot(t *testing.T, what string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skipf("this test needs an unprivileged account: root ignores %s", what)
	}
}

// DenyWrite makes a directory one this process cannot create a file in.
func DenyWrite(t *testing.T, dir string) {
	t.Helper()
	refuseRoot(t, "a directory's write bits")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// AllowWrite undoes DenyWrite.
func AllowWrite(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

// DenyRead makes a file this process cannot read.
func DenyRead(t *testing.T, path string) {
	t.Helper()
	refuseRoot(t, "a file's read bits")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
}

// AllowRead undoes DenyRead.
func AllowRead(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
