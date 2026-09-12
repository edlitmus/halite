package builtin

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// liveACLRegistry builds a bare Registries with only `acl` in it.
//
// registerACL is not wired into New() yet — the ledger entry and the
// call in builtin.go belong to whoever integrates this module — so a
// live test that used New() would find no acl.* functions at all.
func liveACLRegistry() *Registries {
	r := &Registries{Exec: exec.NewRegistry(), States: states.NewRegistry()}
	registerACL(r)
	return r
}

// TestLiveACLRoundTripsAnNFSv4EntryOnARealFile drives `acl.set`,
// `acl.get`, `acl.is_extended`, `acl.remove` and `acl.wipe` against a
// real getfacl/setfacl and a throwaway file on whatever filesystem
// t.TempDir() lands on — ZFS on the host this was written against,
// hence NFSv4 ACLs.
//
// No root and no HALITE_SYSTEM_LIVE gate: every mutation here is on a
// file this process owns inside its own temp directory, the same
// standing every non-live unit test in acl_test.go already assumes
// setfacl needs (verified live while writing this module — see
// acl.go's doc comment). What a fixture-based test cannot exercise is
// the real binary and its exit codes, which is what this proves.
func TestLiveACLRoundTripsAnNFSv4EntryOnARealFile(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skipf("this module speaks the FreeBSD getfacl/setfacl grammar; this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("getfacl") == "" || c.Which("setfacl") == "" {
		t.Skip("this host has no getfacl/setfacl")
	}
	me, err := user.Current()
	if err != nil {
		t.Skipf("the current user could not be looked up: %v", err)
	}

	r := liveACLRegistry()
	path := filepath.Join(t.TempDir(), "aclfile")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	set := value.MapOf("name", path, "tag", "user", "qualifier", me.Username, "perms", "rw", "type", "allow")
	out, err := r.Exec.Call(c, "acl.set", set)
	if err != nil {
		t.Fatalf("acl.set through the real setfacl: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Fatalf("adding a new ACL entry reported no change: %v", out)
	}

	got, err := r.Exec.Call(c, "acl.get", value.MapOf("name", path))
	if err != nil {
		t.Fatalf("acl.get through the real getfacl: %v", err)
	}
	entries, _ := got.(*value.Map).GetString("entries")
	var found *value.Map
	for _, e := range entries.([]any) {
		m := e.(*value.Map)
		tag, _ := m.GetString("tag")
		qualifier, _ := m.GetString("qualifier")
		if tag == "user" && qualifier == me.Username {
			found = m
		}
	}
	if found == nil {
		t.Fatalf("the entry acl.set just wrote is not in acl.get's answer: %v", entries)
	}
	if perms, _ := found.GetString("permissions"); perms != "rw------------" {
		t.Errorf("permissions read back as %v, want the canonical rw------------", perms)
	}

	// Idempotence: the tool itself does not tell setfacl -m from a
	// change, so this is entirely on canonicalPerms/canonicalFlags
	// reading getfacl's own column order correctly.
	again, err := r.Exec.Call(c, "acl.set", set)
	if err != nil {
		t.Fatalf("the second acl.set: %v", err)
	}
	if changed, _ := again.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("setting the same entry a second time reported a change: %v", again)
	}

	if extended, err := r.Exec.Call(c, "acl.is_extended", value.MapOf("name", path)); err != nil || extended != true {
		t.Errorf("acl.is_extended = %v, %v; a file with an extra entry is not trivial", extended, err)
	}

	removed, err := r.Exec.Call(c, "acl.remove", value.MapOf("name", path, "tag", "user", "qualifier", me.Username))
	if err != nil {
		t.Fatalf("acl.remove through the real setfacl: %v", err)
	}
	if changed, _ := removed.(*value.Map).GetString("changed"); changed != true {
		t.Fatalf("removing the entry acl.set wrote reported no change: %v", removed)
	}
	if extended, err := r.Exec.Call(c, "acl.is_extended", value.MapOf("name", path)); err != nil || extended != false {
		t.Errorf("acl.is_extended = %v, %v after removing the only extra entry", extended, err)
	}

	// wipe, exercised on a file with something to wipe.
	if _, err := r.Exec.Call(c, "acl.set", set); err != nil {
		t.Fatalf("re-adding the entry before testing wipe: %v", err)
	}
	wiped, err := r.Exec.Call(c, "acl.wipe", value.MapOf("name", path))
	if err != nil {
		t.Fatalf("acl.wipe through the real setfacl -b: %v", err)
	}
	if changed, _ := wiped.(*value.Map).GetString("changed"); changed != true {
		t.Fatalf("wiping an extended ACL reported no change: %v", wiped)
	}
	if extended, err := r.Exec.Call(c, "acl.is_extended", value.MapOf("name", path)); err != nil || extended != false {
		t.Errorf("acl.is_extended = %v, %v after acl.wipe", extended, err)
	}
}

// TestLivePOSIXOneACLIsRefusedByNameAgainstARealUFSFilesystem is the
// half acl_test.go could not cover: every filesystem reachable without
// root on the host this was written against is ZFS, which has no
// POSIX.1e ACL to capture. `parseACLEntryLine`'s POSIX.1e branch was
// written and tested against the three-field grammar quoted from
// setfacl(1)'s own manual page, not a live entry — this test makes one
// and points the module at it.
//
// `mount -o acls` is what turns POSIX.1e ACLs on for UFS (confirmed
// against mount(8) on this host: nfsv4acls is the other, mutually
// exclusive, option). Everything this test builds is inside a memory
// disk that is detached in cleanup regardless of pass or fail, the
// same shape live_quota_ufs_test.go uses and for the same reason: nothing
// here should still exist after `go test` exits.
func TestLivePOSIXOneACLIsRefusedByNameAgainstARealUFSFilesystem(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skipf("this leg makes a UFS filesystem with a POSIX.1e ACL; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this attach a memory disk")
	}
	if os.Geteuid() != 0 {
		t.Skip("attaching a memory disk and mounting it both need root")
	}
	c := &exec.Context{}
	for _, tool := range []string{"mdconfig", "newfs", "mount", "umount", "getfacl", "setfacl"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`", tool)
		}
	}

	res, err := c.Run(exec.Command{
		Argv:           []string{"mdconfig", "-a", "-t", "swap", "-s", "32m"},
		IgnoreExitCode: true,
	})
	if err != nil || res.Code != 0 {
		t.Skipf("a memory disk could not be attached: %v (exit %d: %s)", err, res.Code, strings.TrimSpace(res.Stderr))
	}
	unit := strings.TrimSpace(res.Stdout)
	if unit == "" || !strings.HasPrefix(unit, "md") {
		t.Skipf("mdconfig did not name the unit it attached; it said %q", unit)
	}
	dev := "/dev/" + unit
	t.Cleanup(func() {
		if _, err := c.Run(exec.Command{Argv: []string{"mdconfig", "-d", "-u", unit}, IgnoreExitCode: true}); err != nil {
			t.Logf("the memory disk %s could not be detached, which leaks 32 MiB of swap: %v", unit, err)
		}
	})

	if res, err := c.Run(exec.Command{Argv: []string{"newfs", "-U", dev}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
		t.Skipf("a UFS filesystem could not be made on %s: %v (%s)", dev, err, strings.TrimSpace(res.Stderr))
	}

	mount := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Run(exec.Command{Argv: []string{"mount", "-o", "acls", dev, mount}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
		t.Skipf("%s could not be mounted with POSIX.1e ACLs: %v (%s)", dev, err, strings.TrimSpace(res.Stderr))
	}
	t.Cleanup(func() {
		if res, err := c.Run(exec.Command{Argv: []string{"umount", mount}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
			t.Logf("%s could not be unmounted: %v (%s)", mount, err, strings.TrimSpace(res.Stderr))
		}
	})

	path := filepath.Join(mount, "posixfile")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The mandatory POSIX.1e triple plus a mask entry, straight out of
	// setfacl(1)'s own POSIX.1E ACL ENTRIES example.
	setRes, err := c.Run(exec.Command{
		Argv:           []string{"setfacl", "-m", "u::rwx,g::rx,o::rx,mask::rwx", path},
		IgnoreExitCode: true,
	})
	if err != nil || setRes.Code != 0 {
		t.Skipf("a real POSIX.1e ACL could not be set: %v (%s)", err, strings.TrimSpace(setRes.Stderr))
	}

	// The tool itself, asked what it just wrote, so the failure message
	// below can be judged against what was really there rather than
	// what this test assumed.
	getRes, err := c.Run(exec.Command{Argv: []string{"getfacl", "-q", path}, IgnoreExitCode: true})
	if err == nil {
		t.Logf("the real getfacl -q answer this test is pointing acl.get at:\n%s", getRes.Stdout)
	}

	r := liveACLRegistry()
	if _, err := r.Exec.Call(c, "acl.get", value.MapOf("name", path)); err == nil {
		t.Fatal("acl.get read a real POSIX.1e ACL without complaint; it should refuse the family by name")
	} else if !strings.Contains(err.Error(), "POSIX.1e") {
		t.Errorf("acl.get failed without naming POSIX.1e: %v", err)
	}

	// acl.is_extended never parses an entry, so the one family split
	// this module has should not reach it: it has to give a real answer
	// for a POSIX.1e path too.
	extended, err := r.Exec.Call(c, "acl.is_extended", value.MapOf("name", path))
	if err != nil {
		t.Fatalf("acl.is_extended refused a POSIX.1e path: %v", err)
	}
	if extended != true {
		t.Errorf("a file with a non-trivial POSIX.1e ACL (a mask entry) read as trivial: %v", extended)
	}
}
