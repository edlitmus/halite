package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `quota`, driven against a real UFS filesystem with real BSD quotas.
//
// # Why this exists
//
// `quota`'s entry in evidence.go has said since DIVERGENCE 5.48 that the
// BSD half "has not been run": the fixed-width parser was written to the
// printf calls in FreeBSD's own usr.sbin/repquota/repquota.c, and
// `edquota -e` was checked only as an argument vector. The reason given
// was that this fleet is entirely ZFS and has no UFS filesystem to make
// one on.
//
// That premise was wrong, and it is the same premise the Linux leg had
// already beaten: a filesystem does not have to be one the machine came
// with. `mdconfig` makes a memory-backed disk, `newfs` puts UFS on it,
// and the whole thing is thrown away afterwards. The Linux leg does
// exactly this with a file and the loop driver.
//
// **Running the tools rather than reading them found two defects that
// every unit test in this package agreed with**, both of them on the
// platform that is four of this fleet's five hosts:
//
//   - `quotaon -p` is a Linux option. FreeBSD's quotaon has no -p, so
//     `quota.get_mode` could not answer at all. The module's own comment
//     asserted that both platforms had it.
//   - `repquota` asked about a filesystem that is not in fstab prints
//     its reason on stderr and **exits zero**, which the BSD branch read
//     as a report of no quotas.
//
// Both are fixed and both have unit tests. This is the leg that proves
// the rest of the BSD path against the tools themselves.
//
// # It is gated, and it touches /etc/fstab
//
// `HALITE_SYSTEM_LIVE=1` and root, the same gate as the Linux leg.
//
// The fstab entry is the part to read carefully. FreeBSD's repquota,
// quotacheck and quotaon all resolve their filesystem argument through
// getfsfile(3), so a mount that is not in fstab is one they refuse to
// look at -- that is the second defect above, arriving as a constraint.
// There is no flag that avoids it.
//
// So the entry is written with `noauto` and a pass number of 0, which
// between them mean that if this test is killed before its cleanup runs,
// the line it left behind still does nothing at the next boot: `noauto`
// keeps mount(8) from mounting it and a zero pass number keeps fsck and
// quotacheck from checking it. A leftover line is untidy. It is not a
// host that will not boot.
func TestLiveQuotaOnARealUFSFilesystem(t *testing.T) {
	img := liveUFSSetup(t)
	r := New()

	// Distinct numbers in all four positions, so a transposed pair shows
	// as a wrong value rather than as two that happen to match. This is
	// the `edquota -e` argument vector -- colon-packed onto the
	// filesystem with the name last -- being run for the first time.
	set := value.NewMap(7)
	set.Set("filesystem", img.mount)
	set.Set("name", "root")
	set.Set("kind", "user")
	set.Set("block_soft", int64(1024))
	set.Set("block_hard", int64(2048))
	set.Set("inode_soft", int64(100))
	set.Set("inode_hard", int64(200))
	out, err := r.Exec.Call(img.c, "quota.set", set)
	if err != nil {
		t.Fatalf("quota.set through the real edquota: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("setting a limit on a filesystem that had none reported no change: %v", out)
	}

	report := value.NewMap(2)
	report.Set("filesystem", img.mount)
	report.Set("kind", "user")
	got, err := r.Exec.Call(img.c, "quota.report", report)
	if err != nil {
		t.Fatalf("quota.report through the real repquota: %v", err)
	}
	m := got.(*value.Map)
	if format, _ := m.GetString("format"); format != "table" {
		t.Errorf("a BSD repquota was read as %v; the BSDs have no -O csv and this must be the table", format)
	}

	quotas, _ := m.GetString("quotas")
	entry, ok := quotas.(*value.Map).GetString("root")
	if !ok {
		t.Fatalf("a limit was set on root and the report does not name it: %v", quotas)
	}
	row := entry.(*value.Map)
	for field, want := range map[string]int64{
		"block_soft": 1024, "block_hard": 2048,
		"inode_soft": 100, "inode_hard": 200,
	} {
		v, _ := row.GetString(field)
		if v != want {
			t.Errorf("%s read back as %v, want %d -- either edquota packed the limits in the "+
				"wrong order or the fixed-width report was misaligned", field, v, want)
		}
	}

	// Idempotence, which is what makes this callable from a tree that
	// runs nightly.
	again, err := r.Exec.Call(img.c, "quota.set", set)
	if err != nil {
		t.Fatalf("the second quota.set: %v", err)
	}
	if changed, _ := again.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("setting the same limits a second time reported a change: %v", again)
	}
}

// The BSD mount flag is read off a filesystem that really has quotas.
//
// This is the half no fixture can settle. `quotaMountFlagSaysQuotas`
// parses what `mount` prints, and what `mount` prints for a quota'd UFS
// is the one string nothing here had ever seen -- sys/mount.h spells it
// "with quotas" and the unit test takes that spelling on the header's
// word. Here the kernel supplies it.
//
// Both states are read, because a reader that matched nothing and
// returned false would agree with the tool exactly once.
func TestLiveQuotaReadsTheBSDMountFlagInBothStates(t *testing.T) {
	img := liveUFSSetup(t)
	r := New()

	ask := value.NewMap(1)
	ask.Set("filesystem", img.mount)
	got, err := r.Exec.Call(img.c, "quota.get_mode", ask)
	if err != nil {
		t.Fatalf("quota.get_mode on a quota'd UFS: %v", err)
	}
	m := got.(*value.Map)
	if user, _ := m.GetString("user"); user != true {
		t.Errorf("quotas are on for this filesystem and get_mode says %v", user)
	}
	// The comment is part of the answer on this platform, not decoration:
	// MNT_QUOTA is one flag for both kinds and a caller must be told that
	// rather than left to read two keys as two readings.
	if comment, _ := m.GetString("comment"); !strings.Contains(fmt.Sprint(comment), "MNT_QUOTA") {
		t.Errorf("the BSD answer does not say that its two keys are one kernel flag: %v", comment)
	}

	off := value.NewMap(2)
	off.Set("filesystem", img.mount)
	off.Set("kind", "user")
	if _, err := r.Exec.Call(img.c, "quota.off", off); err != nil {
		t.Fatalf("quota.off: %v", err)
	}

	got, err = r.Exec.Call(img.c, "quota.get_mode", ask)
	if err != nil {
		t.Fatalf("quota.get_mode after switching off: %v", err)
	}
	if user, _ := got.(*value.Map).GetString("user"); user != false {
		t.Errorf("quotas were switched off and the mount flag still reads %v", user)
	}
}

// liveUFSImage is the memory-backed filesystem and where it is mounted.
type liveUFSImage struct {
	mount string
	c     *exec.Context
}

// liveUFSSetup builds a UFS filesystem with quotas, or skips saying what
// is missing.
func liveUFSSetup(t *testing.T) liveUFSImage {
	t.Helper()
	if runtime.GOOS != "freebsd" {
		t.Skipf("this leg makes a UFS filesystem with BSD quotas; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this attach a memory disk and write to /etc/fstab")
	}
	if os.Geteuid() != 0 {
		t.Skip("attaching a memory disk, mounting it and setting a quota all need root")
	}
	c := &exec.Context{}
	for _, tool := range []string{"mdconfig", "newfs", "mount", "umount", "quotacheck", "quotaon", "quotaoff", "edquota", "repquota"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`", tool)
		}
	}

	mount := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}

	// 64 MiB of swap-backed memory disk: enough for a filesystem with
	// room to put an account over a limit, and nothing on disk to clean
	// up if the machine goes down mid-test.
	dev := liveUFSAttach(t, c)
	if err := liveQuotaRun(c, "newfs", "-U", dev); err != nil {
		t.Skipf("a UFS filesystem could not be made on %s: %v", dev, err)
	}

	// **The fstab entry, and why it is safe to leave behind.**
	// repquota, quotacheck and quotaon all resolve their argument
	// through getfsfile(3), so none of them will look at a filesystem
	// that fstab does not name. `noauto` and pass 0 mean a line this
	// test fails to remove still does nothing at the next boot.
	liveUFSFstab(t, dev, mount)

	if err := liveQuotaRun(c, "mount", mount); err != nil {
		t.Skipf("the memory-backed filesystem could not be mounted: %v", err)
	}
	t.Cleanup(func() {
		if err := liveQuotaRun(c, "umount", mount); err != nil {
			t.Logf("the memory-backed filesystem could not be unmounted: %v", err)
		}
	})

	if err := liveQuotaRun(c, "quotacheck", "-u", "-g", mount); err != nil {
		t.Skipf("quotacheck could not build the quota files: %v", err)
	}
	if err := liveQuotaRun(c, "quotaon", "-u", "-g", mount); err != nil {
		t.Skipf("quotas could not be switched on: %v", err)
	}
	t.Cleanup(func() {
		if err := liveQuotaRun(c, "quotaoff", "-u", "-g", mount); err != nil {
			t.Logf("quotas could not be switched off: %v", err)
		}
	})

	liveQuotaProbe(t, c, "the mount as the kernel reports it", "mount")
	return liveUFSImage{mount: mount, c: c}
}

// liveUFSAttach attaches a memory disk and registers its detach.
//
// The unit is read back from mdconfig's own output rather than assumed,
// because -u is not passed: picking a unit number would collide with
// whatever this host already has attached, and this host is a live
// target rather than a runner.
func liveUFSAttach(t *testing.T, c *exec.Context) string {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv:           []string{"mdconfig", "-a", "-t", "swap", "-s", "64m"},
		IgnoreExitCode: true,
	})
	if err != nil || res.Code != 0 {
		t.Skipf("a memory disk could not be attached: %v (exit %d: %s)",
			err, res.Code, strings.TrimSpace(res.Stderr))
	}
	unit := strings.TrimSpace(res.Stdout)
	if unit == "" || !strings.HasPrefix(unit, "md") {
		t.Skipf("mdconfig did not name the unit it attached; it said %q", unit)
	}
	t.Cleanup(func() {
		if err := liveQuotaRun(c, "mdconfig", "-d", "-u", unit); err != nil {
			t.Logf("the memory disk %s could not be detached, which leaks 64 MiB of swap: %v", unit, err)
		}
	})
	return "/dev/" + unit
}

// liveUFSFstab appends the entry the quota tools insist on and removes
// it again afterwards.
//
// The line is matched and removed exactly, rather than the file being
// restored wholesale from a copy: this host is one halite manages, and a
// test that rewrites /etc/fstab from a snapshot would undo anything that
// changed it while the test was running.
func liveUFSFstab(t *testing.T, dev, mount string) {
	t.Helper()
	const path = "/etc/fstab"
	line := fmt.Sprintf("%s\t%s\tufs\trw,noauto,userquota,groupquota\t0\t0\n", dev, mount)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("%s could not be read: %v", path, err)
	}
	if !strings.HasSuffix(string(before), "\n") {
		line = "\n" + line
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Skipf("%s could not be appended to: %v", path, err)
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		t.Fatalf("the fstab entry could not be written: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("the fstab entry could not be written: %v", err)
	}

	t.Cleanup(func() {
		current, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s could not be read back, so the test's own entry may still be in it: %v", path, err)
			return
		}
		trimmed := strings.Replace(string(current), line, "", 1)
		if trimmed == string(current) {
			t.Errorf("this test's fstab entry is not in %s to remove; it reads:\n%s", path, current)
			return
		}
		if err := os.WriteFile(path, []byte(trimmed), 0o644); err != nil {
			t.Errorf("this test's entry could not be removed from %s -- remove %q by hand: %v",
				path, strings.TrimSpace(line), err)
		}
	})
}
