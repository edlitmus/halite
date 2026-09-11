package builtin

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `udev`, read against the real udev database on the machine running
// the tests. No root and no gate: `udevadm info`/`--export-db` need
// none.
func TestLiveUdevReadsRealDevices(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("udev is Linux; this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("udevadm") == "" {
		t.Skip("this host has no udevadm")
	}
	r := New()

	if v, err := r.Exec.Call(c, "udev.version", value.NewMap(0)); err != nil || v == "" {
		t.Errorf("udev.version: %v, %v", v, err)
	}

	rawList, err := r.Exec.Call(c, "udev.list", value.NewMap(0))
	if err != nil {
		t.Fatalf("udev.list: %v", err)
	}
	devices, _ := rawList.([]any)
	if len(devices) == 0 {
		t.Skip("udev.list found nothing to test against")
	}
	// Find any device with a node, and read it back through udev.info.
	var syspath string
	for _, d := range devices {
		m := d.(*value.Map)
		if sp, _ := m.GetString("syspath"); sp != nil {
			syspath = sp.(string)
			break
		}
	}
	if syspath == "" {
		t.Skip("no device in udev.list has a syspath")
	}
	info, err := r.Exec.Call(c, "udev.info", value.MapOf("syspath", syspath))
	if err != nil {
		t.Fatalf("udev.info(%s): %v", syspath, err)
	}
	props := info.(*value.Map)
	if dp, ok := props.GetString("DEVPATH"); !ok || dp == "" {
		t.Errorf("udev.info(%s) has no DEVPATH: %v", syspath, props)
	}

	// A block-scoped listing contains only block devices.
	if blocks, err := r.Exec.Call(c, "udev.list", value.MapOf("subsystem", "block")); err == nil {
		for _, d := range blocks.([]any) {
			if sub, _ := d.(*value.Map).GetString("subsystem"); sub != "block" {
				t.Errorf("a subsystem=block listing returned %v", sub)
			}
		}
	}

	// trigger in test mode predicts without touching anything, and needs
	// no root.
	tc := &exec.Context{Test: true}
	if _, err := r.Exec.Call(tc, "udev.trigger", value.MapOf("subsystem", "block")); err != nil {
		t.Errorf("udev.trigger in test mode: %v", err)
	}

	if _, err := r.Exec.Call(c, "udev.settle", value.MapOf("timeout", int64(3))); err != nil {
		t.Errorf("udev.settle: %v", err)
	}
}

// The control half: a real trigger against a throwaway loop device, and
// a rule reload. Gated because both need root -- `trigger` writes to a
// real /sys/.../uevent file and `reload_rules` talks to the running
// udev daemon.
func TestLiveUdevControlAsRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this trigger a real udev event and reload the daemon's rules")
	}
	if os.Geteuid() != 0 {
		t.Skip("udevadm trigger and control --reload both need root")
	}
	c := &exec.Context{}
	if c.Which("udevadm") == "" || c.Which("losetup") == "" {
		t.Skip("this host has no udevadm or losetup")
	}
	r := New()

	f, err := os.CreateTemp(t.TempDir(), "udevtest*.img")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 20); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	res, err := c.Run(exec.Command{Argv: []string{"losetup", "--find", "--show", f.Name()}, IgnoreExitCode: true})
	if err != nil || res.Code != 0 {
		t.Skipf("a loop device could not be attached: %v %s", err, res.Stderr)
	}
	dev := strings.TrimSpace(res.Stdout)
	sysname := dev[strings.LastIndexByte(dev, '/')+1:]
	t.Cleanup(func() { _, _ = c.Run(exec.Command{Argv: []string{"losetup", "-d", dev}}) })

	out, err := r.Exec.Call(c, "udev.trigger", value.MapOf("subsystem", "block", "sysname", sysname))
	if err != nil {
		t.Fatalf("udev.trigger: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("trigger reported no change: %v", out)
	}
	if _, err := r.Exec.Call(c, "udev.settle", value.MapOf("timeout", int64(5))); err != nil {
		t.Errorf("udev.settle after trigger: %v", err)
	}

	out, err = r.Exec.Call(c, "udev.reload_rules", value.NewMap(0))
	if err != nil {
		t.Fatalf("udev.reload_rules: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("reload_rules reported no change: %v", out)
	}
}
