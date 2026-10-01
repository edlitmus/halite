package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The acl states, driven against the real getfacl and setfacl.
//
// # Where each family is found
//
// POSIX.1e: on Linux, wherever the temporary directory is (tmpfs on the
// Debian 13 lab host, ext4 on a GitHub runner) and /var/tmp beside it;
// on FreeBSD, a UFS filesystem on a memory disk mounted `-o acls`,
// because the lab host's root is UFS with neither ACL option and
// answers setfacl with "Operation not supported".
//
// NFSv4 (FreeBSD only): a UFS memory disk mounted `-o nfsv4acls`, and a
// ZFS pool on a file when the zfs module is already loaded -- ZFS being
// what this fleet's FreeBSD hosts actually run. The module is never
// loaded by the test: a disposable VM without it skips that leg and
// says so.
//
// Every assertion reads the real getfacl, not the module's own reader:
// a writer and a reader wrong in the same direction would agree with
// each other and prove nothing.

func liveACLStateGate(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to drive the acl states against the real getfacl and setfacl")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		t.Skipf("acl runs on Linux and FreeBSD; this is %s", runtime.GOOS)
	}
	c := realCtx(t)
	for _, tool := range []string{"getfacl", "setfacl"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`; on Linux it is the `acl` package", tool)
		}
	}
	return c
}

// liveGetfacl is the real tool's answer, for the assertions.
func liveGetfacl(t *testing.T, c *exec.Context, args ...string) string {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: append([]string{"getfacl"}, args...), IgnoreExitCode: true})
	if err != nil {
		t.Fatalf("getfacl %v: %v", args, err)
	}
	return res.Stdout
}

// liveApply applies a state through the registry, as a node would.
func liveApply(t *testing.T, test bool, name string, kv ...any) states.Result {
	t.Helper()
	c := realCtx(t)
	c.Test = test
	res, err := New().States.Call(c, name, value.MapOf(kv...))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func wantChanged(t *testing.T, what string, res states.Result) {
	t.Helper()
	if res.Result == nil || !*res.Result || !res.HasChanges() {
		t.Fatalf("%s = %+v, want a success with changes", what, res)
	}
}

func wantConverged(t *testing.T, what string, res states.Result) {
	t.Helper()
	if res.Result == nil || !*res.Result || res.HasChanges() {
		t.Fatalf("%s = %+v, want a success with nothing to change", what, res)
	}
}

func wantPredicted(t *testing.T, what string, res states.Result) {
	t.Helper()
	if res.Result != nil || !res.HasChanges() {
		t.Fatalf("%s = %+v, want test mode's nil result with the change predicted", what, res)
	}
}

func TestLiveACLStatesOnPOSIXOneACLs(t *testing.T) {
	c := liveACLStateGate(t)
	switch runtime.GOOS {
	case "linux":
		t.Run("tmp", func(t *testing.T) { liveACLPOSIXStates(t, c, t.TempDir()) })
		t.Run("var-tmp", func(t *testing.T) {
			dir, err := os.MkdirTemp("/var/tmp", "halite-acl-")
			if err != nil {
				t.Skipf("/var/tmp could not be used: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			liveACLPOSIXStates(t, c, dir)
		})
	case "freebsd":
		liveACLPOSIXStates(t, c, liveUFSWithACLs(t, c, "acls"))
	}
}

func liveACLPOSIXStates(t *testing.T, c *exec.Context, dir string) {
	t.Logf("%s:\n%s", dir, mountLine(c, dir))
	f := filepath.Join(dir, "f")
	d := filepath.Join(dir, "d")
	for _, p := range []string{d, filepath.Join(d, "sub")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{f, filepath.Join(d, "inner")} {
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("inner", filepath.Join(d, "link")); err != nil {
		t.Fatal(err)
	}
	if out := liveGetfacl(t, c, f); strings.Count(out, ":") < 3 || strings.Contains(out, "allow") {
		t.Fatalf("%s does not speak POSIX.1e:\n%s", dir, out)
	}

	user := []any{"name", f, "acl_type", "user", "acl_name", "nobody", "perms", "rwx"}

	before := liveGetfacl(t, c, f)
	wantPredicted(t, "test mode", liveApply(t, true, "acl.present", user...))
	if after := liveGetfacl(t, c, f); after != before {
		t.Fatalf("test mode changed the ACL:\n%s\nbecame\n%s", before, after)
	}
	wantChanged(t, "acl.present", liveApply(t, false, "acl.present", user...))
	if out := liveGetfacl(t, c, f); !strings.Contains(out, "user:nobody:rwx") {
		t.Fatalf("the real getfacl does not show the entry:\n%s", out)
	}
	wantConverged(t, "a second acl.present", liveApply(t, false, "acl.present", user...))
	wantConverged(t, "test mode once applied", liveApply(t, true, "acl.present", user...))

	narrower := []any{"name", f, "acl_type", "user", "acl_name", "nobody", "perms", "r"}
	wantChanged(t, "acl.present to r", liveApply(t, false, "acl.present", narrower...))
	if out := liveGetfacl(t, c, f); !strings.Contains(out, "user:nobody:r--") {
		t.Fatalf("narrowing to r did not reach the file:\n%s", out)
	}
	wantConverged(t, "a second acl.present to r", liveApply(t, false, "acl.present", narrower...))

	group := []any{"name", f, "acl_type", "group", "acl_name", "nogroup", "perms", "rx"}
	wantChanged(t, "acl.present group", liveApply(t, false, "acl.present", group...))
	if out := liveGetfacl(t, c, f); !strings.Contains(out, "group:nogroup:r-x") {
		t.Fatalf("the group entry is not on the file:\n%s", out)
	}
	wantConverged(t, "a second acl.present group", liveApply(t, false, "acl.present", group...))

	// The owner's own entry: Salt's empty acl_name.
	owner := []any{"name", f, "acl_type", "user", "acl_name", "", "perms", "rw"}
	wantConverged(t, "the owner's entry, already rw", liveApply(t, false, "acl.present", owner...))

	// A default ACL, recursively: on the two directories and nothing else.
	defaults := []any{"name", d, "acl_type", "default:user", "acl_name", "nobody", "perms", "rx", "recurse", true}
	predicted := liveApply(t, true, "acl.present", defaults...)
	wantPredicted(t, "test mode, default recurse", predicted)
	if out := liveGetfacl(t, c, "-d", d); strings.Contains(out, "nobody") {
		t.Fatalf("test mode set a default entry:\n%s", out)
	}
	applied := liveApply(t, false, "acl.present", defaults...)
	wantChanged(t, "default recurse", applied)
	if applied.Changes.Len() != 2 {
		t.Errorf("default recurse changed %d paths, want the 2 directories: %v", applied.Changes.Len(), applied.Changes)
	}
	for _, p := range []string{d, filepath.Join(d, "sub")} {
		if out := liveGetfacl(t, c, "-d", p); !strings.Contains(out, "user:nobody:r-x") {
			t.Errorf("%s has no default entry:\n%s", p, out)
		}
	}
	if out := liveGetfacl(t, c, filepath.Join(d, "inner")); strings.Contains(out, "nobody") {
		t.Errorf("a file under the tree was given an entry:\n%s", out)
	}
	wantConverged(t, "a second default recurse", liveApply(t, false, "acl.present", defaults...))
	wantChanged(t, "acl.absent default recurse", liveApply(t, false, "acl.absent", defaults...))
	for _, p := range []string{d, filepath.Join(d, "sub")} {
		if out := liveGetfacl(t, c, "-d", p); strings.Contains(out, "nobody") {
			t.Errorf("%s still has the default entry:\n%s", p, out)
		}
	}
	wantConverged(t, "a second acl.absent default recurse", liveApply(t, false, "acl.absent", defaults...))

	// An access entry, recursively: the directories and the file, and
	// not the symlink.
	access := []any{"name", d, "acl_type", "user", "acl_name", "nobody", "perms", "r", "recurse", true}
	applied = liveApply(t, false, "acl.present", access...)
	wantChanged(t, "access recurse", applied)
	if applied.Changes.Len() != 3 {
		t.Errorf("access recurse changed %d paths, want d, d/sub and d/inner: %v", applied.Changes.Len(), applied.Changes)
	}
	if applied.Changes.Has(filepath.Join(d, "link")) {
		t.Error("the symlink was changed")
	}
	wantConverged(t, "a second access recurse", liveApply(t, false, "acl.present", access...))
	wantChanged(t, "acl.absent access recurse", liveApply(t, false, "acl.absent", access...))
	if out := liveGetfacl(t, c, filepath.Join(d, "inner")); strings.Contains(out, "nobody") {
		t.Errorf("the entry is still under the tree:\n%s", out)
	}

	absent := []any{"name", f, "acl_type", "user", "acl_name", "nobody"}
	wantPredicted(t, "test mode, absent", liveApply(t, true, "acl.absent", absent...))
	if out := liveGetfacl(t, c, f); !strings.Contains(out, "user:nobody:") {
		t.Fatalf("test mode removed the entry:\n%s", out)
	}
	wantChanged(t, "acl.absent", liveApply(t, false, "acl.absent", absent...))
	if out := liveGetfacl(t, c, f); strings.Contains(out, "user:nobody:") {
		t.Fatalf("the entry is still there:\n%s", out)
	}
	wantConverged(t, "a second acl.absent", liveApply(t, false, "acl.absent", absent...))

	list := []any{"name", f, "acl_type", "user", "acl_names", []any{"nobody", "daemon"}, "perms", "r"}
	wantChanged(t, "acl.list_present", liveApply(t, false, "acl.list_present", list...))
	if out := liveGetfacl(t, c, f); !strings.Contains(out, "user:nobody:r--") || !strings.Contains(out, "user:daemon:r--") {
		t.Fatalf("both names are not on the file:\n%s", out)
	}
	wantConverged(t, "a second acl.list_present", liveApply(t, false, "acl.list_present", list...))
	// Salt's list_absent takes no perms, so neither does this one.
	unlist := list[:len(list)-2]
	wantChanged(t, "acl.list_absent", liveApply(t, false, "acl.list_absent", unlist...))
	if out := liveGetfacl(t, c, f); strings.Contains(out, "user:nobody") || strings.Contains(out, "user:daemon") {
		t.Fatalf("list_absent left an entry:\n%s", out)
	}
	wantConverged(t, "a second acl.list_absent", liveApply(t, false, "acl.list_absent", unlist...))

	// What the tool refuses is refused by the state, with nothing changed.
	before = liveGetfacl(t, c, f)
	if res := liveApply(t, false, "acl.present", "name", f, "acl_type", "user", "acl_name", "halite_no_such_user", "perms", "r"); !res.Failed() {
		t.Errorf("an account that does not exist = %+v, want a failure", res)
	}
	if res := liveApply(t, false, "acl.present", "name", f, "acl_type", "default:user", "acl_name", "nobody", "perms", "r"); !res.Failed() {
		t.Errorf("a default entry on a file = %+v, want a failure", res)
	}
	if after := liveGetfacl(t, c, f); after != before {
		t.Errorf("a refused state changed the ACL:\n%s\nbecame\n%s", before, after)
	}
}

func TestLiveACLStatesOnNFSv4ACLs(t *testing.T) {
	c := liveACLStateGate(t)
	if runtime.GOOS != "freebsd" {
		t.Skipf("Linux's getfacl speaks POSIX.1e only; this is %s", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		t.Skip("the NFSv4 filesystems here are built on a memory disk or a pool, which needs root")
	}
	t.Run("ufs-nfsv4acls", func(t *testing.T) { liveACLNFSv4States(t, c, liveUFSWithACLs(t, c, "nfsv4acls")) })
	t.Run("zfs", func(t *testing.T) { liveACLNFSv4States(t, c, liveZFSScratch(t, c)) })
}

func liveACLNFSv4States(t *testing.T, c *exec.Context, dir string) {
	t.Logf("%s:\n%s", dir, mountLine(c, dir))
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := liveGetfacl(t, c, f); !strings.Contains(out, "owner@:") {
		t.Fatalf("%s does not speak NFSv4:\n%s", dir, out)
	}
	user := []any{"name", f, "acl_type", "user", "acl_name", "nobody", "perms", "rwx"}
	before := liveGetfacl(t, c, f)
	wantPredicted(t, "test mode", liveApply(t, true, "acl.present", user...))
	if after := liveGetfacl(t, c, f); after != before {
		t.Fatalf("test mode changed the ACL:\n%s\nbecame\n%s", before, after)
	}
	wantChanged(t, "acl.present", liveApply(t, false, "acl.present", user...))
	if out := liveGetfacl(t, c, f); !strings.Contains(out, "user:nobody:rwx-----------:-------:allow") {
		t.Fatalf("the real getfacl does not show an allow entry for rwx:\n%s", out)
	}
	wantConverged(t, "a second acl.present", liveApply(t, false, "acl.present", user...))
	wantConverged(t, "test mode once applied", liveApply(t, true, "acl.present", user...))

	everyone := []any{"name", f, "acl_type", "everyone@", "perms", "read_set"}
	wantChanged(t, "acl.present everyone@", liveApply(t, false, "acl.present", everyone...))
	wantConverged(t, "a second acl.present everyone@", liveApply(t, false, "acl.present", everyone...))

	if res := liveApply(t, false, "acl.present", "name", f, "acl_type", "d:user", "acl_name", "nobody", "perms", "r"); !res.Failed() || !strings.Contains(res.Comment, "NFSv4") {
		t.Errorf("a default entry on NFSv4 = %+v, want a refusal naming NFSv4", res)
	}

	absent := []any{"name", f, "acl_type", "user", "acl_name", "nobody"}
	wantChanged(t, "acl.absent", liveApply(t, false, "acl.absent", absent...))
	if out := liveGetfacl(t, c, f); strings.Contains(out, "user:nobody:") {
		t.Fatalf("the entry is still there:\n%s", out)
	}
	wantConverged(t, "a second acl.absent", liveApply(t, false, "acl.absent", absent...))
}

// liveZFSScratch makes a pool on a 128 MiB file and returns its mount
// point, destroying it in cleanup. The pool's cachefile is none, so the
// host's zpool.cache is not touched.
func liveZFSScratch(t *testing.T, c *exec.Context) string {
	t.Helper()
	for _, tool := range []string{"zpool", "kldstat"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`", tool)
		}
	}
	if res, err := c.Run(exec.Command{Argv: []string{"kldstat", "-q", "-m", "zfs"}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
		t.Skip("the zfs kernel module is not loaded, and this test does not load one into the host's kernel")
	}
	img := filepath.Join(t.TempDir(), "pool.img")
	f, err := os.Create(img)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(128 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	pool := "halitelive" + strings.ReplaceAll(filepath.Base(filepath.Dir(img)), "-", "")
	if len(pool) > 40 {
		pool = pool[:40]
	}
	mnt := filepath.Join(t.TempDir(), "zfs")
	res, err := c.Run(exec.Command{
		Argv:           []string{"zpool", "create", "-o", "cachefile=none", "-m", mnt, pool, img},
		IgnoreExitCode: true,
	})
	if err != nil || res.Code != 0 {
		t.Skipf("a pool could not be made on a file: %v (%s)", err, strings.TrimSpace(res.Stderr))
	}
	t.Cleanup(func() {
		if res, err := c.Run(exec.Command{Argv: []string{"zpool", "destroy", pool}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
			t.Errorf("the pool %s could not be destroyed: %v (%s)", pool, err, strings.TrimSpace(res.Stderr))
		}
	})
	return mnt
}

// mountLine is the mount table's line for the filesystem under a path,
// logged so that a run says which filesystem and options it ran on.
func mountLine(c *exec.Context, path string) string {
	res, err := c.Run(exec.Command{Argv: []string{"df", path}, IgnoreExitCode: true})
	if err != nil {
		return err.Error()
	}
	fields := strings.Fields(lastLine(res.Stdout))
	if len(fields) == 0 {
		return res.Stdout
	}
	mounts, err := c.Run(exec.Command{Argv: []string{"mount"}, IgnoreExitCode: true})
	if err != nil {
		return err.Error()
	}
	// By mount point, the last column of df on both platforms: a device
	// name like Linux's `tmpfs` is shared by several mounts.
	for _, line := range strings.Split(mounts.Stdout, "\n") {
		if strings.Contains(line, " on "+fields[len(fields)-1]+" ") {
			return line
		}
	}
	return lastLine(res.Stdout)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// acl.wipe on a POSIX.1e ACL must leave a trivial one, and say so once.
//
// FreeBSD's `setfacl -b` on a POSIX.1e file keeps the mask entry -- the
// file still lists `mask::r--` and `ls` still marks it `+` -- so a wipe
// that ran `-b` alone reported a change on every run and never left a
// trivial ACL. Linux's `-b` removes the mask with the rest. Measured on
// both lab hosts on 2026-09-30 (DIVERGENCE 5.185).
func TestLiveACLWipeLeavesATrivialPOSIXOneACL(t *testing.T) {
	c := liveACLStateGate(t)
	dir := t.TempDir()
	if runtime.GOOS == "freebsd" {
		dir = liveUFSWithACLs(t, c, "acls")
	}
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := c.Run(exec.Command{Argv: []string{"setfacl", "-m", "user:nobody:rwx", f}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
		t.Fatalf("setfacl: %v %s", err, res.Stderr)
	}
	r := New()
	out, err := r.Exec.Call(c, "acl.wipe", value.MapOf("name", f))
	if err != nil {
		t.Fatalf("acl.wipe: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Fatalf("wiping an extended ACL reported no change: %v", out)
	}
	if got := liveGetfacl(t, c, f); strings.Contains(got, "mask::") || strings.Contains(got, "nobody") {
		t.Errorf("acl.wipe left more than the trivial ACL:\n%s", got)
	}
	again, err := r.Exec.Call(c, "acl.wipe", value.MapOf("name", f))
	if err != nil {
		t.Fatalf("a second acl.wipe: %v", err)
	}
	if changed, _ := again.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("a second acl.wipe reported a change, so the first did not finish: %v", again)
	}
}
