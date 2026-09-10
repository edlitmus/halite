package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `mdadm`, driven for real against a RAID array built on loopback
// devices.
//
// # Why this needs devices and a gate
//
// md is a kernel subsystem: `mdadm --create` writes superblocks, asks
// device-mapper for a new /dev/md node, and starts a resync thread.
// None of that can be faked, and none of it is namespaced — a new array
// is visible to the whole machine. So this runs only behind
// `HALITE_SYSTEM_LIVE=1` and as root, on a machine that can be thrown
// away, and it builds its members out of files: three loop devices, an
// array across them, torn down whatever the test did.
//
// # What it establishes
//
// The mutating path — `create`, `fail`, `remove`, `add`, `stop`,
// `save_config` — runs against the real mdadm, and every result is
// checked against a fresh `mdadm --detail` / `--examine` / `/proc/mdstat`
// read rather than the module's own answer. `grow` and `assemble --scan`
// are not exercised here: a reshape takes too long for a test and a scan
// touches every superblock on the host.

func liveMdadmRun(c *exec.Context, argv ...string) error {
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil {
		return fmt.Errorf("%s could not be run: %w", argv[0], err)
	}
	if res.Code != 0 {
		return fmt.Errorf("%s exited %d: %s", argv[0], res.Code, strings.TrimSpace(res.Stderr+res.Stdout))
	}
	return nil
}

type liveMdadmRig struct {
	c       *exec.Context
	r       *Registries
	devices []string
	array   string
}

func liveMdadmSetup(t *testing.T) liveMdadmRig {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("md/RAID is Linux; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this build a RAID array on loop devices")
	}
	if os.Geteuid() != 0 {
		t.Skip("creating an md array needs root")
	}
	c := &exec.Context{}
	for _, tool := range []string{"mdadm", "losetup"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`", tool)
		}
	}
	// md personalities are usually autoloaded by mdadm; nudge them in
	// case the runner kernel is stripped.
	_ = liveMdadmRun(c, "modprobe", "md_mod")

	dir := t.TempDir()
	array := "/dev/md/hal" + strconv.Itoa(os.Getpid())

	// Clear any leftover from a crashed run.
	_ = liveMdadmRun(c, "mdadm", "--stop", array)

	var devices []string
	for i := 0; i < 3; i++ {
		img := filepath.Join(dir, fmt.Sprintf("d%d.img", i))
		f, err := os.Create(img)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(48 << 20); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
		out, err := c.Run(exec.Command{Argv: []string{"losetup", "--find", "--show", img}, IgnoreExitCode: true})
		if err != nil || out.Code != 0 {
			t.Skipf("a loop device could not be attached: %v %s", err, out.Stderr)
		}
		devices = append(devices, strings.TrimSpace(out.Stdout))
	}

	rig := liveMdadmRig{c: c, r: New(), devices: devices, array: array}
	t.Cleanup(func() {
		_ = liveMdadmRun(rig.c, "mdadm", "--stop", rig.array)
		for _, d := range rig.devices {
			_ = liveMdadmRun(rig.c, "mdadm", "--zero-superblock", d)
			if err := liveMdadmRun(rig.c, "losetup", "-d", d); err != nil {
				t.Logf("losetup -d %s left a loop device attached: %v", d, err)
			}
		}
	})
	return rig
}

func TestLiveMdadmBuildsAnArrayARealMdadmReportsBack(t *testing.T) {
	rig := liveMdadmSetup(t)
	r, c := rig.r, rig.c

	call := func(fn string, kv ...any) *value.Map {
		t.Helper()
		args := value.NewMap(len(kv) / 2)
		for i := 0; i+1 < len(kv); i += 2 {
			args.Set(kv[i].(string), kv[i+1])
		}
		out, err := r.Exec.Call(c, fn, args)
		if err != nil {
			t.Fatalf("%s(%v): %v", fn, kv, err)
		}
		m, _ := out.(*value.Map)
		return m
	}
	changed := func(m *value.Map) bool { v, _ := m.GetString("changed"); return v == true }
	detail := func() *value.Map {
		t.Helper()
		out, err := r.Exec.Call(c, "mdadm.detail", value.MapOf("device", rig.array))
		if err != nil {
			t.Fatalf("mdadm.detail: %v", err)
		}
		return out.(*value.Map)
	}

	// create RAID1 with a spare.
	create := call("mdadm.create", "device", rig.array, "level", "1",
		"devices", []any{rig.devices[0], rig.devices[1]}, "spares", []any{rig.devices[2]},
		"metadata", "1.2", "name", "hal"+strconv.Itoa(os.Getpid()))
	if !changed(create) {
		t.Fatalf("create reported no change: %v", create)
	}

	d := detail()
	if lvl, _ := d.GetString("level"); lvl != "raid1" {
		t.Errorf("level = %v", lvl)
	}
	if rd, _ := d.GetString("raid_devices"); rd != int64(2) {
		t.Errorf("raid_devices = %v", rd)
	}
	if sp, _ := d.GetString("spare_devices"); sp != int64(1) {
		t.Errorf("spare_devices = %v", sp)
	}
	mv, _ := d.GetString("members")
	if members, _ := mv.([]any); len(members) != 3 {
		t.Errorf("detail lists %d members, want 3", len(members))
	}

	// list and mdstat see it.
	lv, err := r.Exec.Call(c, "mdadm.list", value.NewMap(0))
	if err != nil {
		t.Fatalf("mdadm.list: %v", err)
	}
	var listed bool
	tag := "hal" + strconv.Itoa(os.Getpid())
	for _, e := range lv.([]any) {
		dev, _ := e.(*value.Map).GetString("device")
		ds, _ := dev.(string)
		if strings.Contains(ds, tag) {
			listed = true
		}
	}
	if !listed {
		t.Errorf("mdadm.list does not show the new array: %v", lv)
	}
	if _, err := r.Exec.Call(c, "mdadm.mdstat", value.NewMap(0)); err != nil {
		t.Errorf("mdadm.mdstat: %v", err)
	}

	// examine a member: its superblock names the same uuid.
	ex, err := r.Exec.Call(c, "mdadm.examine", value.MapOf("device", rig.devices[0]))
	if err != nil {
		t.Fatalf("mdadm.examine: %v", err)
	}
	if has, _ := ex.(*value.Map).GetString("has_superblock"); has != true {
		t.Errorf("examine of a member says it has no superblock: %v", ex)
	}
	exUUID, _ := ex.(*value.Map).GetString("uuid")
	dUUID, _ := d.GetString("uuid")
	if exUUID != dUUID {
		t.Errorf("examine uuid %v != detail uuid %v", exUUID, dUUID)
	}

	// create again: nothing to do.
	if changed(call("mdadm.create", "device", rig.array, "level", "1",
		"devices", []any{rig.devices[0], rig.devices[1]})) {
		t.Error("a second create reported a change")
	}

	// fail, remove, add a member; each checked against a fresh detail.
	call("mdadm.fail", "device", rig.array, "component", rig.devices[0])
	if deg, _ := detail().GetString("degraded"); deg != true {
		t.Errorf("after fail the array is not degraded: %v", detail())
	}
	call("mdadm.remove", "device", rig.array, "component", rig.devices[0])
	if mdadmMemberInDetail(detail(), rig.devices[0]) {
		t.Errorf("%s still in the array after remove", rig.devices[0])
	}
	if changed(call("mdadm.remove", "device", rig.array, "component", rig.devices[0])) {
		t.Error("a second remove reported a change")
	}
	call("mdadm.add", "device", rig.array, "component", rig.devices[0])
	if !mdadmMemberInDetail(detail(), rig.devices[0]) {
		t.Errorf("%s not back in the array after add", rig.devices[0])
	}
	if changed(call("mdadm.add", "device", rig.array, "component", rig.devices[0])) {
		t.Error("a second add reported a change")
	}

	// save_config keeps non-ARRAY lines and lists the array.
	conf := filepath.Join(t.TempDir(), "mdadm.conf")
	if err := os.WriteFile(conf, []byte("MAILADDR root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	call("mdadm.save_config", "path", conf)
	body, _ := os.ReadFile(conf)
	if !strings.Contains(string(body), "MAILADDR root") {
		t.Errorf("save_config dropped the MAILADDR line:\n%s", body)
	}
	if !strings.Contains(string(body), "ARRAY ") {
		t.Errorf("save_config wrote no ARRAY line:\n%s", body)
	}
	if changed(call("mdadm.save_config", "path", conf)) {
		t.Error("a second save_config reported a change")
	}

	// stop, and confirm it is gone.
	if !changed(call("mdadm.stop", "device", rig.array)) {
		t.Error("stop reported no change")
	}
	if _, err := r.Exec.Call(c, "mdadm.detail", value.MapOf("device", rig.array)); err == nil {
		t.Error("mdadm.detail still succeeds after stop")
	}
	if changed(call("mdadm.stop", "device", rig.array)) {
		t.Error("a second stop reported a change")
	}
}

func mdadmMemberInDetail(d *value.Map, dev string) bool {
	mv, _ := d.GetString("members")
	members, _ := mv.([]any)
	for _, m := range members {
		if md, _ := m.(*value.Map).GetString("device"); md == dev {
			return true
		}
	}
	return false
}
