package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// A real `udevadm info --export-db`, trimmed to two devices.
const udevExportDBSample = `P: /devices/LNXSYSTM:00
M: LNXSYSTM:00
U: acpi
E: DEVPATH=/devices/LNXSYSTM:00
E: SUBSYSTEM=acpi
E: MODALIAS=acpi:LNXSYSTM:

P: /devices/virtual/block/loop0
N: loop0
U: block
S: disk/by-loop-inode/259:2-3416121
S: disk/by-diskseq/11
E: DEVNAME=/dev/loop0
E: DEVTYPE=disk
E: SUBSYSTEM=block
E: ID_FS_TYPE=squashfs
`

func TestUdevUnquoteExport(t *testing.T) {
	for in, want := range map[string]string{
		"'/dev/loop0'": "/dev/loop0",
		"'11'":         "11",
		"unquoted":     "unquoted",
		"":             "",
		"'":            "'",
	} {
		if got := udevUnquoteExport(in); got != want {
			t.Errorf("udevUnquoteExport(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUdevParsesExportDB(t *testing.T) {
	devices := udevParseExportDB(udevExportDBSample)
	if len(devices) != 2 {
		t.Fatalf("parsed %d devices, want 2", len(devices))
	}
	acpi, loop := devices[0], devices[1]
	if acpi.Subsystem != "acpi" || acpi.Devnode != "" {
		t.Errorf("acpi device = %+v", acpi)
	}
	if acpi.Properties["MODALIAS"] != "acpi:LNXSYSTM:" {
		t.Errorf("acpi MODALIAS = %q", acpi.Properties["MODALIAS"])
	}
	if loop.Subsystem != "block" || loop.Devnode != "/dev/loop0" {
		t.Errorf("loop device = %+v", loop)
	}
	if len(loop.Symlinks) != 2 || loop.Symlinks[0] != "/dev/disk/by-loop-inode/259:2-3416121" {
		t.Errorf("loop symlinks = %v", loop.Symlinks)
	}
	if loop.Properties["ID_FS_TYPE"] != "squashfs" {
		t.Errorf("loop ID_FS_TYPE = %q", loop.Properties["ID_FS_TYPE"])
	}
}

func udevCtx(responses map[string]exec.Result) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
		Lookup: func(name string) string { return "/usr/sbin/" + name },
	}
}

func TestUdevInfoParsesExportedProperties(t *testing.T) {
	key := (exec.Command{Argv: []string{"udevadm", "info", "--query=property", "--export", "--name=/dev/loop0"}}).String()
	c := udevCtx(map[string]exec.Result{
		key: {Stdout: "DEVNAME='/dev/loop0'\nDEVTYPE='disk'\nMAJOR='7'\n"},
	})
	out, err := udevInfoFn(c, value.MapOf("device", "/dev/loop0"))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(*value.Map)
	if v, _ := m.GetString("DEVNAME"); v != "/dev/loop0" {
		t.Errorf("DEVNAME = %v (quotes should be stripped)", v)
	}
	if v, _ := m.GetString("MAJOR"); v != "7" {
		t.Errorf("MAJOR = %v", v)
	}
}

func TestUdevInfoRejectsBothOrNeither(t *testing.T) {
	c := udevCtx(nil)
	if _, err := udevInfoFn(c, value.MapOf("device", "/dev/loop0", "syspath", "/sys/class/block/loop0")); err == nil {
		t.Error("giving both device and syspath was accepted")
	}
	if _, err := udevInfoFn(c, value.NewMap(0)); err == nil {
		t.Error("giving neither device nor syspath was accepted")
	}
}

func TestUdevListFiltersBySubsystem(t *testing.T) {
	key := (exec.Command{Argv: []string{"udevadm", "info", "--export-db"}}).String()
	c := udevCtx(map[string]exec.Result{key: {Stdout: udevExportDBSample}})
	out, err := udevListFn(c, value.MapOf("subsystem", "block"))
	if err != nil {
		t.Fatal(err)
	}
	list := out.([]any)
	if len(list) != 1 {
		t.Fatalf("filtered list has %d entries, want 1", len(list))
	}
	if dn, _ := list[0].(*value.Map).GetString("devnode"); dn != "/dev/loop0" {
		t.Errorf("devnode = %v", dn)
	}
}

func TestUdevTriggerBuildsTheMatchingArgv(t *testing.T) {
	c := udevCtx(nil)
	out, err := udevTriggerFn(c, value.MapOf("action", "add", "subsystem", "block", "sysname", "sda*"))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("trigger reported no change: %v", out)
	}
	want := "udevadm trigger --action=add --subsystem-match=block --sysname-match=sda*"
	var ran bool
	for _, cmd := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if cmd == want {
			ran = true
		}
	}
	if !ran {
		t.Errorf("trigger ran %v, want it to include %q", c.Runner.(*exec.RecordingRunner).RanCommands(), want)
	}

	// Test mode runs --dry-run instead of the real thing.
	c2 := udevCtx(nil)
	c2.Test = true
	if _, err := udevTriggerFn(c2, value.MapOf("action", "change")); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range c2.Runner.(*exec.RecordingRunner).RanCommands() {
		if !strings.Contains(cmd, "--dry-run") {
			t.Errorf("a test run executed %q without --dry-run", cmd)
		}
	}
}

func TestUdevSettleRejectsABadTimeout(t *testing.T) {
	c := udevCtx(nil)
	if _, err := udevSettleFn(c, value.MapOf("timeout", int64(0))); err == nil {
		t.Error("a zero timeout was accepted")
	}
	if _, err := udevSettleFn(c, value.MapOf("timeout", int64(-1))); err == nil {
		t.Error("a negative timeout was accepted")
	}
}

func TestUdevRefusesWithoutTheTool(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if err := udevToolPresent(c); err == nil || !strings.Contains(err.Error(), "udevadm") {
		t.Errorf("the refusal does not name udevadm: %v", err)
	}
}

func TestUdevRefusesOnANonLinuxPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this is about the platforms udev is not declared for")
	}
	_, err := New().Exec.Call(&exec.Context{}, "udev.list", value.NewMap(0))
	if err == nil || !strings.Contains(err.Error(), "this node is "+runtime.GOOS) {
		t.Errorf("udev.list did not refuse by platform: %v", err)
	}
}
