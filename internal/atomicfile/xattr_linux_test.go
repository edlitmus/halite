//go:build linux

package atomicfile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// Write carries the replaced file's extended attributes onto its
// successor.
//
// The one that matters most on an SELinux host is `security.selinux`, which
// only an SELinux kernel will take a real context for, so this stands in a
// `user.` attribute: it asserts that every attribute on the old file is on the
// new one, which is the part that is ours. That a real enforcing host ends
// with `net_conf_t` and not `etc_t` is what
// TestLiveWriteConfigFileKeepsSELinuxContext shows, on a host that has one.
// Two attributes are set, so a copy of "the label" and nothing else fails.
func TestWriteAtomicKeepsExtendedAttributes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"user.halite-test-label": "kept", "user.halite-test-other": "also kept"}
	for attr, val := range want {
		if err := syscall.Setxattr(path, attr, []byte(val), 0); err != nil {
			if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EPERM) {
				t.Skipf("this filesystem takes no user extended attributes: %v", err)
			}
			t.Fatal(err)
		}
	}

	if err := Write(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for attr, val := range want {
		got := make([]byte, 64)
		n, err := syscall.Getxattr(path, attr, got)
		if err != nil {
			t.Fatalf("the rewritten file lost %s: %v", attr, err)
		}
		if string(got[:n]) != val {
			t.Errorf("%s = %q, want %q", attr, got[:n], val)
		}
	}
}

// A file with no extended attributes is not an error.
func TestKeepExtendedAttributesWithNothingToKeep(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	tmp := filepath.Join(dir, "tmp")
	for _, p := range []string{plain, tmp} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := keepExtendedAttributes(tmp, plain, 0o644); err != nil {
		t.Fatalf("a file with no extended attributes: %v", err)
	}
}

// Write carries a POSIX ACL onto the file that replaces the old one.
//
// An ACL is the extended attribute `system.posix_acl_access`, and a file
// rewritten through a temporary file and a rename lost it: the new inode had
// none, so a `user:backup:r--` entry an administrator had added to a config
// file was gone after the first state that touched it, silently widening or
// narrowing who could read it. Salt writes in place and keeps it (5.471, read
// in its source). This one uses the real setfacl and getfacl, on whatever
// filesystem the temporary directory is on, and skips when either is missing
// or the filesystem takes no ACLs.
func TestWriteAtomicKeepsAnACL(t *testing.T) {
	setfacl, err1 := exec.LookPath("setfacl")
	getfacl, err2 := exec.LookPath("getfacl")
	if err1 != nil || err2 != nil {
		t.Skip("no setfacl and getfacl on this machine")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Our own uid: the ACL names a user that exists, and nothing here needs
	// another account.
	entry := "user:" + strconv.Itoa(os.Getuid()) + ":r--"
	if out, err := exec.Command(setfacl, "-m", entry, path).CombinedOutput(); err != nil {
		t.Skipf("this filesystem takes no ACLs: %v\n%s", err, out)
	}
	show := func() string {
		out, err := exec.Command(getfacl, "-c", "-p", path).CombinedOutput()
		if err != nil {
			t.Fatalf("getfacl: %v\n%s", err, out)
		}
		return string(out)
	}
	before := show()

	if err := Write(path, []byte("new\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if after := show(); after != before {
		t.Fatalf("the rewritten file's ACL changed\nbefore:\n%safter:\n%s", before, after)
	}
	if got, _ := os.ReadFile(path); string(got) != "new\n" {
		t.Fatalf("contents = %q", got)
	}
	// The mode the caller sees is the ACL mask, and it survived too.
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", fi.Mode().Perm())
	}
}

// A narrower mode than the old file's ACL mask is not widened by the ACL.
//
// Setting `system.posix_acl_access` sets the group bits to the ACL's mask, so
// copying the ACL after the temporary file got its mode would turn a 0600
// write over a file with a `user:<uid>:r--` entry into one that entry can
// read. The caller asked for 0600; the copied entry survives, with nothing
// effective under the new mask.
func TestWriteKeepsAnACLNoWiderThanTheModeAsked(t *testing.T) {
	setfacl, err1 := exec.LookPath("setfacl")
	getfacl, err2 := exec.LookPath("getfacl")
	if err1 != nil || err2 != nil {
		t.Skip("no setfacl and getfacl on this machine")
	}
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	entry := "user:" + strconv.Itoa(os.Getuid()) + ":r--"
	if out, err := exec.Command(setfacl, "-m", entry, path).CombinedOutput(); err != nil {
		t.Skipf("this filesystem takes no ACLs: %v\n%s", err, out)
	}
	if err := Write(path, []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 (the ACL's mask widened it)", fi.Mode().Perm())
	}
	out, err := exec.Command(getfacl, "-c", "-p", path).CombinedOutput()
	if err != nil {
		t.Fatalf("getfacl: %v\n%s", err, out)
	}
	// getfacl names the user, so the entry is found by its shape: a named user
	// entry that is r-- and has nothing effective.
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "user:") && !strings.HasPrefix(line, "user::") &&
			strings.Contains(line, ":r--") && strings.Contains(line, "#effective:---") {
			found = true
		}
	}
	if !found {
		t.Errorf("the named-user r-- entry was not carried over with nothing effective:\n%s", out)
	}
	if !strings.Contains(string(out), "mask::---") {
		t.Errorf("the mask is not the 0600 file's group bits:\n%s", out)
	}
}
