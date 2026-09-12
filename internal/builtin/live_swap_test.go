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

// `swap`, driven against a real swap device on a memory disk.
//
// # Why a memory disk and not a real one
//
// This host has no swap configured at all -- `swapinfo` prints only its
// header -- which is the fact `swap.list`'s doc comment is built around:
// no swap is a valid answer, not an error. It also means there is no
// existing swap device here to toggle. `mdconfig -a -t swap` makes one
// out of nothing, the same tool `live_quota_ufs_test.go` uses to get a
// UFS filesystem: 64 MiB of swap-backed memory, gone the moment it is
// detached, with nothing on a real disk to clean up if the machine goes
// down mid-test.
//
// Swap space needs no filesystem on it, unlike the UFS leg this borrows
// the memory-disk idea from -- `swapon` takes the raw device directly --
// so this is simpler than that one: attach, swapon, assert, swapoff,
// detach.
//
// # The Linux leg needs no loop device either
//
// `swapon` on Linux accepts a plain file directly, the way it accepts a
// block device; there is no need for `losetup` the way `live_quota_
// loopback_test.go` needs it for a filesystem. A sparse file of the
// right size, `mkswap`'d, is a swap area.
//
// # It is gated, the same way the quota legs are
//
// `HALITE_SYSTEM_LIVE=1` and root: enabling and disabling swap changes
// what the kernel will page to, and attaching a memory disk is not a
// thing to do because somebody typed `go test ./...`.
func liveSwapSetup(t *testing.T) (path string, c *exec.Context) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this attach a memory disk or swap file and turn swap on")
	}
	if os.Geteuid() != 0 {
		t.Skip("enabling and disabling swap both need root")
	}
	c = &exec.Context{}
	for _, tool := range []string{"swapon", "swapoff"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`", tool)
		}
	}

	switch runtime.GOOS {
	case "freebsd":
		return liveSwapFreeBSDDevice(t, c), c
	case "linux":
		return liveSwapLinuxFile(t, c), c
	default:
		t.Skipf("this leg knows freebsd and linux; this is %s", runtime.GOOS)
		return "", nil
	}
}

// liveSwapFreeBSDDevice attaches a swap-backed memory disk and registers
// its detach.
//
// The unit is read back from mdconfig's own output rather than assumed,
// for the reason `liveUFSAttach` gives: picking one would collide with
// whatever this live host already has attached.
func liveSwapFreeBSDDevice(t *testing.T, c *exec.Context) string {
	t.Helper()
	if c.Which("mdconfig") == "" {
		t.Skip("this host has no `mdconfig`")
	}
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
		// Ask first: a test that failed after swapon but before its own
		// swapoff would otherwise make mdconfig -d fail with the device
		// still busy, and the log would blame detachment for a problem
		// that started upstream.
		if liveSwapPathIsActive(t, c, "/dev/"+unit) {
			if _, err := c.Run(exec.Command{Argv: []string{"swapoff", "/dev/" + unit}, IgnoreExitCode: true}); err != nil {
				t.Logf("cleanup: swapoff /dev/%s: %v", unit, err)
			}
		}
		if res, err := c.Run(exec.Command{Argv: []string{"mdconfig", "-d", "-u", unit}, IgnoreExitCode: true}); err != nil || res.Code != 0 {
			t.Logf("cleanup: the memory disk %s could not be detached, which leaks 64 MiB of swap-backed memory: %v (%s)",
				unit, err, strings.TrimSpace(res.Stderr))
		}
	})
	return "/dev/" + unit
}

// liveSwapLinuxFile makes a swap file in a temporary directory and
// registers its removal.
func liveSwapLinuxFile(t *testing.T, c *exec.Context) string {
	t.Helper()
	if c.Which("mkswap") == "" {
		t.Skip("this host has no `mkswap`")
	}
	path := filepath.Join(t.TempDir(), "halite-live-swap.img")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Skipf("the swap file could not be created: %v", err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		f.Close()
		t.Skipf("the swap file could not be sized: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Skipf("the swap file could not be written: %v", err)
	}
	t.Cleanup(func() {
		if liveSwapPathIsActive(t, c, path) {
			if _, err := c.Run(exec.Command{Argv: []string{"swapoff", path}, IgnoreExitCode: true}); err != nil {
				t.Logf("cleanup: swapoff %s: %v", path, err)
			}
		}
		// t.TempDir removes the directory itself; the file is removed
		// explicitly so the log is honest about what this leg cleaned up.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Logf("cleanup: %s could not be removed: %v", path, err)
		}
	})

	res, err := c.Run(exec.Command{Argv: []string{"mkswap", path}, IgnoreExitCode: true})
	if err != nil || res.Code != 0 {
		t.Skipf("mkswap could not prepare %s: %v (exit %d: %s)", path, err, res.Code, strings.TrimSpace(res.Stderr))
	}
	return path
}

// liveSwapPathIsActive asks this module's own idempotency check, so a
// cleanup step does not run a command whose only purpose is to be
// ignored by a kernel that already agrees nothing is there.
func liveSwapPathIsActive(t *testing.T, c *exec.Context, path string) bool {
	t.Helper()
	active, err := activeSwaps(c)
	if err != nil {
		return false
	}
	return swapPathActive(active, path)
}

// TestLiveSwapOnAndOffRoundTripARealDevice runs `swap.on` and `swap.off`
// against a real device or file, through this module's own registry
// entry points rather than by calling the tool directly, and checks the
// device's presence through `mount.swaps` -- a second, independent
// reader of the same kernel state, so the assertion is not "the module
// says it changed something" but "the kernel agrees".
func TestLiveSwapOnAndOffRoundTripARealDevice(t *testing.T) {
	path, c := liveSwapSetup(t)
	r := New()

	swapsAny, err := r.Exec.Call(c, "mount.swaps", value.NewMap(0))
	if err != nil {
		t.Fatalf("mount.swaps before anything happened: %v", err)
	}
	if _, ok := swapsAny.(*value.Map).GetString(path); ok {
		t.Fatalf("%s is already active before this test attached it", path)
	}

	on := value.MapOf("name", path)
	out, err := r.Exec.Call(c, "swap.on", on)
	if err != nil {
		t.Fatalf("swap.on through the real swapon: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("enabling swap on a path with none active reported no change: %v", out)
	}

	swapsAny, err = r.Exec.Call(c, "mount.swaps", value.NewMap(0))
	if err != nil {
		t.Fatalf("mount.swaps after swap.on: %v", err)
	}
	if _, ok := swapsAny.(*value.Map).GetString(path); !ok {
		t.Fatalf("swap.on reported success but mount.swaps does not list %s among %v", path, swapsAny)
	}

	// Idempotence: asking again changes nothing, which is what makes
	// this callable from a tree that runs nightly.
	again, err := r.Exec.Call(c, "swap.on", on)
	if err != nil {
		t.Fatalf("the second swap.on: %v", err)
	}
	if changed, _ := again.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("enabling an already-active swap path reported a change: %v", again)
	}

	off := value.MapOf("name", path)
	out, err = r.Exec.Call(c, "swap.off", off)
	if err != nil {
		t.Fatalf("swap.off through the real swapoff: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("disabling an active swap path reported no change: %v", out)
	}

	swapsAny, err = r.Exec.Call(c, "mount.swaps", value.NewMap(0))
	if err != nil {
		t.Fatalf("mount.swaps after swap.off: %v", err)
	}
	if _, ok := swapsAny.(*value.Map).GetString(path); ok {
		t.Fatalf("swap.off reported success but mount.swaps still lists %s among %v", path, swapsAny)
	}

	again, err = r.Exec.Call(c, "swap.off", off)
	if err != nil {
		t.Fatalf("the second swap.off: %v", err)
	}
	if changed, _ := again.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("disabling an already-inactive swap path reported a change: %v", again)
	}
}

// TestLiveSwapOnRefusesAPriorityOnFreeBSD pins the one platform
// difference `swapOnArgv`'s doc comment claims, against the real tool
// rather than only against the argv table.
func TestLiveSwapOnRefusesAPriorityOnFreeBSD(t *testing.T) {
	if runtime.GOOS != "freebsd" {
		t.Skip("this is a claim about FreeBSD's swapon specifically")
	}
	path, c := liveSwapSetup(t)
	r := New()

	on := value.MapOf("name", path, "priority", int64(5))
	_, err := r.Exec.Call(c, "swap.on", on)
	if err == nil {
		t.Fatal("a priority was accepted on a platform whose swapon has no -p")
	}
	if !strings.Contains(err.Error(), "no -p") && !strings.Contains(err.Error(), "priority") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}

	// The device must still be off: a refused request is not a partial
	// one.
	swapsAny, err := r.Exec.Call(c, "mount.swaps", value.NewMap(0))
	if err != nil {
		t.Fatalf("mount.swaps: %v", err)
	}
	if _, ok := swapsAny.(*value.Map).GetString(path); ok {
		t.Errorf("a refused swap.on still left %s active: %v", path, swapsAny)
	}
}
