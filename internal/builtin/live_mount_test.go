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

// **mount.mounted, driven against a real mount(8) on a throwaway tmpfs.**
//
// The table it writes is a scratch file passed as `config`, so the
// machine's /etc/fstab is never touched, and the line it writes is then
// handed back to mount(8) itself -- `mount -F` on FreeBSD, `mount --fstab`
// on Linux -- because a line this module can read back is not the same
// as one the platform will mount at boot. That difference is issue 321's
// M-44: FreeBSD refuses `defaults`, which is this module's default, and
// `mount -a` skips such a line and exits 0.
//
// Run it, as root:
//
//	sudo HALITE_SYSTEM_LIVE=1 go test -run TestLiveMount -v ./internal/builtin/
func TestLiveMountMountedConvergesAndRemounts(t *testing.T) {
	if runtime.GOOS != "freebsd" && runtime.GOOS != "linux" {
		t.Skipf("this mounts a real tmpfs, and is written for FreeBSD's and Linux's mount; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this mount a real tmpfs")
	}
	if os.Geteuid() != 0 {
		t.Skip("mounting a filesystem needs root")
	}
	c := realCtx(t)
	dir := t.TempDir()
	point := filepath.Join(dir, "mnt")
	fstab := filepath.Join(dir, "fstab")
	t.Cleanup(func() {
		for liveMountOpts(t, c, point) != "" {
			if res, err := c.Run(exec.Command{Argv: []string{"umount", point}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
				t.Errorf("%s is still mounted -- run `umount %s`: %v (%s)", point, point, err, strings.TrimSpace(res.Stderr))
				return
			}
		}
	})
	r := New()
	apply := func(extra ...any) (bool, string) {
		t.Helper()
		args := value.MapOf("name", point, "device", "tmpfs", "fstype", "tmpfs", "mkmnt", true, "config", fstab)
		for i := 0; i+1 < len(extra); i += 2 {
			args.Set(extra[i].(string), extra[i+1])
		}
		res, err := r.States.Call(c, "mount.mounted", args)
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || !*res.Result {
			t.Fatalf("mount.mounted failed: %s", res.Comment)
		}
		return res.HasChanges(), res.Comment
	}

	// **No `opts`**, which is how most trees write it, so the default is
	// what is under test.
	if changed, said := apply(); !changed {
		t.Fatalf("the first run changed nothing: %s", said)
	}
	if opts := liveMountOpts(t, c, point); opts == "" {
		t.Fatalf("mount.mounted succeeded and nothing is mounted at %s", point)
	}
	if changed, said := apply(); changed {
		t.Errorf("the second run changed something again: %s", said)
	}
	raw, _ := os.ReadFile(fstab)
	t.Logf("the table it wrote: %q", strings.TrimSpace(string(raw)))

	// The line has to be one the platform mounts.
	if res, err := c.Run(exec.Command{Argv: []string{"umount", point}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
		t.Fatalf("umount: %v (%s)", err, res.Stderr)
	}
	argv := []string{"mount", "--fstab", fstab, point}
	if runtime.GOOS == "freebsd" {
		argv = []string{"mount", "-F", fstab, point}
	}
	if res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true}); err != nil || res.Code != 0 {
		t.Errorf("%q refused the line mount.mounted wrote: %v (%s)", argv, err, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	if liveMountOpts(t, c, point) == "" {
		t.Fatalf("%q did not mount %s from the table mount.mounted wrote", argv, point)
	}

	// **A remount**: `ro` contradicts the running `rw`, which is the
	// remount branch, and it is `-u` on FreeBSD and `-o remount` on Linux.
	if changed, said := apply("opts", "ro"); !changed {
		t.Fatalf("declaring ro on a rw mount changed nothing: %s", said)
	}
	if opts := liveMountOpts(t, c, point); !liveHasOpt(opts, "ro") {
		t.Errorf("after the remount the kernel says %q, without ro", opts)
	}
	if changed, said := apply("opts", "ro"); changed {
		t.Errorf("the second ro run changed something again: %s", said)
	}
}

// liveMountOpts is the running option list at point, from the platform's
// own table rather than this module's reader: `mount -p` on FreeBSD,
// /proc/self/mounts on Linux. Empty when nothing is mounted there. The
// last match wins, since a later mount over the same point hides an
// earlier one.
func liveMountOpts(t *testing.T, c *exec.Context, point string) string {
	t.Helper()
	var table string
	if runtime.GOOS == "freebsd" {
		res, err := c.Run(exec.Command{Argv: []string{"mount", "-p"}})
		if err != nil {
			t.Fatalf("mount -p: %v", err)
		}
		table = res.Stdout
	} else {
		raw, err := os.ReadFile("/proc/self/mounts")
		if err != nil {
			t.Fatal(err)
		}
		table = string(raw)
	}
	opts := ""
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && f[1] == point {
			opts = f[3]
		}
	}
	return opts
}

func liveHasOpt(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}
