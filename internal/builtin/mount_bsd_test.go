package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// bsdMountCtx makes this module speak FreeBSD's mount for one test, with
// `mount` answering as beastie's did (FreeBSD 15.1-RELEASE-p5, captured
// 2026-10-16) for a tmpfs mounted rw and then updated to ro:
//
//	tmpfs on /tmp/h44.gxEE/m (tmpfs, local)
//	tmpfs on /tmp/h44.gxEE/m (tmpfs, local, read-only)
func bsdMountCtx(t *testing.T, mountOut string) (*exec.Context, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mount.mounted does not run on Windows, and this build says so")
	}
	old, oldProc := mountGOOS, ProcMountsPath
	mountGOOS = "freebsd"
	ProcMountsPath = filepath.Join(t.TempDir(), "no-proc-mounts")
	t.Cleanup(func() { mountGOOS, ProcMountsPath = old, oldProc })
	c := newCtx(false)
	c.Grains = freebsdGrains()
	c.Runner = &exec.RecordingRunner{Responses: map[string]exec.Result{"mount": {Stdout: mountOut}}}
	return c, filepath.Join(t.TempDir(), "fstab")
}

// **`defaults` is Linux's**, and on a BSD it becomes the mode it stands
// for, in the table and on the command line alike (issue 321, M-44).
func TestPlatformMountOpts(t *testing.T) {
	for _, tc := range []struct{ goos, in, want string }{
		{"freebsd", "defaults", "rw"},
		{"freebsd", "defaults,noatime", "rw,noatime"},
		// A table line with no mode is "Inappropriate file type or
		// format" to FreeBSD's getfsent, so a mode is supplied.
		{"freebsd", "noatime", "rw,noatime"},
		{"freebsd", "ro", "ro"},
		{"freebsd", "defaults,ro", "ro"},
		{"freebsd", "rw,noexec", "rw,noexec"},
		{"darwin", "defaults", "rw"},
		{"openbsd", "defaults,nodev", "rw,nodev"},
		{"linux", "defaults", "defaults"},
		{"linux", "noatime", "noatime"},
	} {
		if got := platformMountOpts(tc.goos, tc.in); got != tc.want {
			t.Errorf("%s %q = %q, want %q", tc.goos, tc.in, got, tc.want)
		}
	}
}

// A state that omits `opts` writes a line FreeBSD mounts, mounts with
// options FreeBSD accepts, and converges.
func TestMountedOnFreeBSDWritesNoDefaults(t *testing.T) {
	r := New()
	c, path := bsdMountCtx(t, "")
	args := value.MapOf("name", "/srv/scratch", "device", "tmpfs", "fstype", "tmpfs", "config", path)

	res, err := r.States.Call(c, "mount.mounted", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("mount.mounted failed: %s", res.Comment)
	}
	assertRan(t, c, "mount -t tmpfs -o rw tmpfs /srv/scratch")
	raw, _ := os.ReadFile(path)
	if got, want := string(raw), "tmpfs\t/srv/scratch\ttmpfs\trw\t0 0\n"; got != want {
		t.Errorf("the table holds %q, want %q", got, want)
	}

	c, _ = bsdMountCtx(t, "tmpfs on /srv/scratch (tmpfs, local)\n")
	res, err = r.States.Call(c, "mount.mounted", args)
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the second run changed something: %s %v", res.Comment, res.Changes)
	}
	assertChangedNothing(t, c)
}

// A remount is `mount -u` on a BSD: `-o remount` is Linux's, and
// beastie's mount answered it with "unknown special file or file system".
func TestMountedOnFreeBSDRemountsWithU(t *testing.T) {
	r := New()
	c, path := bsdMountCtx(t, "tmpfs on /srv/scratch (tmpfs, local)\n")
	writeFile(t, path, "tmpfs\t/srv/scratch\ttmpfs\tro\t0 0\n")

	res, err := r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/srv/scratch", "device", "tmpfs", "fstype", "tmpfs", "opts", "ro", "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("the remount failed: %s", res.Comment)
	}
	assertRan(t, c, "mount -u -t tmpfs -o ro tmpfs /srv/scratch")
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.Contains(ran, "remount") {
			t.Errorf("ran %q", ran)
		}
	}

	c, _ = bsdMountCtx(t, "tmpfs on /srv/scratch (tmpfs, local, read-only)\n")
	res, err = r.States.Call(c, "mount.mounted", value.MapOf(
		"name", "/srv/scratch", "device", "tmpfs", "fstype", "tmpfs", "opts", "ro", "config", path))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("the run after the remount changed something: %s %v", res.Comment, res.Changes)
	}
}
