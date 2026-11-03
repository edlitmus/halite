//go:build linux

package atomicfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// The attack of TestWriteDoesNotCopyAttributesThroughASwappedTemporaryName,
// made by a real second account against a root write into its directory.
//
// The unit test is its own "other account", so it shows the copy goes where
// the name points, but not that the account can move root's temporary file
// at all. This runs as root, gives a directory and the file in it to
// `nobody` (65534, which exists on every distribution the fleet runs and on
// the hosted runners), and has that account itself replace the temporary
// name with a symbolic link to a root-owned 0600 file outside its directory,
// at the moment between the write and the copy. Only the symbolic link: a
// hard link to a root file is what fs.protected_hardlinks=1 refuses the
// account, and the unit test has that case.
//
// Gated on HALITE_SYSTEM_LIVE=1 and root, like the other live tests. It
// writes only under its own temporary directories.
func TestLiveACLIsNotCopiedOntoAFileTheAccountLinked(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("HALITE_SYSTEM_LIVE=1 not set; this test needs root and a second account")
	}
	if os.Geteuid() != 0 {
		t.Skip("not root: the attack is a root write into another account's directory")
	}
	const account = 65534

	// Not t.TempDir, whose parent is 0700 and so out of the account's reach.
	base, err := os.MkdirTemp("", "halite-xattr-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".profile")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The account's file, with the attributes the account chose: an entry
	// naming itself rw-.
	if err := syscall.Setxattr(path, "system.posix_acl_access", aclXattr(account), 0); err != nil {
		t.Skipf("this filesystem takes no ACLs: %v", err)
	}
	for _, p := range []string{home, path} {
		if err := os.Lchown(p, account, account); err != nil {
			t.Fatal(err)
		}
	}
	// root's file, somewhere the account can name but not write.
	victim := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(victim, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ran := false
	beforeAttributes = func(tmp string) {
		cmd := exec.Command("sh", "-c", `rm -f -- "$1" && ln -s -- "$2" "$1"`, "sh", tmp, victim)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: account, Gid: account}}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("the account could not swap root's temporary file: %v\n%s", err, out)
			return
		}
		if fi, err := os.Lstat(tmp); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the temporary name is not the account's link: %v %v", fi, err)
			return
		}
		ran = true
	}
	defer func() { beforeAttributes = nil }()

	_ = Write(path, []byte("new\n"), 0o644)
	if !ran {
		t.Fatal("the account's swap never ran, so nothing was tested")
	}
	untouched(t, victim, 0o600)
	var st syscall.Stat_t
	if err := syscall.Stat(victim, &st); err != nil {
		t.Fatal(err)
	}
	if st.Uid != 0 {
		t.Errorf("the file outside the write is now owned by %d", st.Uid)
	}
}
