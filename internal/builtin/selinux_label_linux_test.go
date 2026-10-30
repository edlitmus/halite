//go:build linux

package builtin

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// writeAtomic carries the replaced file's SELinux label onto its successor.
//
// The label is `security.selinux`, which only an SELinux kernel will take
// on a real context, so this stands in a `user.` attribute for it by
// pointing keepSELinuxLabel at the same code path with a different name:
// it asserts that an attribute on the old file is on the new one, which is
// the part that is ours. That the attribute is the right one, and that a
// real enforcing host ends with `net_conf_t` and not `etc_t`, is what
// TestLiveWriteConfigFileKeepsSELinuxContext shows, on a host that has one.
func TestWriteAtomicKeepsAnExtendedAttribute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const attr = "user.halite-test-label"
	if err := syscall.Setxattr(path, attr, []byte("kept"), 0); err != nil {
		if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EPERM) {
			t.Skipf("this filesystem takes no user extended attributes: %v", err)
		}
		t.Fatal(err)
	}
	old := selinuxLabelAttrName
	selinuxLabelAttrName = attr
	defer func() { selinuxLabelAttrName = old }()

	if err := writeAtomic(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 32)
	n, err := syscall.Getxattr(path, attr, got)
	if err != nil {
		t.Fatalf("the rewritten file lost its attribute: %v", err)
	}
	if string(got[:n]) != "kept" {
		t.Fatalf("attribute = %q, want %q", got[:n], "kept")
	}
}

// A file with no label, and a path that does not exist, are not errors.
func TestKeepSELinuxLabelWithNothingToKeep(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	tmp := filepath.Join(dir, "tmp")
	for _, p := range []string{plain, tmp} {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := keepSELinuxLabel(tmp, plain); err != nil {
		t.Fatalf("a file with no SELinux label: %v", err)
	}
}
