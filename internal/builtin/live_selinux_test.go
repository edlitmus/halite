package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// `selinux`, and file's SELinux pair, driven against the real policy on
// the machine running the tests.
//
// # What it touches, and how it puts it back
//
//   - One boolean, httpd_can_network_connect, chosen because nothing on a
//     lab host listens for httpd. Runtime and, in one test, persistent.
//   - File-context rules under /srv/halite-selinux-test, and that
//     directory.
//   - Port rules for tcp/18990-18999 and udp/18999, which no policy on
//     either lab host assigns, and a local override of tcp/8080 -- the
//     one rule here over something the policy already has, and the case
//     the module's port lookup exists for.
//   - The running mode, Enforcing to Permissive and back, inside one
//     test whose cleanup writes Enforcing again whatever happened.
//
// selinuxGuardHost snapshots the local customisations (`semanage
// boolean|fcontext|port -l -C`), the boolean, and the mode before
// anything changes, and fails the test if any of them differs after the
// cleanups ran. setsebool -P leaves a local record behind even when it
// sets a boolean back to its default (captured on both hosts), and
// semanage has no way to delete one record, so the guard removes it
// through the same seobject call semanage itself is built on -- only if
// it was not there before.
//
// /etc/selinux/config is never written, by the module or by this file.
//
// Gated on HALITE_SYSTEM_LIVE=1, root, Linux, and a running SELinux;
// on a node without one (Debian 13), TestLiveSelinuxRefusesWhereThereIsNone
// runs instead.

const (
	liveSelinuxBoolean = "httpd_can_network_connect"
	liveSelinuxDir     = "/srv/halite-selinux-test"
	liveSelinuxSpec    = liveSelinuxDir + "(/.*)?"
)

func selinuxLiveGate(t *testing.T) *exec.Context {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("SELinux is Linux; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this changes SELinux policy and mode")
	}
	if os.Geteuid() != 0 {
		t.Skip("semanage and setsebool need root, and this is not root")
	}
	if ok, why := selinuxEnabled(); !ok {
		t.Skipf("%s; TestLiveSelinuxRefusesWhereThereIsNone is the test for this node", why)
	}
	c := realCtx(t)
	for _, tool := range []string{"semanage", "setsebool", "restorecon", "semodule", "getenforce", "getsebool", "chcon"} {
		if c.Which(tool) == "" {
			t.Skipf("%s is not installed here", tool)
		}
	}
	return c
}

func liveSelinuxRun(t *testing.T, c *exec.Context, argv ...string) string {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil || res.Code != 0 {
		t.Fatalf("%v: %v %d %q", argv, err, res.Code, res.Stderr)
	}
	return res.Stdout
}

type selinuxSnapshot struct {
	mode, boolean, booleansC, fcontextC, portC string
}

func selinuxTakeSnapshot(t *testing.T, c *exec.Context) selinuxSnapshot {
	t.Helper()
	return selinuxSnapshot{
		mode:      strings.TrimSpace(liveSelinuxRun(t, c, "getenforce")),
		boolean:   selinuxBooleanRow(t, c),
		booleansC: liveSelinuxRun(t, c, "semanage", "boolean", "-l", "-C"),
		fcontextC: liveSelinuxRun(t, c, "semanage", "fcontext", "-l", "-C"),
		portC:     liveSelinuxRun(t, c, "semanage", "port", "-l", "-C"),
	}
}

func selinuxBooleanRow(t *testing.T, c *exec.Context) string {
	t.Helper()
	for _, line := range strings.Split(liveSelinuxRun(t, c, "semanage", "boolean", "-l"), "\n") {
		if strings.HasPrefix(line, liveSelinuxBoolean+" ") {
			return strings.Join(strings.Fields(line), " ")
		}
	}
	t.Fatalf("%s is not in this policy", liveSelinuxBoolean)
	return ""
}

// selinuxGuardHost is the precondition and the postcondition. It
// refuses a host where this suite's own names are already in use, and
// registers, first so it runs last, the check that everything is back.
func selinuxGuardHost(t *testing.T, c *exec.Context) {
	t.Helper()
	before := selinuxTakeSnapshot(t, c)
	if before.mode != "Enforcing" {
		t.Skipf("SELinux is %s here; this suite starts from Enforcing so that putting it back is unambiguous", before.mode)
	}
	if strings.Contains(before.fcontextC, "halite") || strings.Contains(before.portC, "1899") {
		t.Skipf("this host already has local rules this suite would manage:\n%s%s", before.fcontextC, before.portC)
	}
	if _, err := os.Lstat(liveSelinuxDir); err == nil {
		t.Skipf("%s already exists, and this suite removes it", liveSelinuxDir)
	}
	hadRecord := strings.Contains(before.booleansC, liveSelinuxBoolean+" ")
	t.Cleanup(func() {
		after := selinuxTakeSnapshot(t, c)
		if !hadRecord && strings.Contains(after.booleansC, liveSelinuxBoolean+" ") {
			liveSelinuxRun(t, c, "python3", "-c",
				"import seobject; seobject.booleanRecords().delete('"+liveSelinuxBoolean+"')")
			after = selinuxTakeSnapshot(t, c)
		}
		if after != before {
			t.Errorf("the host was not put back:\nbefore: %+v\nafter:  %+v", before, after)
		}
	})
}

func liveSelinuxExec(t *testing.T, c *exec.Context, test bool, name string, args *value.Map) any {
	t.Helper()
	cc := *c
	cc.Test = test
	out, err := New().Exec.Call(&cc, name, args)
	if err != nil {
		t.Fatalf("%s(%v) test=%v: %v", name, args, test, err)
	}
	return out
}

func liveSelinuxState(t *testing.T, c *exec.Context, test bool, name string, args *value.Map) states.Result {
	t.Helper()
	cc := *c
	cc.Test = test
	res, err := New().States.Call(&cc, name, args)
	if err != nil {
		t.Fatalf("%s test=%v: %v", name, test, err)
	}
	return res
}

func selinuxChanged(t *testing.T, out any) bool {
	t.Helper()
	m, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("result = %#v, want a map", out)
	}
	changed, _ := m.Get("changed")
	return changed == true
}

func liveGetsebool(t *testing.T, c *exec.Context) string {
	t.Helper()
	out := strings.TrimSpace(liveSelinuxRun(t, c, "getsebool", liveSelinuxBoolean))
	return out[strings.LastIndex(out, " ")+1:]
}

// The reads, each against the other tool that answers the same question.
func TestLiveSelinuxReadsAgreeWithTheTools(t *testing.T) {
	c := selinuxLiveGate(t)

	if got, want := liveSelinuxExec(t, c, false, "selinux.getenforce", value.NewMap(0)), strings.TrimSpace(liveSelinuxRun(t, c, "getenforce")); got != want {
		t.Errorf("getenforce = %v, the tool says %s", got, want)
	}
	var fromConfig string
	for _, line := range strings.Split(liveSelinuxRun(t, c, "sestatus"), "\n") {
		if strings.HasPrefix(line, "Mode from config file:") {
			fromConfig = strings.TrimSpace(strings.TrimPrefix(line, "Mode from config file:"))
		}
	}
	if got := liveSelinuxExec(t, c, false, "selinux.getconfig", value.NewMap(0)); !strings.EqualFold(got.(string), fromConfig) {
		t.Errorf("getconfig = %v, sestatus says %q", got, fromConfig)
	}

	all := liveSelinuxExec(t, c, false, "selinux.list_sebool", value.NewMap(0)).(*value.Map)
	tool := strings.Split(strings.TrimSpace(liveSelinuxRun(t, c, "getsebool", "-a")), "\n")
	if all.Len() != len(tool) {
		t.Errorf("list_sebool has %d booleans and getsebool -a %d", all.Len(), len(tool))
	}
	for _, line := range tool {
		name, state, _ := strings.Cut(line, " --> ")
		entry, ok := all.Get(name)
		if !ok {
			t.Errorf("%s is in getsebool -a and not in list_sebool", name)
			continue
		}
		if got, _ := entry.(*value.Map).Get("State"); got != state {
			t.Errorf("%s: list_sebool State = %v, getsebool says %s", name, got, state)
		}
	}
	one := liveSelinuxExec(t, c, false, "selinux.getsebool", value.MapOf("boolean", liveSelinuxBoolean)).(*value.Map)
	if got, _ := one.Get("State"); got != liveGetsebool(t, c) {
		t.Errorf("getsebool %s State = %v", liveSelinuxBoolean, got)
	}
	if none := liveSelinuxExec(t, c, false, "selinux.getsebool", value.MapOf("boolean", "halite_no_such_boolean")).(*value.Map); none.Len() != 0 {
		t.Errorf("a boolean that does not exist = %v, want an empty mapping", none)
	}

	// semodule -l lists the enabled modules at the priority in force.
	mods := liveSelinuxExec(t, c, false, "selinux.list_semod", value.NewMap(0)).(*value.Map)
	var enabled []string
	for _, e := range mods.Entries() {
		if on, _ := e.Val.(*value.Map).Get("Enabled"); on == true {
			enabled = append(enabled, e.Key.(string))
		}
	}
	listed := strings.Fields(liveSelinuxRun(t, c, "semodule", "-l"))
	sort.Strings(enabled)
	sort.Strings(listed)
	if strings.Join(enabled, " ") != strings.Join(listed, " ") {
		t.Errorf("list_semod's enabled modules (%d) are not semodule -l's (%d)", len(enabled), len(listed))
	}

	rule := liveSelinuxExec(t, c, false, "selinux.fcontext_get_policy", value.MapOf("name", "/var/www(/.*)?"))
	if m, ok := rule.(*value.Map); !ok {
		t.Errorf("/var/www(/.*)? = %#v", rule)
	} else if typ, _ := m.Get("sel_type"); typ != "httpd_sys_content_t" {
		t.Errorf("/var/www(/.*)? is %v; matchpathcon says %s", typ, liveSelinuxRun(t, c, "matchpathcon", "/var/www"))
	}
	ssh := liveSelinuxExec(t, c, false, "selinux.port_get_policy", value.MapOf("name", "tcp/22")).(*value.Map)
	if typ, _ := ssh.Get("sel_type"); typ != "ssh_port_t" {
		t.Errorf("tcp/22 = %v", ssh)
	}

	ctx := liveSelinuxExec(t, c, false, "file.get_selinux_context", value.MapOf("path", "/etc/passwd"))
	lsZ := strings.Fields(liveSelinuxRun(t, c, "ls", "-Zd", "/etc/passwd"))[0]
	if ctx != lsZ {
		t.Errorf("file.get_selinux_context /etc/passwd = %v, ls -Z says %s", ctx, lsZ)
	}
}

func TestLiveSelinuxBooleanRuntimeAndPersistent(t *testing.T) {
	c := selinuxLiveGate(t)
	selinuxGuardHost(t, c)
	if liveGetsebool(t, c) != "off" {
		t.Skipf("%s is on here; this test starts from off", liveSelinuxBoolean)
	}
	t.Cleanup(func() {
		_, _ = c.Run(exec.Command{Argv: []string{"setsebool", "-P", liveSelinuxBoolean, "off"}, IgnoreExitCode: true})
	})
	on := value.MapOf("boolean", liveSelinuxBoolean, "value", "on")

	if !selinuxChanged(t, liveSelinuxExec(t, c, true, "selinux.setsebool", on)) {
		t.Error("test mode predicted nothing")
	}
	if got := liveGetsebool(t, c); got != "off" {
		t.Fatalf("test mode set it %s", got)
	}
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.setsebool", on)) {
		t.Error("setsebool reported no change")
	}
	if got := liveGetsebool(t, c); got != "on" {
		t.Errorf("getsebool says %s after setsebool on", got)
	}
	if row := selinuxBooleanRow(t, c); !strings.Contains(row, "(on , off)") {
		t.Errorf("a runtime-only change touched the policy store: %s", row)
	}
	if selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.setsebool", on)) {
		t.Error("a second setsebool reported a change")
	}

	// persist: the Default column too, which only -P writes. A YAML
	// `on` arrives as true.
	persisted := value.MapOf("boolean", liveSelinuxBoolean, "value", true, "persist", true)
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.setsebool", persisted)) {
		t.Error("persist reported no change while Default was off")
	}
	if row := selinuxBooleanRow(t, c); !strings.Contains(row, "(on , on)") {
		t.Errorf("after persist: %s", row)
	}

	// setsebools, back to off in the store and the running policy.
	off := value.MapOf("pairs", value.MapOf(liveSelinuxBoolean, "off"), "persist", true)
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.setsebools", off)) {
		t.Error("setsebools reported no change")
	}
	if row := selinuxBooleanRow(t, c); !strings.Contains(row, "(off , off)") {
		t.Errorf("after setsebools off: %s", row)
	}
	if _, err := New().Exec.Call(c, "selinux.setsebools", value.MapOf("pairs",
		value.MapOf(liveSelinuxBoolean, "on", "halite_no_such_boolean", "on"))); err == nil {
		t.Error("setsebools accepted a boolean the policy does not have")
	}
	if got := liveGetsebool(t, c); got != "off" {
		t.Errorf("a refused setsebools still set %s %s", liveSelinuxBoolean, got)
	}

	// The state, runtime: predict, apply, converge.
	args := value.MapOf("name", liveSelinuxBoolean, "value", "on")
	if res := liveSelinuxState(t, c, true, "selinux.boolean", args); res.Result != nil || !res.HasChanges() {
		t.Errorf("test mode: %+v", res)
	}
	if res := liveSelinuxState(t, c, false, "selinux.boolean", args); res.Failed() || !res.HasChanges() {
		t.Errorf("apply: %s", res.Comment)
	}
	if res := liveSelinuxState(t, c, false, "selinux.boolean", args); res.Failed() || res.HasChanges() {
		t.Errorf("second apply: %s %v", res.Comment, res.Changes)
	}
	// persist with the running value already right: only Default moves.
	args.Set("persist", true)
	res := liveSelinuxState(t, c, false, "selinux.boolean", args)
	if res.Failed() || !res.HasChanges() {
		t.Errorf("persist apply: %s", res.Comment)
	}
	if ch, _ := res.Changes.Get(liveSelinuxBoolean); ch == nil || ch.(*value.Map).Has("State") {
		t.Errorf("persist apply changed the running value too: %v", res.Changes)
	}
	if res := liveSelinuxState(t, c, false, "selinux.boolean", args); res.HasChanges() {
		t.Errorf("second persist apply: %v", res.Changes)
	}
	liveSelinuxExec(t, c, false, "selinux.setsebool", value.MapOf("boolean", liveSelinuxBoolean, "value", "off", "persist", true))
}

func TestLiveSelinuxRunningModeAndItsState(t *testing.T) {
	c := selinuxLiveGate(t)
	selinuxGuardHost(t, c)
	if cfg, _ := selinuxGetconfig(); cfg != "Enforcing" {
		t.Skipf("%s says %q; this test needs enforcing there", SELinuxConfigPath, cfg)
	}
	configBefore, err := os.ReadFile(SELinuxConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	// Registered after the guard, so it runs before it.
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(SELinuxFSPath, "enforce"), []byte("1"), 0o644)
		if after, _ := os.ReadFile(SELinuxConfigPath); string(after) != string(configBefore) {
			t.Errorf("%s changed", SELinuxConfigPath)
		}
	})
	tool := func() string { return strings.TrimSpace(liveSelinuxRun(t, c, "getenforce")) }

	permissive := value.MapOf("mode", "Permissive")
	if !selinuxChanged(t, liveSelinuxExec(t, c, true, "selinux.setenforce", permissive)) {
		t.Error("test mode predicted nothing")
	}
	if got := tool(); got != "Enforcing" {
		t.Fatalf("test mode left the node %s", got)
	}
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.setenforce", permissive)) {
		t.Error("setenforce reported no change")
	}
	if got := tool(); got != "Permissive" {
		t.Errorf("getenforce says %s after setenforce Permissive", got)
	}
	if selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.setenforce", value.MapOf("mode", 0))) {
		t.Error("setenforce 0 on a permissive node reported a change")
	}
	if _, err := New().Exec.Call(c, "selinux.setenforce", value.MapOf("mode", "disabled")); err == nil {
		t.Error("setenforce disabled was accepted")
	}

	// The state refuses Permissive while the config says enforcing, and
	// changes nothing doing it.
	if res := liveSelinuxState(t, c, false, "selinux.mode", value.MapOf("name", "permissive")); !res.Failed() {
		t.Errorf("selinux.mode permissive against an enforcing config: %s", res.Comment)
	}
	enforcing := value.MapOf("name", "Enforcing")
	if res := liveSelinuxState(t, c, true, "selinux.mode", enforcing); res.Result != nil || !res.HasChanges() {
		t.Errorf("test mode: %+v", res)
	}
	if got := tool(); got != "Permissive" {
		t.Fatalf("test mode set the node %s", got)
	}
	if res := liveSelinuxState(t, c, false, "selinux.mode", enforcing); res.Failed() || !res.HasChanges() {
		t.Errorf("apply: %s", res.Comment)
	}
	if got := tool(); got != "Enforcing" {
		t.Errorf("getenforce says %s after selinux.mode enforcing", got)
	}
	if res := liveSelinuxState(t, c, false, "selinux.mode", enforcing); res.Failed() || res.HasChanges() {
		t.Errorf("second apply: %s", res.Comment)
	}
}

func selinuxLocalFcontexts(t *testing.T, c *exec.Context) string {
	t.Helper()
	return liveSelinuxRun(t, c, "semanage", "fcontext", "-l", "-C")
}

func selinuxLocalHas(listing, spec, words, typ string) bool {
	for _, r := range parseSemanageFcontexts(listing) {
		if r.Spec == spec && r.Filetype == words && (typ == "" || r.Type == typ) {
			return true
		}
	}
	return false
}

func TestLiveSelinuxFcontextRulesAndRelabelling(t *testing.T) {
	c := selinuxLiveGate(t)
	selinuxGuardHost(t, c)
	t.Cleanup(func() {
		for _, argv := range [][]string{
			{"semanage", "fcontext", "-d", liveSelinuxSpec},
			{"semanage", "fcontext", "-d", "-f", "d", liveSelinuxDir + "-dir"},
			{"semanage", "fcontext", "-d", liveSelinuxDir + "/persisted"},
		} {
			_, _ = c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		}
		_ = os.RemoveAll(liveSelinuxDir)
	})

	add := value.MapOf("name", liveSelinuxSpec, "sel_type", "httpd_sys_content_t")
	if !selinuxChanged(t, liveSelinuxExec(t, c, true, "selinux.fcontext_add_policy", add)) {
		t.Error("test mode predicted nothing")
	}
	if selinuxLocalHas(selinuxLocalFcontexts(t, c), liveSelinuxSpec, "all files", "") {
		t.Fatal("test mode added the rule")
	}
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.fcontext_add_policy", add)) {
		t.Error("add reported no change")
	}
	if !selinuxLocalHas(selinuxLocalFcontexts(t, c), liveSelinuxSpec, "all files", "httpd_sys_content_t") {
		t.Errorf("semanage -C does not list the rule:\n%s", selinuxLocalFcontexts(t, c))
	}
	if selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.fcontext_add_policy", add)) {
		t.Error("a second add reported a change")
	}
	// A different type for the same rule is the -m path.
	add.Set("sel_type", "public_content_t")
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.fcontext_add_policy", add)) {
		t.Error("changing the type reported no change")
	}
	if !selinuxLocalHas(selinuxLocalFcontexts(t, c), liveSelinuxSpec, "all files", "public_content_t") {
		t.Errorf("the type did not change:\n%s", selinuxLocalFcontexts(t, c))
	}
	got := liveSelinuxExec(t, c, false, "selinux.fcontext_get_policy", value.MapOf("name", liveSelinuxSpec)).(*value.Map)
	if typ, _ := got.Get("sel_type"); typ != "public_content_t" {
		t.Errorf("fcontext_get_policy = %v", got)
	}

	// A directory-only rule, which semanage keeps apart from the
	// all-files one and deletes only with -f d.
	dirRule := value.MapOf("name", liveSelinuxDir+"-dir", "filetype", "d", "sel_type", "public_content_t")
	liveSelinuxExec(t, c, false, "selinux.fcontext_add_policy", dirRule)
	if !selinuxLocalHas(selinuxLocalFcontexts(t, c), liveSelinuxDir+"-dir", "directory", "public_content_t") {
		t.Error("the directory rule is not listed as one")
	}
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.fcontext_delete_policy", value.MapOf("name", liveSelinuxDir+"-dir", "filetype", "d"))) {
		t.Error("deleting the directory rule reported no change")
	}
	if selinuxLocalHas(selinuxLocalFcontexts(t, c), liveSelinuxDir+"-dir", "directory", "") {
		t.Error("the directory rule is still there")
	}

	// Refusals before semanage is asked.
	for _, bad := range []string{"relative/path", liveSelinuxDir + "/"} {
		if _, err := New().Exec.Call(c, "selinux.fcontext_add_policy", value.MapOf("name", bad, "sel_type", "public_content_t")); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := New().Exec.Call(c, "selinux.fcontext_delete_policy", value.MapOf("name", "/vicepb")); err == nil ||
		!strings.Contains(err.Error(), "defined in the policy") {
		t.Errorf("deleting the policy's own /vicepb: %v", err)
	}

	// Relabelling, through files the rule now covers.
	if err := os.MkdirAll(filepath.Join(liveSelinuxDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveSelinuxDir, "sub", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(liveSelinuxDir, "sub", "file")
	statType := func(p string) string {
		ctx := strings.TrimSpace(liveSelinuxRun(t, c, "stat", "-c", "%C", p))
		_, _, typ, _, _ := selinuxContextParts(ctx)
		return typ
	}
	pending := liveSelinuxExec(t, c, false, "selinux.fcontext_policy_is_applied", value.MapOf("name", liveSelinuxDir, "recursive", true)).(*value.Map)
	if pending.Len() != 3 {
		t.Errorf("fcontext_policy_is_applied = %v, want the directory, sub and file", pending)
	}
	apply := value.MapOf("name", liveSelinuxDir, "recursive", true)
	if !selinuxChanged(t, liveSelinuxExec(t, c, true, "selinux.fcontext_apply_policy", apply)) {
		t.Error("test mode predicted nothing")
	}
	if got := statType(file); got == "public_content_t" {
		t.Fatal("test mode relabelled the file")
	}
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.fcontext_apply_policy", apply)) {
		t.Error("apply reported no change")
	}
	if got := statType(file); got != "public_content_t" {
		t.Errorf("after apply the file is %s", got)
	}
	if again := liveSelinuxExec(t, c, false, "selinux.fcontext_policy_is_applied", apply).(*value.Map); again.Len() != 0 {
		t.Errorf("after apply restorecon -n still wants %v", again)
	}

	// file.set_selinux_context, and the -F pair: a user-only change is
	// something restorecon -F resets, so is_applied must see it.
	set := value.MapOf("path", file, "user", "user_u")
	if !selinuxChanged(t, liveSelinuxExec(t, c, true, "file.set_selinux_context", set)) {
		t.Error("test mode predicted nothing")
	}
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "file.set_selinux_context", set)) {
		t.Error("set_selinux_context reported no change")
	}
	if ctx := liveSelinuxExec(t, c, false, "file.get_selinux_context", value.MapOf("path", file)).(string); !strings.HasPrefix(ctx, "user_u:") {
		t.Errorf("after chcon -u the context is %s", ctx)
	}
	if selinuxChanged(t, liveSelinuxExec(t, c, false, "file.set_selinux_context", set)) {
		t.Error("a second set_selinux_context reported a change")
	}
	if pending := liveSelinuxExec(t, c, false, "selinux.fcontext_policy_is_applied", value.MapOf("name", file)).(*value.Map); pending.Len() != 1 {
		t.Errorf("a user-only change was not seen by is_applied: %v", pending)
	}
	link := filepath.Join(liveSelinuxDir, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := New().Exec.Call(c, "file.set_selinux_context", value.MapOf("path", link, "type", "public_content_t")); err == nil {
		t.Error("a symlink was accepted")
	}

	// persist: the rule and the label.
	persisted := filepath.Join(liveSelinuxDir, "persisted")
	if err := os.WriteFile(persisted, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	liveSelinuxExec(t, c, false, "file.set_selinux_context", value.MapOf("path", persisted, "type", "httpd_sys_content_t", "persist", true))
	if !selinuxLocalHas(selinuxLocalFcontexts(t, c), persisted, "all files", "httpd_sys_content_t") {
		t.Errorf("persist added no rule:\n%s", selinuxLocalFcontexts(t, c))
	}
	if got := statType(persisted); got != "httpd_sys_content_t" {
		t.Errorf("persisted file is %s", got)
	}
	liveSelinuxRun(t, c, "restorecon", "-F", persisted)
	if got := statType(persisted); got != "httpd_sys_content_t" {
		t.Errorf("a relabel undid the persisted type: %s", got)
	}
	liveSelinuxExec(t, c, false, "selinux.fcontext_delete_policy", value.MapOf("name", persisted))

	// Delete the main rule.
	del := value.MapOf("name", liveSelinuxSpec)
	if !selinuxChanged(t, liveSelinuxExec(t, c, true, "selinux.fcontext_delete_policy", del)) {
		t.Error("test mode predicted nothing")
	}
	if !selinuxLocalHas(selinuxLocalFcontexts(t, c), liveSelinuxSpec, "all files", "") {
		t.Fatal("test mode deleted the rule")
	}
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.fcontext_delete_policy", del)) {
		t.Error("delete reported no change")
	}
	if selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.fcontext_delete_policy", del)) {
		t.Error("a second delete reported a change")
	}
}

func selinuxLocalPorts(t *testing.T, c *exec.Context) []selinuxPort {
	t.Helper()
	return parseSemanagePorts(liveSelinuxRun(t, c, "semanage", "port", "-l", "-C"))
}

func TestLiveSelinuxPortRules(t *testing.T) {
	c := selinuxLiveGate(t)
	selinuxGuardHost(t, c)
	t.Cleanup(func() {
		for _, argv := range [][]string{
			{"semanage", "port", "-d", "-p", "tcp", "18999"},
			{"semanage", "port", "-d", "-p", "tcp", "18990-18995"},
			{"semanage", "port", "-d", "-p", "udp", "18999"},
			{"semanage", "port", "-d", "-p", "tcp", "8080"},
		} {
			_, _ = c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		}
	})
	localType := func(proto, port string) string {
		typ, _ := selinuxPortIn(selinuxLocalPorts(t, c), proto, port)
		return typ
	}

	add := value.MapOf("name", "tcp/18999", "sel_type", "http_port_t")
	if !selinuxChanged(t, liveSelinuxExec(t, c, true, "selinux.port_add_policy", add)) {
		t.Error("test mode predicted nothing")
	}
	if localType("tcp", "18999") != "" {
		t.Fatal("test mode added the rule")
	}
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.port_add_policy", add)) {
		t.Error("add reported no change")
	}
	if got := localType("tcp", "18999"); got != "http_port_t" {
		t.Errorf("semanage -C has tcp/18999 as %q", got)
	}
	if selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.port_add_policy", add)) {
		t.Error("a second add reported a change")
	}
	add.Set("sel_type", "http_cache_port_t")
	if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.port_add_policy", add)) {
		t.Error("changing the type reported no change")
	}
	if got := localType("tcp", "18999"); got != "http_cache_port_t" {
		t.Errorf("after -m tcp/18999 is %q", got)
	}

	// A range, and a range whose ends are equal, which semanage lists as
	// the one port.
	liveSelinuxExec(t, c, false, "selinux.port_add_policy", value.MapOf("name", "tcp/18990-18995", "sel_type", "http_port_t"))
	if got := localType("tcp", "18990-18995"); got != "http_port_t" {
		t.Errorf("the range is %q", got)
	}
	liveSelinuxExec(t, c, false, "selinux.port_add_policy", value.MapOf("name", "x", "protocol", "udp", "port", "18999-18999", "sel_type", "http_port_t"))
	if got := localType("udp", "18999"); got != "http_port_t" {
		t.Errorf("udp/18999-18999 is listed as %q under 18999", got)
	}
	if selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.port_add_policy", value.MapOf("name", "udp/18999", "sel_type", "http_port_t"))) {
		t.Error("udp/18999 after udp/18999-18999 reported a change")
	}

	// Over a port the policy has: the local rule must win in the answer,
	// although the full listing still shows the policy's type.
	liveSelinuxExec(t, c, false, "selinux.port_add_policy", value.MapOf("name", "tcp/8080", "sel_type", "http_port_t"))
	full := liveSelinuxRun(t, c, "semanage", "port", "-l")
	if typ, ok := selinuxPortIn(parseSemanagePorts(full), "tcp", "8080"); !ok || typ != "http_cache_port_t" {
		t.Logf("the full listing's first row for tcp/8080 is %q; the case this guards is that it still lists the policy's", typ)
	}
	got := liveSelinuxExec(t, c, false, "selinux.port_get_policy", value.MapOf("name", "tcp/8080")).(*value.Map)
	if typ, _ := got.Get("sel_type"); typ != "http_port_t" {
		t.Errorf("port_get_policy tcp/8080 = %v after the local rule", got)
	}
	if stale := liveSelinuxExec(t, c, false, "selinux.port_get_policy", value.MapOf("name", "tcp/8080", "sel_type", "http_cache_port_t")); stale != nil {
		t.Errorf("port_get_policy still finds tcp/8080 as http_cache_port_t: %v", stale)
	}
	del := liveSelinuxExec(t, c, false, "selinux.port_delete_policy", value.MapOf("name", "tcp/8080")).(*value.Map)
	if comment, _ := del.Get("comment"); !strings.Contains(comment.(string), "http_cache_port_t") {
		t.Errorf("deleting the override did not say the policy's type is back: %v", comment)
	}
	if _, err := New().Exec.Call(c, "selinux.port_delete_policy", value.MapOf("name", "tcp/80")); err == nil {
		t.Error("deleting the policy's tcp/80 was accepted")
	}
	for _, bad := range []string{"tcp/18997-18990", "tcp/70000", "sctp/18999", "18999"} {
		if _, err := New().Exec.Call(c, "selinux.port_add_policy", value.MapOf("name", bad, "sel_type", "http_port_t")); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}

	for _, name := range []string{"tcp/18999", "tcp/18990-18995", "udp/18999"} {
		d := value.MapOf("name", name)
		if !selinuxChanged(t, liveSelinuxExec(t, c, true, "selinux.port_delete_policy", d)) {
			t.Errorf("%s: test mode predicted nothing", name)
		}
		if !selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.port_delete_policy", d)) {
			t.Errorf("%s: delete reported no change", name)
		}
		if selinuxChanged(t, liveSelinuxExec(t, c, false, "selinux.port_delete_policy", d)) {
			t.Errorf("%s: a second delete reported a change", name)
		}
	}
	if left := selinuxLocalPorts(t, c); len(left) != 0 {
		t.Errorf("local port rules left: %v", left)
	}
}

// On a node with no SELinux every function says so rather than
// answering as if there were a policy with nothing in it.
func TestLiveSelinuxRefusesWhereThereIsNone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("SELinux is Linux; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1; this asks the module about a node's SELinux")
	}
	if ok, _ := selinuxEnabled(); ok {
		t.Skip("SELinux is running here; the other TestLiveSelinux* tests are for this node")
	}
	c := realCtx(t)
	if got := liveSelinuxExec(t, c, false, "selinux.getenforce", value.NewMap(0)); got != "Disabled" {
		t.Errorf("getenforce = %v, want Disabled", got)
	}
	for _, name := range []string{"selinux.list_sebool", "selinux.list_semod"} {
		if _, err := New().Exec.Call(c, name, value.NewMap(0)); err == nil || !strings.Contains(err.Error(), "not running") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := New().Exec.Call(c, "selinux.setenforce", value.MapOf("mode", "Enforcing")); err == nil {
		t.Error("setenforce was accepted")
	}
	if _, err := New().Exec.Call(c, "file.get_selinux_context", value.MapOf("path", "/etc/passwd")); err == nil {
		t.Error("file.get_selinux_context answered on a node with no contexts")
	} else {
		t.Logf("file.get_selinux_context: %v", err)
	}
	if res := liveSelinuxState(t, c, false, "selinux.mode", value.MapOf("name", "enforcing")); !res.Failed() {
		t.Errorf("selinux.mode: %s", res.Comment)
	} else {
		t.Logf("selinux.mode: %s", res.Comment)
	}
}
