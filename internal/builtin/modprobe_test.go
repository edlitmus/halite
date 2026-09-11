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

const procModulesSample = `raid1 61440 0 - Live 0x0000000000000000
xt_conntrack 12288 2 xt_tcpudp,ip6table_filter Live 0x0000000000000000
sha256_generic 16384 0 - Live 0x0000000000000000
`

// A real `modinfo netdevsim`, trimmed of its signature blob, with one
// `parm:` line added for coverage -- netdevsim itself declares none, and
// a module that does is exactly the case this parser has to get right:
// `alias` and `parm` both repeat, and the continuation lines the
// signature blob wraps onto (which this fixture also keeps a taste of)
// have no label of their own.
const modinfoSample = `filename:       /lib/modules/6.18.7-generic/kernel/drivers/net/netdevsim/netdevsim.ko.zst
alias:          rtnl-link-netdevsim
description:    Simulated networking device for testing
license:        GPL
depends:        ptp_mock,psample
retpoline:      Y
name:           netdevsim
vermagic:       6.18.7-generic SMP preempt mod_unload modversions
parm:           debug:Debug level (int)
signature:      63:06:77:2A:0C:03:E9:84:6E:FE:E5:8A:74:A3:9F:6F:ED:72:B5:BE:
		A2:DD:80:59:3F:07:48:A6:EC:AB:A4:A2:5D:1B:97:AB:D7:E4:64:69:
`

func TestModprobeReadsModulesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "modules")
	if err := os.WriteFile(path, []byte(procModulesSample), 0o644); err != nil {
		t.Fatal(err)
	}
	old := ProcModulesPath
	ProcModulesPath = path
	t.Cleanup(func() { ProcModulesPath = old })

	mods, err := modprobeReadModules()
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 3 {
		t.Fatalf("read %d modules, want 3", len(mods))
	}
	if mods["raid1"].UsedBy != nil {
		t.Errorf("raid1's `-` dependents should be nil, got %v", mods["raid1"].UsedBy)
	}
	xt := mods["xt_conntrack"]
	if len(xt.UsedBy) != 2 || xt.UsedBy[0] != "xt_tcpudp" || xt.UsedBy[1] != "ip6table_filter" {
		t.Errorf("xt_conntrack used_by = %v", xt.UsedBy)
	}
	if xt.UseCount != 2 || xt.SizeB != 12288 {
		t.Errorf("xt_conntrack size/use_count = %d/%d", xt.SizeB, xt.UseCount)
	}
}

func TestModprobeParseInfo(t *testing.T) {
	m := modprobeParseInfo(modinfoSample)
	if v, _ := m.GetString("name"); v != "netdevsim" {
		t.Errorf("name = %v", v)
	}
	if v, _ := m.GetString("license"); v != "GPL" {
		t.Errorf("license = %v", v)
	}
	dep, ok := m.GetString("depends")
	if !ok {
		t.Fatal("depends is missing")
	}
	if list, _ := dep.([]any); len(list) != 2 || list[0] != "ptp_mock" || list[1] != "psample" {
		t.Errorf("depends = %v, want [ptp_mock psample]", dep)
	}
	alias, _ := m.GetString("alias")
	if list, _ := alias.([]any); len(list) != 1 || list[0] != "rtnl-link-netdevsim" {
		t.Errorf("alias = %v", alias)
	}
	parm, _ := m.GetString("parm")
	if list, _ := parm.([]any); len(list) != 1 || !strings.Contains(list[0].(string), "debug") {
		t.Errorf("parm = %v", parm)
	}
	// The signature's continuation line has no label and must not
	// silently glue onto or replace another field.
	if v, _ := m.GetString("signature"); !strings.HasPrefix(v.(string), "63:06:77") {
		t.Errorf("signature = %v", v)
	}
	if v, ok := m.GetString("A2"); ok {
		t.Errorf("the continuation line was read as its own field: A2=%v", v)
	}
}

func TestModprobeParamArgs(t *testing.T) {
	args := value.MapOf("params", value.MapOf("debug", int64(1), "irq", int64(7)))
	got := modprobeParamArgs(args, "params")
	want := []string{"debug=1", "irq=7"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("params = %v, want %v (sorted by key)", got, want)
	}
	if got := modprobeParamArgs(value.NewMap(0), "params"); got != nil {
		t.Errorf("no params should give nil, got %v", got)
	}
}

// A stock denylist file Debian ships under its own historical name is
// real material here, and modprobeDenylistedIn has to sweep it
// correctly whatever a distribution calls it.
func TestModprobeDenylistedIn(t *testing.T) {
	dir := t.TempDir()
	old := ModProbeDir
	ModProbeDir = dir
	t.Cleanup(func() { ModProbeDir = old })

	os.WriteFile(filepath.Join(dir, "blacklist-firewire.conf"), []byte( // lexicon:allow — Debian's own filename
		"# legacy stack\nblacklist ohci1394\nblacklist sbp2\n#blacklist firewire-ohci\n"), 0o644) // lexicon:allow — modprobe.conf(5) syntax

	if file, ok := modprobeDenylistedIn("ohci1394"); !ok || file != filepath.Join(dir, "blacklist-firewire.conf") { // lexicon:allow — Debian's own filename
		t.Errorf("ohci1394: file=%q ok=%v", file, ok)
	}
	// A commented-out directive does not count.
	if _, ok := modprobeDenylistedIn("firewire-ohci"); ok {
		t.Error("a commented-out denylist line was honoured")
	}
	if _, ok := modprobeDenylistedIn("nonexistent_mod"); ok {
		t.Error("a module nothing denies was reported as denylisted")
	}
}

// ---- behaviour ----

func modprobeCtx(responses map[string]exec.Result) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
		Lookup: func(name string) string { return "/usr/sbin/" + name },
	}
}

func withProcModules(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "modules")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := ProcModulesPath
	ProcModulesPath = path
	t.Cleanup(func() { ProcModulesPath = old })
}

func TestModprobeLoadIsIdempotent(t *testing.T) {
	withProcModules(t, "sample 16384 0 - Live 0x0\n")
	c := modprobeCtx(nil)
	out, err := modprobeLoadFn(c, value.MapOf("name", "sample"))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("loading an already-loaded module reported a change: %v", out)
	}
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(ran, "modprobe sample") {
			t.Errorf("modprobe ran %q for a module already loaded", ran)
		}
	}

	withProcModules(t, "")
	c2 := modprobeCtx(nil)
	out, err = modprobeLoadFn(c2, value.MapOf("name", "sample", "params", value.MapOf("debug", int64(1))))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("loading a missing module reported no change: %v", out)
	}
	var ran bool
	for _, cmd := range c2.Runner.(*exec.RecordingRunner).RanCommands() {
		if cmd == "modprobe sample debug=1" {
			ran = true
		}
	}
	if !ran {
		t.Errorf("load did not run the expected command: %v", c2.Runner.(*exec.RecordingRunner).RanCommands())
	}
}

func TestModprobeRemoveRefusesAModuleInUse(t *testing.T) {
	withProcModules(t, "xt_conntrack 12288 1 xt_tcpudp Live 0x0\n")
	c := modprobeCtx(nil)
	if _, err := modprobeRemoveFn(c, value.MapOf("name", "xt_conntrack")); err == nil {
		t.Fatal("removing a module still in use was accepted")
	} else if !strings.Contains(err.Error(), "xt_tcpudp") {
		t.Errorf("the refusal does not name what depends on it: %v", err)
	}
}

func TestModprobePersistLoadWritesBothFilesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	oldLoad, oldOpt := ModulesLoadDir, ModProbeDir
	ModulesLoadDir, ModProbeDir = filepath.Join(dir, "load"), filepath.Join(dir, "probe")
	os.MkdirAll(ModulesLoadDir, 0o755)
	os.MkdirAll(ModProbeDir, 0o755)
	t.Cleanup(func() { ModulesLoadDir, ModProbeDir = oldLoad, oldOpt })

	c := &exec.Context{}
	out, err := modprobePersistLoadFn(c, value.MapOf("name", "sample", "params", value.MapOf("debug", int64(1))))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Fatalf("first persist_load reported no change: %v", out)
	}
	loadBody, _ := os.ReadFile(filepath.Join(ModulesLoadDir, "sample.conf"))
	if string(loadBody) != "sample\n" {
		t.Errorf("modules-load.d file = %q", loadBody)
	}
	optBody, _ := os.ReadFile(filepath.Join(ModProbeDir, "sample-options.conf"))
	if string(optBody) != "options sample debug=1\n" {
		t.Errorf("modprobe.d options file = %q", optBody)
	}

	out, err = modprobePersistLoadFn(c, value.MapOf("name", "sample", "params", value.MapOf("debug", int64(1))))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("a second persist_load reported a change: %v", out)
	}

	// persist_remove takes the boot-load file only, not the options file.
	if _, err := modprobePersistRemoveFn(c, value.MapOf("name", "sample")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ModulesLoadDir, "sample.conf")); !os.IsNotExist(err) {
		t.Error("persist_remove left the modules-load.d file behind")
	}
}

// allowlist refuses to touch a denylist file it did not write itself,
// but removes its own without complaint.
func TestModprobeAllowlistRefusesAFileItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	old := ModProbeDir
	ModProbeDir = dir
	t.Cleanup(func() { ModProbeDir = old })
	os.WriteFile(filepath.Join(dir, "blacklist-firewire.conf"), []byte("blacklist ohci1394\n"), 0o644) // lexicon:allow — Debian's own filename and directive

	c := &exec.Context{}
	_, err := modprobeAllowlistFn(c, value.MapOf("name", "ohci1394"))
	if err == nil {
		t.Fatal("allowlist removed a directive from a file it did not write")
	}
	if !strings.Contains(err.Error(), "blacklist-firewire.conf") { // lexicon:allow — Debian's own filename
		t.Errorf("the refusal does not name the file: %v", err)
	}
	// The file this module itself wrote is fair game.
	path := modprobeDenylistFilePath("sample")
	os.WriteFile(path, []byte("blacklist sample\n"), 0o644) // lexicon:allow — modprobe.conf(5)'s own directive name
	if _, err := modprobeAllowlistFn(c, value.MapOf("name", "sample")); err != nil {
		t.Fatalf("allowlist refused its own file: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("allowlist did not remove its own file")
	}
}

func TestModprobeRefusesWithoutTheTool(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if err := modprobeToolPresent(c); err == nil || !strings.Contains(err.Error(), "modprobe") {
		t.Errorf("the refusal does not name modprobe: %v", err)
	}
}

func TestModprobeRefusesOnANonLinuxPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this is about the platforms modprobe is not declared for")
	}
	_, err := New().Exec.Call(&exec.Context{}, "modprobe.list", value.NewMap(0))
	if err == nil || !strings.Contains(err.Error(), "this node is "+runtime.GOOS) {
		t.Errorf("modprobe.list did not refuse by platform: %v", err)
	}
}
