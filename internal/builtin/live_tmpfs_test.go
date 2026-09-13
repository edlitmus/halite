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

func liveTmpfsRegistry() *Registries {
	r := &Registries{Exec: exec.NewRegistry()}
	registerTmpfs(r)
	return r
}

// TestLiveTmpfsListsARealTmpfsAlreadyOnTheSystem needs neither root nor
// a gate: every host this was built against already has one tmpfs
// mounted (on FreeBSD, /compat/linux/dev/shm — the Linux compatibility
// layer's shared memory; on Linux, /dev/shm itself), and reading the
// mount table and df both need no privilege. This is what proves
// tmpfsEntries' join against a live system rather than only against
// the captures in tmpfs_test.go.
func TestLiveTmpfsListsARealTmpfsAlreadyOnTheSystem(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" && runtime.GOOS != "darwin" {
		t.Skipf("this is about a real tmpfs mount; not asserting one exists on %s", runtime.GOOS)
	}
	c := &exec.Context{}
	r := liveTmpfsRegistry()

	out, err := r.Exec.Call(c, "tmpfs.list", value.NewMap(0))
	if err != nil {
		t.Fatalf("tmpfs.list: %v", err)
	}
	table := out.(*value.Map)
	if table.Len() == 0 {
		t.Skip("this host has no tmpfs mounted right now")
	}

	// Whatever tmpfs.list found, tmpfs.is_mounted must agree it is a
	// tmpfs, and tmpfs.usage must either answer for it or say plainly
	// that this host's df did not have a matching row — never silently
	// wrong numbers.
	for _, point := range table.StringKeys() {
		mounted, err := r.Exec.Call(c, "tmpfs.is_mounted", value.MapOf("name", point))
		if err != nil || mounted != true {
			t.Errorf("tmpfs.is_mounted(%s) = %v, %v; tmpfs.list just reported this as a tmpfs", point, mounted, err)
		}
		if _, err := r.Exec.Call(c, "tmpfs.usage", value.MapOf("name", point)); err != nil {
			t.Errorf("tmpfs.usage(%s): %v; tmpfs.list found this mount, usage should at worst answer with no figures, not fail", point, err)
		}
	}

	// The running root filesystem is never a tmpfs on any host this
	// runs on, which is a cheap, always-available negative case.
	if mounted, err := r.Exec.Call(c, "tmpfs.is_mounted", value.MapOf("name", "/")); err != nil || mounted != false {
		t.Errorf("tmpfs.is_mounted(/) = %v, %v, want false", mounted, err)
	}
}

// TestLiveTmpfsSeesAFreshlyMountedOne is the half the ungated test
// above cannot prove: that tmpfs.list notices a tmpfs the instant it is
// mounted, not just the ones already on the host when the test suite
// started. Mounting anything needs root.
func TestLiveTmpfsSeesAFreshlyMountedOne(t *testing.T) {
	if runtime.GOOS != "freebsd" && runtime.GOOS != "linux" {
		t.Skipf("this mounts a real tmpfs; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this mount a real tmpfs")
	}
	if os.Geteuid() != 0 {
		t.Skip("mounting a filesystem needs root")
	}
	c := &exec.Context{}
	if c.Which("mount") == "" || c.Which("umount") == "" {
		t.Skip("this host has no mount/umount")
	}

	mount := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mount, 0o755); err != nil {
		t.Fatal(err)
	}
	// FreeBSD's tmpfs(4) and Linux's tmpfs(5) agree on this much: `mount
	// -t tmpfs tmpfs <point>` with no options is enough to get one.
	if res, err := c.Run(exec.Command{Argv: []string{"mount", "-t", "tmpfs", "tmpfs", mount}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
		t.Skipf("a tmpfs could not be mounted at %s: %v (%s)", mount, err, strings.TrimSpace(res.Stderr))
	}
	// Unconditional, so a failure part-way through does not leave a
	// filesystem mounted on a live host -- but it asks first, because
	// the test's own last act is to unmount and assert that the reader
	// notices. Without the check, every *successful* run logged
	//
	//	umount: /tmp/.../mnt: not a file system root directory
	//
	// which is this cleanup finding the work already done. A warning
	// printed by a passing test is worse than no warning: it teaches
	// whoever reads the log to ignore this line, and one day it will be
	// the real thing.
	t.Cleanup(func() {
		if !liveTmpfsStillMounted(t, c, mount) {
			return
		}
		if res, err := c.Run(exec.Command{Argv: []string{"umount", mount}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
			t.Errorf("%s is still mounted and could not be unmounted -- run `umount %s`: %v (%s)",
				mount, mount, err, strings.TrimSpace(res.Stderr))
		}
	})

	r := liveTmpfsRegistry()
	mounted, err := r.Exec.Call(c, "tmpfs.is_mounted", value.MapOf("name", mount))
	if err != nil {
		t.Fatalf("tmpfs.is_mounted(%s): %v", mount, err)
	}
	if mounted != true {
		t.Fatalf("tmpfs.is_mounted(%s) = %v right after mounting one there", mount, mounted)
	}

	usage, err := r.Exec.Call(c, "tmpfs.usage", value.MapOf("name", mount))
	if err != nil {
		t.Fatalf("tmpfs.usage(%s): %v", mount, err)
	}
	if _, ok := usage.(*value.Map).GetString("available"); !ok {
		t.Errorf("tmpfs.usage(%s) has no available field: %v", mount, usage)
	}

	if res, err := c.Run(exec.Command{Argv: []string{"umount", mount}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
		t.Fatalf("%s could not be unmounted: %v (%s)", mount, err, strings.TrimSpace(res.Stderr))
	}
	mounted, err = r.Exec.Call(c, "tmpfs.is_mounted", value.MapOf("name", mount))
	if err != nil {
		t.Fatalf("tmpfs.is_mounted(%s) after umount: %v", mount, err)
	}
	if mounted != false {
		t.Errorf("tmpfs.is_mounted(%s) = %v after umount", mount, mounted)
	}
}

// liveTmpfsStillMounted asks the kernel's mount table, not the module.
//
// Deliberately independent of the code under test: a cleanup that used
// `tmpfs.is_mounted` would skip the unmount whenever that reader was
// wrong, which is exactly when the filesystem would be left behind.
func liveTmpfsStillMounted(t *testing.T, c *exec.Context, point string) bool {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: []string{"mount"}, IgnoreExitCode: true})
	if err != nil {
		// Unknown is treated as mounted, so the unmount is attempted
		// rather than skipped on a guess.
		t.Logf("`mount` could not be read, so the unmount will be attempted anyway: %v", err)
		return true
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.Contains(line, " on "+point+" (") {
			return true
		}
	}
	return false
}
