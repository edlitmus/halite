package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `modprobe`, read against the real kernel module table on the machine
// running the tests. No root and no gate: /proc/modules and `modinfo`
// are readable by anyone.
func TestLiveModprobeReadsRealModules(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("kernel modules are Linux; this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("modprobe") == "" || c.Which("modinfo") == "" {
		t.Skip("this host has no modprobe/modinfo")
	}
	r := New()

	rawList, err := r.Exec.Call(c, "modprobe.list", value.NewMap(0))
	if err != nil {
		t.Fatalf("modprobe.list: %v", err)
	}
	rows, _ := rawList.([]any)
	if len(rows) == 0 {
		t.Skip("this kernel reports no loaded modules")
	}
	first := rows[0].(*value.Map)
	name, _ := first.GetString("name")

	loaded, err := r.Exec.Call(c, "modprobe.is_loaded", value.MapOf("name", name))
	if err != nil || loaded != true {
		t.Errorf("is_loaded(%v) = %v, %v; it is in the list modprobe.list just returned", name, loaded, err)
	}

	info, err := r.Exec.Call(c, "modprobe.info", value.MapOf("name", name))
	if err != nil {
		t.Fatalf("modprobe.info(%v): %v", name, err)
	}
	if n, _ := info.(*value.Map).GetString("name"); n != name {
		t.Errorf("modinfo's own name field says %v, want %v", n, name)
	}

	if _, err := r.Exec.Call(c, "modprobe.is_denylisted", value.MapOf("name", name)); err != nil {
		t.Errorf("is_denylisted: %v", err)
	}
}

// The mutating half: load and unload a module, and manage its
// persistence files -- gated because it changes the running kernel's
// module set, even though the module chosen (`netdevsim`, the kernel's
// own simulated networking device for testing) creates no network
// interface merely by loading and leaves nothing behind.
func TestLiveModprobeLoadsAndPersists(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this load and unload a real kernel module")
	}
	if os.Geteuid() != 0 {
		t.Skip("loading a kernel module needs root")
	}
	c := &exec.Context{}
	if c.Which("modprobe") == "" {
		t.Skip("this host has no modprobe")
	}
	r := New()
	const testModule = "netdevsim"

	dir := t.TempDir()
	oldLoad, oldProbe := ModulesLoadDir, ModProbeDir
	ModulesLoadDir = filepath.Join(dir, "modules-load.d")
	ModProbeDir = filepath.Join(dir, "modprobe.d")
	if err := os.MkdirAll(ModulesLoadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ModProbeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = r.Exec.Call(c, "modprobe.remove", value.MapOf("name", testModule))
		ModulesLoadDir, ModProbeDir = oldLoad, oldProbe
	})

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

	// The module must not already be loaded, or this proves nothing.
	if loaded, _ := r.Exec.Call(c, "modprobe.is_loaded", value.MapOf("name", testModule)); loaded == true {
		t.Skipf("%s is already loaded on this host", testModule)
	}

	if !changed(call("modprobe.load", "name", testModule)) {
		t.Fatal("load reported no change")
	}
	if loaded, _ := r.Exec.Call(c, "modprobe.is_loaded", value.MapOf("name", testModule)); loaded != true {
		t.Fatalf("%s is not loaded after modprobe.load", testModule)
	}
	if changed(call("modprobe.load", "name", testModule)) {
		t.Error("a second load reported a change")
	}
	if !changed(call("modprobe.remove", "name", testModule)) {
		t.Fatal("remove reported no change")
	}
	if loaded, _ := r.Exec.Call(c, "modprobe.is_loaded", value.MapOf("name", testModule)); loaded != false {
		t.Fatalf("%s is still loaded after modprobe.remove", testModule)
	}
	if changed(call("modprobe.remove", "name", testModule)) {
		t.Error("a second remove reported a change")
	}

	// Persistence, in the redirected directories. The options file's
	// content is just text this test owns the meaning of, so an
	// arbitrary key exercises the same code path a real parameter would.
	if !changed(call("modprobe.persist_load", "name", testModule, "params", value.MapOf("debug", int64(1)))) {
		t.Fatal("persist_load reported no change")
	}
	if changed(call("modprobe.persist_load", "name", testModule, "params", value.MapOf("debug", int64(1)))) {
		t.Error("a second persist_load reported a change")
	}
	if !changed(call("modprobe.persist_remove", "name", testModule)) {
		t.Error("persist_remove reported no change")
	}

	// Denylisting, in the redirected directory.
	if !changed(call("modprobe.denylist", "name", testModule)) {
		t.Fatal("denylist reported no change")
	}
	if dl, _ := r.Exec.Call(c, "modprobe.is_denylisted", value.MapOf("name", testModule)); dl.(*value.Map) == nil {
		t.Fatal("is_denylisted returned nothing")
	} else if v, _ := dl.(*value.Map).GetString("denylisted"); v != true {
		t.Errorf("%s is not reported denylisted after modprobe.denylist", testModule)
	}
	if !changed(call("modprobe.allowlist", "name", testModule)) {
		t.Error("allowlist reported no change")
	}
}
