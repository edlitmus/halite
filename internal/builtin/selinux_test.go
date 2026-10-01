package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Every file under testdata/selinux was captured as root on 2026-09-30
// from two lab hosts, both enforcing the targeted policy:
//
//   - rocky9: Rocky Linux 9.8, policycoreutils 3.6-5.el9,
//     selinux-policy-targeted 38.1.75-2.el9_8
//   - alma8: AlmaLinux 8.10, policycoreutils 2.9-26.el8_10,
//     selinux-policy-targeted 3.14.3-139.el8_10.2
//
// They were taken with local rules in place so the listings show them:
// httpd_can_network_connect set on at runtime only; fcontext rules for
// /srv/halite-cap(/.*)?, /srv/halite-cap-dir (-f d) and /srv/halite-cap-user
// (-s user_u -r s0), and /vicepa changed from the policy's afs_files_t;
// ports tcp/18999, tcp/18990-18995 and udp/18999 as http_port_t, and
// tcp/8080 changed from the policy's http_cache_port_t. Every one was
// removed afterwards and the -C listings compared with the ones taken
// before. `fcontext-l.excerpt.txt` is the full listing's lines cut down
// (header, the local rules, one row of each file type, two `<<None>>`
// rows, and the tail with the equivalence section); each line kept is
// byte for byte what semanage printed. The full listings are ~7000 lines.
// `boolean-l-C.txt` is the -C listing as found, before anything changed.
//
// Nothing here is written from semanage(8). The two hosts' formats turned
// out the same, byte for byte, in every listing parsed here; what differs
// is content (364 booleans against 350, 441 modules against 421) and
// restorecon -R's order of traversal.

var selinuxHosts = []string{"rocky9", "alma8"}

func selinuxFixture(t *testing.T, host, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "selinux", host, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Captured on both hosts by `semodule -d zabbix` and `-e zabbix`, which
// the as-found listing has no example of.
const selinuxDisabledModuleRow = "100 zabbix            pp  disabled\n"

// restorecon -v output, captured. The order of the -R lines differs
// between the hosts and nothing here depends on it.
const (
	selinuxWouldRelabelSample = "Would relabel /srv/halite-selinux-test from unconfined_u:object_r:var_t:s0 to unconfined_u:object_r:public_content_t:s0\n" +
		"Would relabel /srv/halite-selinux-test/top from unconfined_u:object_r:var_t:s0 to unconfined_u:object_r:public_content_t:s0\n"
	selinuxRelabeledSample = "Relabeled /srv/halite-selinux-test/top from user_u:object_r:httpd_sys_content_t:s0 to system_u:object_r:public_content_t:s0\n"
)

func TestSELinuxIsRegistered(t *testing.T) {
	r := New()
	for _, m := range append(selinuxExecModules(), selinuxFileModules()...) {
		if !r.Exec.Has(m.Sig.Module + "." + m.Sig.Function) {
			t.Errorf("%s.%s is not registered", m.Sig.Module, m.Sig.Function)
		}
	}
	for _, m := range selinuxStateModules() {
		if !r.States.Has("selinux." + m.Sig.Function) {
			t.Errorf("selinux.%s is not registered", m.Sig.Function)
		}
	}
}

func TestSemanageBooleanListingReadsBothReleases(t *testing.T) {
	for _, host := range selinuxHosts {
		out := selinuxFixture(t, host, "boolean-l.txt")
		bools := parseSemanageBooleans(out)
		rows := len(strings.Split(strings.TrimSpace(out), "\n")) - 2 // header and blank line
		if len(bools) != rows {
			t.Errorf("%s: %d booleans read from %d rows", host, len(bools), rows)
		}
		if b := bools["httpd_can_network_connect"]; b.State != "on" || b.Default != "off" {
			t.Errorf("%s: the runtime-only change reads %+v, want State on, Default off", host, b)
		}
		// Thirty characters or more shifts the columns by one space.
		if b, ok := bools["awstats_purge_apache_log_files"]; !ok || b.State != "off" || !strings.HasPrefix(b.Description, "Determine whether awstats") {
			t.Errorf("%s: a 30-character name read as %+v", host, b)
		}
		local := parseSemanageBooleans(selinuxFixture(t, host, "boolean-l-C.txt"))
		if len(local) != 2 || local["virt_use_nfs"].Default != "on" {
			t.Errorf("%s: -C = %+v", host, local)
		}
	}
}

func TestSemanageFcontextListingKeepsTheToolsSpelling(t *testing.T) {
	for _, host := range selinuxHosts {
		rules := parseSemanageFcontexts(selinuxFixture(t, host, "fcontext-l.excerpt.txt"))
		seen := map[string]bool{}
		for _, r := range rules {
			seen[r.Filetype] = true
			if strings.Contains(r.Spec, "=") {
				t.Errorf("%s: an equivalence line was read as a rule: %+v", host, r)
			}
		}
		for _, words := range selinuxFiletypes {
			if !seen[words] {
				t.Errorf("%s: no rule of type %q was read", host, words)
			}
		}
		r, ok := selinuxFindFcontext(rules, "/srv/halite-cap(/.*)?", "all files")
		if !ok || r.Type != "httpd_sys_content_t" || r.User != "system_u" || r.Level != "s0" {
			t.Errorf("%s: /srv/halite-cap(/.*)? = %+v", host, r)
		}
		if r, _ := selinuxFindFcontext(rules, "/srv/halite-cap-dir", "directory"); r.Type != "public_content_t" {
			t.Errorf("%s: the directory rule = %+v", host, r)
		}
		if _, ok := selinuxFindFcontext(rules, "/srv/halite-cap-dir", "all files"); ok {
			t.Errorf("%s: a directory rule was found as an all-files one", host)
		}
		if r, _ := selinuxFindFcontext(rules, "/srv/halite-cap-user", "all files"); r.User != "user_u" {
			t.Errorf("%s: the user_u rule = %+v", host, r)
		}
		// The local /vicepa replaced the policy's row; only one is listed.
		n := 0
		for _, r := range rules {
			if r.Spec == "/vicepa" {
				n++
				if r.Type != "public_content_t" {
					t.Errorf("%s: /vicepa = %+v", host, r)
				}
			}
		}
		if n != 1 {
			t.Errorf("%s: /vicepa listed %d times", host, n)
		}
		if r, ok := selinuxFindFcontext(rules, `/\.journal`, "all files"); !ok || r.Context != "<<None>>" || r.Type != "" {
			t.Errorf("%s: a <<None>> rule = %+v", host, r)
		}
		if local := parseSemanageFcontexts(selinuxFixture(t, host, "fcontext-l-C.txt")); len(local) != 4 {
			t.Errorf("%s: -C read %d rules, want 4", host, len(local))
		}
	}
}

// The policy's tcp/8080 stays in its row after a local rule moves it,
// so the answer must come from -C first.
func TestSemanagePortLookupLetsTheLocalRuleWin(t *testing.T) {
	for _, host := range selinuxHosts {
		all := parseSemanagePorts(selinuxFixture(t, host, "port-l.txt"))
		local := parseSemanagePorts(selinuxFixture(t, host, "port-l-C.txt"))
		var listedUnder []string
		for _, row := range all {
			for _, p := range row.Ports {
				if row.Protocol == "tcp" && p == "8080" {
					listedUnder = append(listedUnder, row.Type)
				}
			}
		}
		if strings.Join(listedUnder, ",") != "http_cache_port_t,http_port_t" {
			t.Errorf("%s: tcp/8080 is listed under %v; the fixture should show it under both", host, listedUnder)
		}
		if typ, isLocal, ok := selinuxPortLookup(all, local, "tcp", "8080"); !ok || !isLocal || typ != "http_port_t" {
			t.Errorf("%s: tcp/8080 = %q local=%v", host, typ, isLocal)
		}
		if typ, isLocal, _ := selinuxPortLookup(all, nil, "tcp", "8080"); typ != "http_cache_port_t" || isLocal {
			t.Errorf("%s: without -C tcp/8080 = %q local=%v", host, typ, isLocal)
		}
		if typ, isLocal, _ := selinuxPortLookup(all, local, "tcp", "18990-18995"); typ != "http_port_t" || !isLocal {
			t.Errorf("%s: the range = %q local=%v", host, typ, isLocal)
		}
		if typ, isLocal, _ := selinuxPortLookup(all, local, "tcp", "22"); typ != "ssh_port_t" || isLocal {
			t.Errorf("%s: tcp/22 = %q local=%v", host, typ, isLocal)
		}
		if _, _, ok := selinuxPortLookup(all, local, "tcp", "18998"); ok {
			t.Errorf("%s: tcp/18998 was found", host)
		}
	}
}

func TestSemoduleListingKeepsTheHighestPriority(t *testing.T) {
	for _, host := range selinuxHosts {
		mods := parseSemoduleFull(selinuxFixture(t, host, "semodule-lfull.txt"))
		if m := mods["permissivedomains"]; m.Kind != "cil" || !m.Enabled {
			t.Errorf("%s: permissivedomains = %+v", host, m)
		}
		for _, m := range mods {
			if !m.Enabled {
				t.Errorf("%s: %s read as disabled; none was", host, m.Name)
			}
		}
	}
	if m := parseSemoduleFull(selinuxFixture(t, "alma8", "semodule-lfull.txt"))["cockpit"]; m.Priority != 200 {
		t.Errorf("cockpit, at 100 and 200 on alma8, = %+v", m)
	}
	if m := parseSemoduleFull(selinuxDisabledModuleRow)["zabbix"]; m.Enabled || m.Priority != 100 {
		t.Errorf("the disabled row = %+v", m)
	}
}

func TestRestoreconLinesReadBothSpellings(t *testing.T) {
	would := parseRestorecon(selinuxWouldRelabelSample)
	if len(would) != 2 || would[1].Path != "/srv/halite-selinux-test/top" ||
		would[1].To != "unconfined_u:object_r:public_content_t:s0" {
		t.Errorf("would = %+v", would)
	}
	done := parseRestorecon(selinuxRelabeledSample)
	if len(done) != 1 || done[0].From != "user_u:object_r:httpd_sys_content_t:s0" || done[0].To != "system_u:object_r:public_content_t:s0" {
		t.Errorf("done = %+v", done)
	}
	// Salt's pattern, which neither host printed, is not mistaken for one.
	if got := parseRestorecon("restorecon reset /srv/x context a:b:c:s0->a:b:d:s0\n"); len(got) != 0 {
		t.Errorf("the pre-2.7 spelling was read: %+v", got)
	}
}

func selinuxTestFS(t *testing.T, enforce string) {
	t.Helper()
	dir := t.TempDir()
	oldFS, oldConfig := SELinuxFSPath, SELinuxConfigPath
	SELinuxFSPath, SELinuxConfigPath = dir, filepath.Join(dir, "config")
	t.Cleanup(func() { SELinuxFSPath, SELinuxConfigPath = oldFS, oldConfig })
	if enforce != "" {
		if err := os.WriteFile(filepath.Join(dir, "enforce"), []byte(enforce), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := os.ReadFile(filepath.Join("testdata", "selinux", "rocky9", "selinux-config.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SELinuxConfigPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSELinuxModeReadsSelinuxfsAndTheConfig(t *testing.T) {
	selinuxTestFS(t, "1")
	if got, _ := selinuxGetenforce(); got != "Enforcing" {
		t.Errorf("getenforce = %s", got)
	}
	// The commented lines above SELINUX= mention "SELINUX=" too, as text.
	if got, _ := selinuxGetconfig(); got != "Enforcing" {
		t.Errorf("getconfig = %q", got)
	}
	selinuxTestFS(t, "")
	if got, _ := selinuxGetenforce(); got != "Disabled" {
		t.Errorf("no enforce file = %s", got)
	}
}

// selinux.mode refuses a mode the config disagrees with, before touching
// anything, and converges the one it agrees with.
func TestSELinuxModeStateRefusesWhatTheConfigWouldUndo(t *testing.T) {
	selinuxTestFS(t, "0")
	c := newCtx(false)
	res, _ := selinuxModeState(c, value.MapOf("name", "permissive"))
	if res.Changes.Len() != 0 {
		t.Fatalf("permissive against an enforcing config: %+v", res)
	}
	if !res.Failed() {
		t.Errorf("permissive was already the running mode and the config says enforcing: %s", res.Comment)
	}
	test := newCtx(true)
	if res, _ := selinuxModeState(test, value.MapOf("name", "enforcing")); res.Result != nil || !res.HasChanges() {
		t.Errorf("test mode: %+v", res)
	}
	if got, _ := selinuxGetenforce(); got != "Permissive" {
		t.Fatalf("test mode set %s", got)
	}
	if res, _ := selinuxModeState(c, value.MapOf("name", 1)); res.Failed() || !res.HasChanges() {
		t.Errorf("apply: %s", res.Comment)
	}
	if res, _ := selinuxModeState(c, value.MapOf("name", "Enforcing")); res.Failed() || res.HasChanges() {
		t.Errorf("second apply: %s", res.Comment)
	}
	if res, _ := selinuxModeState(c, value.MapOf("name", "disabled")); !res.Failed() {
		t.Errorf("disabled: %s", res.Comment)
	}
}

func TestSELinuxPortSpellings(t *testing.T) {
	for _, tc := range []struct{ name, proto, port, wantProto, wantPort string }{
		{"tcp/8080", "", "", "tcp", "8080"},
		{"udp/18990-18995", "", "", "udp", "18990-18995"},
		{"tcp/18998-18998", "", "", "tcp", "18998"},
		{"anything", "udp", "53", "udp", "53"},
	} {
		proto, port, err := selinuxPortSpec(tc.name, tc.proto, tc.port)
		if err != nil || proto != tc.wantProto || port != tc.wantPort {
			t.Errorf("%q = %s %s %v", tc.name, proto, port, err)
		}
	}
	for _, bad := range []string{"tcp/18997-18990", "tcp/70000", "tcp/0", "sctp/18999", "8080/tcp", "18999"} {
		if _, _, err := selinuxPortSpec(bad, "", ""); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestSELinuxFilespecRefusesWhatCanNeverMatch(t *testing.T) {
	for _, ok := range []string{"/", "/srv/www(/.*)?", "/srv//x"} {
		if err := selinuxFilespec(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "relative/path", "/srv/halite-ts/"} {
		if err := selinuxFilespec(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func selinuxRecorder(t *testing.T, host string, test bool) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	selinuxTestFS(t, "1")
	run := func(argv ...string) string { return exec.Command{Argv: argv}.String() }
	runner := &exec.RecordingRunner{Responses: map[string]exec.Result{
		run("semanage", "fcontext", "-l"):       {Stdout: selinuxFixture(t, host, "fcontext-l.excerpt.txt")},
		run("semanage", "fcontext", "-l", "-C"): {Stdout: selinuxFixture(t, host, "fcontext-l-C.txt")},
		run("semanage", "port", "-l"):           {Stdout: selinuxFixture(t, host, "port-l.txt")},
		run("semanage", "port", "-l", "-C"):     {Stdout: selinuxFixture(t, host, "port-l-C.txt")},
		run("semanage", "boolean", "-l"):        {Stdout: selinuxFixture(t, host, "boolean-l.txt")},
		run("semodule", "-lfull"):               {Stdout: selinuxFixture(t, host, "semodule-lfull.txt")},
	}}
	c := newCtx(test)
	c.Runner = runner
	c.Lookup = func(name string) string { return "/usr/sbin/" + name }
	return c, runner
}

func selinuxCall(c *exec.Context, function string, args *value.Map) (any, error) {
	for _, m := range selinuxExecModules() {
		if m.Sig.Function != function {
			continue
		}
		bound, errs := m.Sig.Bind(nil, args)
		if len(errs) > 0 {
			return nil, fmt.Errorf("selinux.%s: %v", function, errs)
		}
		return m.Fn(c, bound)
	}
	return nil, fmt.Errorf("selinux.%s is not a function", function)
}

// DIVERGENCE 5.113: OSRunner turns a non-zero exit into an error unless
// the command asks for its code, and the recorder does not. So the
// property is asserted instead of the behaviour.
func TestSELinuxCommandsAskForTheirExitCode(t *testing.T) {
	c, runner := selinuxRecorder(t, "rocky9", false)
	calls := []struct {
		fn   string
		args *value.Map
	}{
		{"list_sebool", value.NewMap(0)},
		{"list_semod", value.NewMap(0)},
		{"fcontext_get_policy", value.MapOf("name", "/var/www(/.*)?")},
		{"port_get_policy", value.MapOf("name", "tcp/22")},
		{"port_add_policy", value.MapOf("name", "tcp/18997", "sel_type", "http_port_t")},
		{"fcontext_add_policy", value.MapOf("name", "/srv/new(/.*)?", "sel_type", "public_content_t")},
		{"setsebool", value.MapOf("boolean", "httpd_can_network_connect", "value", "off", "persist", true)},
	}
	for _, call := range calls {
		_, _ = selinuxCall(c, call.fn, call.args)
	}
	if len(runner.Ran) < len(calls) {
		t.Fatalf("only %d commands ran", len(runner.Ran))
	}
	for _, cmd := range runner.Ran {
		if !cmd.IgnoreExitCode {
			t.Errorf("%s does not ask for its exit code", cmd.String())
		}
	}
}

// Exec test mode reads and predicts, and never runs a command that
// changes anything.
func TestSELinuxExecTestModeOnlyReads(t *testing.T) {
	for _, host := range selinuxHosts {
		c, runner := selinuxRecorder(t, host, true)
		for _, call := range []struct {
			fn   string
			args *value.Map
		}{
			{"port_add_policy", value.MapOf("name", "tcp/8080", "sel_type", "http_cache_port_t")},
			{"port_delete_policy", value.MapOf("name", "tcp/18999")},
			{"fcontext_add_policy", value.MapOf("name", "/srv/halite-cap(/.*)?", "sel_type", "public_content_t")},
			{"fcontext_delete_policy", value.MapOf("name", "/srv/halite-cap-dir", "filetype", "d")},
			{"setsebool", value.MapOf("boolean", "httpd_can_network_connect", "value", "off")},
		} {
			out, err := selinuxCall(c, call.fn, call.args)
			if err != nil {
				t.Errorf("%s %s: %v", host, call.fn, err)
				continue
			}
			if changed, _ := out.(*value.Map).Get("changed"); changed != true {
				t.Errorf("%s %s predicted nothing: %v", host, call.fn, out)
			}
		}
		for _, cmd := range runner.Ran {
			if !strings.Contains(cmd.String(), " -l") {
				t.Errorf("%s: test mode ran %s", host, cmd.String())
			}
		}
	}
}

// The policy's own rules are refused before semanage is asked.
func TestSELinuxRefusesToDeleteThePolicysOwnRules(t *testing.T) {
	c, runner := selinuxRecorder(t, "alma8", false)
	if _, err := selinuxCall(c, "port_delete_policy", value.MapOf("name", "tcp/22")); err == nil {
		t.Error("tcp/22 was accepted")
	}
	if _, err := selinuxCall(c, "fcontext_delete_policy", value.MapOf("name", "/vicepb")); err == nil {
		t.Error("/vicepb was accepted")
	}
	for _, cmd := range runner.Ran {
		if strings.Contains(cmd.String(), " -d") {
			t.Errorf("ran %s", cmd.String())
		}
	}
	// A state for a rule that is already as declared reports success.
	m := selinuxStateModules()
	for _, s := range m {
		if s.Sig.Function != "port_policy_present" {
			continue
		}
		bound, _ := s.Sig.Bind(nil, value.MapOf("name", "tcp/8080", "sel_type", "http_port_t"))
		res, _ := s.Fn(c, bound)
		if res.Failed() || res.HasChanges() {
			t.Errorf("tcp/8080 is http_port_t by a local rule: %+v", res)
		}
	}
}
