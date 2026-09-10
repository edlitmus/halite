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

// `lvm`, driven for real against loopback block devices.
//
// # Why this needs devices of its own
//
// LVM writes a label onto a block device, builds a volume group across
// devices, and carves logical volumes out of the group. None of that can
// be faked: `pvs` reads the label off the disk, `vgcreate` writes group
// metadata, `lvcreate` asks the kernel's device-mapper to make a new
// node under /dev. A container shares the host's device-mapper, so a
// test that ran there would be reaching the developer's own machine.
//
// A CI runner is a whole virtual machine with root and a loop driver, so
// it can make its own: two backing files in a temporary directory,
// attached as `/dev/loopN` through `losetup`, thrown away at the end.
// Nothing the machine came with is touched, and the loop devices are
// detached whatever the test did.
//
// # It is gated, and it is meant to be
//
// `HALITE_SYSTEM_LIVE=1` and root, the same gate as `quota`'s loopback
// leg. Attaching a loop device and writing LVM metadata are not things
// to do because somebody typed `go test ./...`, and the loop driver and
// device-mapper are shared with the rest of the machine.
//
// # What it establishes, and what it does not
//
// The mutating path — `pvcreate`, `vgcreate`, `vgextend`, `lvcreate`,
// `lvresize`, `lvremove`, and the states over them — run against the
// real tools, and each result is checked against a fresh `pvs`/`vgs`/
// `lvs` read rather than against the module's own answer. The shrink
// guard is exercised against a volume that really exists.
//
// Not covered: thin pools and thin volumes (the argument vectors are
// pinned in lvm_test.go but nothing has built one here), striping across
// real spindles, and any filesystem on top of a volume — `--resizefs`
// is checked as an argument only.

const lvmLiveVGPrefix = "halitelive"

// liveLVMRig is the pair of loop devices and the group name this run
// uses.
type liveLVMRig struct {
	c       *exec.Context
	r       *Registries
	devices []string
	vg      string
}

func liveLVMRun(c *exec.Context, argv ...string) (string, error) {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return "", fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return "", fmt.Errorf("%s exited %d: %s", argv[0], res.Code,
			strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return res.Stdout, nil
}

func liveLVMSetup(t *testing.T) liveLVMRig {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("LVM and the loop driver are Linux; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this attach loop devices and write LVM metadata")
	}
	if os.Geteuid() != 0 {
		t.Skip("attaching a loop device and writing an LVM label both need root")
	}
	c := &exec.Context{}
	for _, tool := range []string{"losetup", "pvs", "vgs", "lvs", "pvcreate", "vgcreate", "vgextend", "lvcreate", "lvresize", "lvremove", "vgremove", "pvremove"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`; the LVM2 command line is a separate package", tool)
		}
	}

	dir := t.TempDir()
	vg := fmt.Sprintf("%s%d", lvmLiveVGPrefix, os.Getpid())

	// A previous run that crashed between vgcreate and cleanup would leave
	// the group behind, and vgcreate then fails. Clear it first; ignore
	// the error, because "not there" is the normal case.
	_, _ = liveLVMRun(c, "vgremove", "-f", vg)

	var devices []string
	for i := 0; i < 2; i++ {
		img := filepath.Join(dir, fmt.Sprintf("pv%d.img", i))
		f, err := os.Create(img)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(64 << 20); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()

		out, err := liveLVMRun(c, "losetup", "--find", "--show", img)
		if err != nil {
			t.Skipf("a loop device could not be attached: %v", err)
		}
		dev := strings.TrimSpace(out)
		devices = append(devices, dev)
		t.Logf("attached %s -> %s", dev, img)
	}

	rig := liveLVMRig{c: c, r: New(), devices: devices, vg: vg}
	t.Cleanup(func() { liveLVMTeardown(t, rig) })
	return rig
}

// liveLVMTeardown undoes everything, in reverse, and does not stop on the
// first failure: a loop device left attached costs the next job on the
// machine.
func liveLVMTeardown(t *testing.T, rig liveLVMRig) {
	if out, err := liveLVMRun(rig.c, "vgremove", "-f", rig.vg); err != nil {
		if !strings.Contains(err.Error(), "not found") {
			t.Logf("vgremove %s: %v", rig.vg, err)
		}
	} else {
		t.Logf("vgremove %s: %s", rig.vg, strings.TrimSpace(out))
	}
	for _, dev := range rig.devices {
		if _, err := liveLVMRun(rig.c, "pvremove", "-ff", "-y", dev); err != nil &&
			!strings.Contains(err.Error(), "No PV label") && !strings.Contains(err.Error(), "not found") {
			t.Logf("pvremove %s: %v", dev, err)
		}
		if _, err := liveLVMRun(rig.c, "losetup", "-d", dev); err != nil {
			t.Logf("losetup -d %s left a loop device attached: %v", dev, err)
		}
	}
}

func liveLVMCall(t *testing.T, rig liveLVMRig, fn string, kv ...any) *value.Map {
	t.Helper()
	args := value.NewMap(len(kv) / 2)
	for i := 0; i+1 < len(kv); i += 2 {
		v := kv[i+1]
		// A list argument arrives from YAML as []any; the signature's
		// List coercion wraps anything else into a one-item list, so a
		// bare []string would reach the module as a single element that
		// stringifies to "[a b]". Convert it the way real args come in.
		if ss, ok := v.([]string); ok {
			as := make([]any, len(ss))
			for j, s := range ss {
				as[j] = s
			}
			v = as
		}
		args.Set(kv[i].(string), v)
	}
	out, err := rig.r.Exec.Call(rig.c, fn, args)
	if err != nil {
		t.Fatalf("%s(%v): %v", fn, kv, err)
	}
	m, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("%s returned %T, not a map", fn, out)
	}
	return m
}

// A physical volume this module labels is one `pvs` reports, a group it
// builds is one `vgs` reports, and a logical volume it carves and grows
// is one `lvs` reports at the new size — each checked against a fresh
// read of the real tool, not against the module's own answer.
func TestLiveLVMBuildsAGroupAndAVolumeARealLVMReportsBack(t *testing.T) {
	rig := liveLVMSetup(t)

	// pvcreate both loop devices.
	res := liveLVMCall(t, rig, "lvm.pvcreate", "devices", rig.devices)
	if changed, _ := res.GetString("changed"); changed != true {
		t.Fatalf("pvcreate on two fresh devices reported no change: %v", res)
	}
	pvs := liveLVMReport(t, rig, "lvm.pvs")
	for _, dev := range rig.devices {
		if !liveLVMHas(pvs, "pv_name", dev) {
			t.Errorf("pvcreate ran and %s is not in `pvs`", dev)
		}
	}
	// Second pvcreate: nothing to do.
	if again := liveLVMCall(t, rig, "lvm.pvcreate", "devices", rig.devices); func() bool {
		v, _ := again.GetString("changed")
		return v != false
	}() {
		t.Errorf("a second pvcreate reported a change: %v", again)
	}

	// vgcreate from the first device only.
	liveLVMCall(t, rig, "lvm.vgcreate", "name", rig.vg, "devices", []any{rig.devices[0]})
	if !liveLVMHas(liveLVMReport(t, rig, "lvm.vgs"), "vg_name", rig.vg) {
		t.Fatalf("vgcreate ran and %s is not in `vgs`", rig.vg)
	}

	// vgextend with the second device.
	liveLVMCall(t, rig, "lvm.vgextend", "name", rig.vg, "devices", []any{rig.devices[1]})
	vg := liveLVMFind(liveLVMReport(t, rig, "lvm.vgs"), "vg_name", rig.vg)
	if vg == nil {
		t.Fatal("the group vanished after vgextend")
	}
	if pc, _ := vg.GetString("pv_count"); pc != int64(2) {
		t.Errorf("after vgextend the group spans pv_count=%v, want 2", pc)
	}
	// vgextend again: the device is already a member.
	if again := liveLVMCall(t, rig, "lvm.vgextend", "name", rig.vg, "devices", []any{rig.devices[1]}); func() bool {
		v, _ := again.GetString("changed")
		return v != false
	}() {
		t.Errorf("a second vgextend reported a change: %v", again)
	}

	// lvcreate a 16 MiB volume.
	liveLVMCall(t, rig, "lvm.lvcreate", "name", "data", "vgname", rig.vg, "size", "16M")
	lv := liveLVMFind(liveLVMReport(t, rig, "lvm.lvs"), "lv_name", "data")
	if lv == nil {
		t.Fatal("lvcreate ran and `lvs` does not show the volume")
	}
	created, _ := lv.GetString("lv_size")
	if created != int64(16<<20) {
		t.Errorf("the new volume is %v bytes, want %d", created, 16<<20)
	}

	// Grow it to 32 MiB through lvresize.
	liveLVMCall(t, rig, "lvm.lvresize", "name", rig.vg+"/data", "size", "32M")
	lv = liveLVMFind(liveLVMReport(t, rig, "lvm.lvs"), "lv_name", "data")
	if grown, _ := lv.GetString("lv_size"); grown != int64(32<<20) {
		t.Errorf("after the grow the volume is %v bytes, want %d", grown, 32<<20)
	}

	// A shrink is refused before LVM is called.
	_, err := rig.r.Exec.Call(rig.c, "lvm.lvresize", value.MapOf("name", rig.vg+"/data", "size", "8M"))
	if err == nil {
		t.Error("a shrink was accepted without force")
	} else if !strings.Contains(err.Error(), "shrink") {
		t.Errorf("the shrink refusal does not say what is wrong: %v", err)
	}
	// It really did not shrink.
	lv = liveLVMFind(liveLVMReport(t, rig, "lvm.lvs"), "lv_name", "data")
	if still, _ := lv.GetString("lv_size"); still != int64(32<<20) {
		t.Errorf("the refused shrink still changed the volume to %v bytes", still)
	}

	// lv_present at the current size is a satisfied no-op.
	pres, err := rig.r.States.Call(rig.c, "lvm.lv_present",
		value.MapOf("name", "data", "vgname", rig.vg, "size", "32M"))
	if err != nil {
		t.Fatalf("lvm.lv_present: %v", err)
	}
	if !pres.Succeeded() || pres.HasChanges() {
		t.Errorf("lvm.lv_present against a matching volume reported work: %+v", pres)
	}

	// Tear the volume down through the module, and confirm each step
	// against a real read.
	liveLVMCall(t, rig, "lvm.lvremove", "name", rig.vg+"/data")
	if liveLVMFind(liveLVMReport(t, rig, "lvm.lvs"), "lv_name", "data") != nil {
		t.Error("lvremove ran and the volume is still in `lvs`")
	}
	vgAbsent, err := rig.r.States.Call(rig.c, "lvm.vg_absent", value.MapOf("name", rig.vg))
	if err != nil || !vgAbsent.Succeeded() {
		t.Fatalf("lvm.vg_absent: %v / %+v", err, vgAbsent)
	}
	if liveLVMHas(liveLVMReport(t, rig, "lvm.vgs"), "vg_name", rig.vg) {
		t.Error("vg_absent ran and the group is still in `vgs`")
	}
	for _, dev := range rig.devices {
		abs, err := rig.r.States.Call(rig.c, "lvm.pv_absent", value.MapOf("name", dev))
		if err != nil || !abs.Succeeded() {
			t.Fatalf("lvm.pv_absent %s: %v / %+v", dev, err, abs)
		}
	}
	if pvs := liveLVMReport(t, rig, "lvm.pvs"); liveLVMHas(pvs, "pv_name", rig.devices[0]) ||
		liveLVMHas(pvs, "pv_name", rig.devices[1]) {
		t.Error("pv_absent ran and a device is still labelled")
	}
}

// liveLVMReport calls a read function and returns its rows.
func liveLVMReport(t *testing.T, rig liveLVMRig, fn string) []*value.Map {
	t.Helper()
	out, err := rig.r.Exec.Call(rig.c, fn, value.NewMap(0))
	if err != nil {
		t.Fatalf("%s: %v", fn, err)
	}
	list, ok := out.([]any)
	if !ok {
		t.Fatalf("%s returned %T, not a list", fn, out)
	}
	rows := make([]*value.Map, 0, len(list))
	for _, r := range list {
		rows = append(rows, r.(*value.Map))
	}
	return rows
}

func liveLVMFind(rows []*value.Map, key, want string) *value.Map {
	for _, row := range rows {
		if v, _ := row.GetString(key); v == want {
			return row
		}
	}
	return nil
}

func liveLVMHas(rows []*value.Map, key, want string) bool {
	return liveLVMFind(rows, key, want) != nil
}
