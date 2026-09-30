package builtin

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `firewalld`, driven against the real daemon on the machine running
// the tests.
//
// # Why it is safe to run on a host reached over SSH
//
// Every change is made to a zone this test creates, named `halite-fw-`
// and four random hex digits, with no interface bound to it. Traffic
// reaches a zone through an interface or a source; the only source this
// test binds is 192.0.2.0/24, TEST-NET-1, which no real peer sends from.
// So nothing this test does can change what happens to the SSH session
// it may be running under.
//
// The test does reload firewalld, because a zone created in the
// permanent configuration does not exist in the running firewall until
// it does, and `new_zone`/`delete_zone` reload by default the way Salt's
// do. A reload discards runtime-only changes on *every* zone. So before
// anything is changed it compares each non-test zone's runtime and
// permanent configuration, and skips, saying so, if they differ in
// anything but the interface binding (which NetworkManager makes at
// runtime and a reload keeps: captured on both lab hosts, where
// `enp1s0` stayed in `public` across three reloads). A host with a
// runtime-only rule somebody depends on is not one to run this on.
//
// And it checks its own work: every zone other than its own is
// snapshotted, runtime and permanent, before the first change, and
// compared after the cleanup. A difference fails the test.
//
// Gated on HALITE_SYSTEM_LIVE=1, root, `firewall-cmd`, and a running
// daemon, and it skips saying which is missing.

func firewalldLiveGate(t *testing.T) *exec.Context {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("firewalld is Linux; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; this creates and deletes a firewalld zone")
	}
	if os.Geteuid() != 0 {
		t.Skip("firewall-cmd needs root to change the configuration, and this is not root")
	}
	c := realCtx(t)
	if c.Which("firewall-cmd") == "" {
		t.Skip("this host has no firewall-cmd")
	}
	res, err := c.Run(exec.Command{Argv: []string{"firewall-cmd", "--state"}, IgnoreExitCode: true})
	if err != nil || res.Code != 0 {
		t.Skipf("firewalld is not running here (firewall-cmd --state: %q %q)", res.Stdout, res.Stderr)
	}
	return c
}

// firewalldZoneBlocks splits `--list-all-zones` into one block per zone,
// keyed by the zone's name with the " (active)"/" (default, active)"
// decoration removed, dropping this suite's own zones.
func firewalldZoneBlocks(out string) map[string]string {
	blocks := map[string]string{}
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		lines := strings.Split(block, "\n")
		name := strings.Fields(lines[0])
		if len(name) == 0 || strings.HasPrefix(name[0], "halite-") {
			continue
		}
		blocks[name[0]] = block
	}
	return blocks
}

func firewalldSnapshot(t *testing.T, c *exec.Context, permanent bool) map[string]string {
	t.Helper()
	argv := []string{"firewall-cmd", "--list-all-zones"}
	if permanent {
		argv = []string{"firewall-cmd", "--permanent", "--list-all-zones"}
	}
	res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
	if err != nil || res.Code != 0 {
		t.Fatalf("%v: %v %q", argv, err, res.Stderr)
	}
	return firewalldZoneBlocks(res.Stdout)
}

// firewalldComparable removes what legitimately differs between the
// runtime and permanent listing of the same zone: the header's
// decoration and the interfaces NetworkManager binds at runtime.
func firewalldComparable(block string) string {
	var kept []string
	for i, line := range strings.Split(block, "\n") {
		if i == 0 {
			kept = append(kept, strings.Fields(line)[0])
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "interfaces:") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// firewalldGuardHost is the precondition and the postcondition: it skips
// when a reload would lose something, and registers a cleanup-time
// check that nothing outside the test's zone moved.
func firewalldGuardHost(t *testing.T, c *exec.Context) {
	t.Helper()
	runtimeBefore := firewalldSnapshot(t, c, false)
	permanentBefore := firewalldSnapshot(t, c, true)
	for name, block := range runtimeBefore {
		if firewalldComparable(block) != firewalldComparable(permanentBefore[name]) {
			t.Skipf("zone %s's running configuration differs from its permanent one, and this test reloads "+
				"firewalld, which would discard the difference:\nruntime:\n%s\npermanent:\n%s",
				name, block, permanentBefore[name])
		}
	}
	// Registered first so it runs last, after the zone is gone.
	t.Cleanup(func() {
		for label, pair := range map[string][2]map[string]string{
			"runtime":   {runtimeBefore, firewalldSnapshot(t, c, false)},
			"permanent": {permanentBefore, firewalldSnapshot(t, c, true)},
		} {
			before, after := pair[0], pair[1]
			if len(before) != len(after) {
				t.Errorf("%s: %d zones before and %d after", label, len(before), len(after))
			}
			for name, block := range before {
				if after[name] != block {
					t.Errorf("%s zone %s changed:\nbefore:\n%s\nafter:\n%s", label, name, block, after[name])
				}
			}
		}
	})
}

func firewalldTestZone(t *testing.T, c *exec.Context) string {
	t.Helper()
	b := make([]byte, 2)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	zone := "halite-fw-" + hex.EncodeToString(b)
	// A zone delete leaves `<zone>.xml.old` behind in /etc/firewalld/zones
	// (captured on both hosts); it is this test's, so it goes too.
	t.Cleanup(func() {
		_, _ = c.Run(exec.Command{Argv: []string{"firewall-cmd", "--permanent", "--delete-zone=" + zone}, IgnoreExitCode: true})
		_, _ = c.Run(exec.Command{Argv: []string{"firewall-cmd", "--reload"}, IgnoreExitCode: true})
		_ = os.Remove("/etc/firewalld/zones/" + zone + ".xml.old")
		_ = os.Remove("/etc/firewalld/zones/" + zone + ".xml")
	})
	return zone
}

func liveFirewalld(t *testing.T, c *exec.Context, test bool, function string, args *value.Map) any {
	t.Helper()
	cc := *c
	cc.Test = test
	out, err := New().Exec.Call(&cc, "firewalld."+function, args)
	if err != nil {
		t.Fatalf("firewalld.%s(%v) test=%v: %v", function, args, test, err)
	}
	return out
}

func firewalldChanged(t *testing.T, out any) bool {
	t.Helper()
	m, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("result = %#v, want a map", out)
	}
	changed, _ := m.Get("changed")
	return changed == true
}

func firewalldList(t *testing.T, out any) []string {
	t.Helper()
	list, ok := out.([]any)
	if !ok {
		t.Fatalf("result = %#v, want a list", out)
	}
	var s []string
	for _, v := range list {
		s = append(s, v.(string))
	}
	return s
}

func TestLiveFirewalldReadsTheRealDaemon(t *testing.T) {
	c := firewalldLiveGate(t)
	if v, _ := liveFirewalld(t, c, false, "version", value.NewMap(0)).(string); v == "" {
		t.Error("version is empty")
	}
	def, _ := liveFirewalld(t, c, false, "default_zone", value.NewMap(0)).(string)
	zones := firewalldList(t, liveFirewalld(t, c, false, "get_zones", value.NewMap(0)))
	if !contains(zones, def) {
		t.Errorf("the default zone %q is not among get_zones %q", def, zones)
	}
	if svc := firewalldList(t, liveFirewalld(t, c, false, "get_services", value.NewMap(0))); !contains(svc, "ssh") {
		t.Errorf("get_services lacks ssh: %q", svc)
	}
	// list_services with no zone is the default zone's.
	a := firewalldList(t, liveFirewalld(t, c, false, "list_services", value.NewMap(0)))
	b := firewalldList(t, liveFirewalld(t, c, false, "list_services", value.MapOf("zone", def)))
	if strings.Join(a, " ") != strings.Join(b, " ") {
		t.Errorf("list_services() = %q and list_services(zone=%s) = %q", a, def, b)
	}
}

func TestLiveFirewalldDrivesAThrowawayZone(t *testing.T) {
	c := firewalldLiveGate(t)
	firewalldGuardHost(t, c)
	zone := firewalldTestZone(t, c)
	def, _ := liveFirewalld(t, c, false, "default_zone", value.NewMap(0)).(string)
	if def == zone {
		t.Fatalf("the default zone is %s, which this test was about to create", zone)
	}
	// A name firewall-cmd would turn into a path is refused before it
	// gets there.
	if _, err := New().Exec.Call(c, "firewalld.new_zone", value.MapOf("zone", "halite/"+zone[7:])); err == nil {
		t.Error("new_zone accepted a name with a slash")
	}

	// ---- new_zone ----
	if !firewalldChanged(t, liveFirewalld(t, c, true, "new_zone", value.MapOf("zone", zone))) {
		t.Error("test mode did not predict the zone")
	}
	if contains(firewalldList(t, liveFirewalld(t, c, false, "get_zones", value.NewMap(0))), zone) {
		t.Fatal("test mode created the zone")
	}
	if !firewalldChanged(t, liveFirewalld(t, c, false, "new_zone", value.MapOf("zone", zone))) {
		t.Error("new_zone reported no change")
	}
	if !contains(firewalldList(t, liveFirewalld(t, c, false, "get_zones", value.NewMap(0))), zone) {
		t.Fatal("the zone is not in the permanent configuration")
	}
	if !contains(firewalldList(t, liveFirewalld(t, c, false, "get_zones", value.MapOf("permanent", false))), zone) {
		t.Fatal("the zone is not in the running firewall: new_zone's reload did not happen")
	}
	if firewalldChanged(t, liveFirewalld(t, c, false, "new_zone", value.MapOf("zone", zone))) {
		t.Error("a second new_zone reported a change")
	}
	if got := firewalldList(t, liveFirewalld(t, c, false, "get_interfaces", value.MapOf("zone", zone))); len(got) != 0 {
		t.Fatalf("the test zone has interfaces %q; stopping before anything else touches it", got)
	}

	// ---- each member kind, permanent ----
	for _, tc := range []struct{ add, remove, list, param, item string }{
		{"add_service", "remove_service", "list_services", "service", "http"},
		{"add_port", "remove_port", "list_ports", "port", "8080/tcp"},
		{"add_source", "remove_source", "get_sources", "source", "192.0.2.0/24"},
		{"add_rich_rule", "remove_rich_rule", "get_rich_rules", "rule", firewalldRichRuleAsGiven},
	} {
		args := func() *value.Map { return value.MapOf("zone", zone, tc.param, tc.item) }
		listed := func() []string {
			return firewalldList(t, liveFirewalld(t, c, false, tc.list, value.MapOf("zone", zone)))
		}

		if !firewalldChanged(t, liveFirewalld(t, c, true, tc.add, args())) {
			t.Errorf("%s: test mode predicted nothing", tc.add)
		}
		if len(listed()) != 0 {
			t.Errorf("%s: test mode changed the zone: %q", tc.add, listed())
		}
		if !firewalldChanged(t, liveFirewalld(t, c, false, tc.add, args())) {
			t.Errorf("%s reported no change", tc.add)
		}
		if got := listed(); len(got) != 1 {
			t.Errorf("%s: listed %q, want one entry", tc.add, got)
		}
		// The second add must find what the first added -- for a rich
		// rule, spelled differently in the listing than it was given.
		if firewalldChanged(t, liveFirewalld(t, c, false, tc.add, args())) {
			t.Errorf("%s: a second add reported a change; listed %q", tc.add, listed())
		}
		if !firewalldChanged(t, liveFirewalld(t, c, true, tc.remove, args())) {
			t.Errorf("%s: test mode predicted nothing", tc.remove)
		}
		if len(listed()) != 1 {
			t.Errorf("%s: test mode changed the zone", tc.remove)
		}
		if !firewalldChanged(t, liveFirewalld(t, c, false, tc.remove, args())) {
			t.Errorf("%s reported no change", tc.remove)
		}
		if got := listed(); len(got) != 0 {
			t.Errorf("%s: still listed %q", tc.remove, got)
		}
		if firewalldChanged(t, liveFirewalld(t, c, false, tc.remove, args())) {
			t.Errorf("%s: a second remove reported a change", tc.remove)
		}
	}

	// firewalld respells a rich rule, which is the whole reason for
	// asking rather than comparing. Recorded so a release that stops
	// doing it is noticed, since the reason would then be gone.
	liveFirewalld(t, c, false, "add_rich_rule", value.MapOf("zone", zone, "rule", firewalldRichRuleAsGiven))
	rules := firewalldList(t, liveFirewalld(t, c, false, "get_rich_rules", value.MapOf("zone", zone)))
	if len(rules) != 1 || rules[0] == firewalldRichRuleAsGiven {
		t.Errorf("rich rules listed as %q; firewalld used to requote %q", rules, firewalldRichRuleAsGiven)
	}
	liveFirewalld(t, c, false, "remove_rich_rule", value.MapOf("zone", zone, "rule", rules[0]))

	// ---- the running firewall, and only it ----
	runtimeArgs := value.MapOf("service", "http", "zone", zone, "permanent", false)
	if !firewalldChanged(t, liveFirewalld(t, c, false, "add_service", runtimeArgs)) {
		t.Error("runtime add_service reported no change")
	}
	if got := firewalldList(t, liveFirewalld(t, c, false, "list_services", value.MapOf("zone", zone, "permanent", false))); !contains(got, "http") {
		t.Errorf("runtime services = %q", got)
	}
	if got := firewalldList(t, liveFirewalld(t, c, false, "list_services", value.MapOf("zone", zone))); contains(got, "http") {
		t.Errorf("a runtime add reached the permanent configuration: %q", got)
	}
	if !firewalldChanged(t, liveFirewalld(t, c, false, "reload_rules", value.NewMap(0))) {
		t.Error("reload_rules reported no change")
	}
	if got := firewalldList(t, liveFirewalld(t, c, false, "list_services", value.MapOf("zone", zone, "permanent", false))); contains(got, "http") {
		t.Errorf("reload_rules did not discard the runtime-only service: %q", got)
	}

	// ---- delete_zone ----
	if _, err := New().Exec.Call(c, "firewalld.delete_zone", value.MapOf("zone", def)); err == nil {
		t.Fatalf("delete_zone accepted the default zone %s", def)
	}
	if !firewalldChanged(t, liveFirewalld(t, c, true, "delete_zone", value.MapOf("zone", zone))) {
		t.Error("test mode predicted nothing")
	}
	if !contains(firewalldList(t, liveFirewalld(t, c, false, "get_zones", value.NewMap(0))), zone) {
		t.Fatal("test mode deleted the zone")
	}
	if !firewalldChanged(t, liveFirewalld(t, c, false, "delete_zone", value.MapOf("zone", zone))) {
		t.Error("delete_zone reported no change")
	}
	if contains(firewalldList(t, liveFirewalld(t, c, false, "get_zones", value.NewMap(0))), zone) {
		t.Error("the zone is still in the permanent configuration")
	}
	if contains(firewalldList(t, liveFirewalld(t, c, false, "get_zones", value.MapOf("permanent", false))), zone) {
		t.Error("the zone is still in the running firewall: delete_zone's reload did not happen")
	}
	if firewalldChanged(t, liveFirewalld(t, c, false, "delete_zone", value.MapOf("zone", zone))) {
		t.Error("a second delete_zone reported a change")
	}
}

// firewalld.present end to end, including the one case the conformance
// harness does not reach: pruning a port range that `--query-port`
// says covers the port being declared.
func TestLiveFirewalldPresentConvergesAndPrunes(t *testing.T) {
	c := firewalldLiveGate(t)
	firewalldGuardHost(t, c)
	zone := firewalldTestZone(t, c)
	r := New()

	apply := func(test bool, args *value.Map) (bool, bool) {
		t.Helper()
		cc := *c
		cc.Test = test
		res, err := r.States.Call(&cc, "firewalld.present", args)
		if err != nil {
			t.Fatalf("firewalld.present test=%v: %v", test, err)
		}
		if res.Failed() {
			t.Fatalf("firewalld.present test=%v failed: %s", test, res.Comment)
		}
		return res.Changes.Len() > 0, res.Result == nil
	}
	declared := func() *value.Map {
		return value.MapOf("name", zone,
			"services", []any{"http", "https"},
			"ports", []any{"8080/tcp", "9000-9001/udp"},
			"sources", []any{"192.0.2.0/24"},
			"rich_rules", []any{firewalldRichRuleAsGiven})
	}

	if changed, would := apply(true, declared()); !changed || !would {
		t.Errorf("test mode against a missing zone: changed=%v would=%v", changed, would)
	}
	if contains(firewalldList(t, liveFirewalld(t, c, false, "get_zones", value.NewMap(0))), zone) {
		t.Fatal("test mode created the zone")
	}
	if changed, _ := apply(false, declared()); !changed {
		t.Error("the first apply changed nothing")
	}
	if changed, _ := apply(false, declared()); changed {
		t.Error("the second apply changed something: not idempotent")
	}
	if changed, _ := apply(true, declared()); changed {
		t.Error("test mode after convergence predicted a change")
	}
	// The running firewall has it too, because present reloaded.
	if got := firewalldList(t, liveFirewalld(t, c, false, "list_ports", value.MapOf("zone", zone, "permanent", false))); !contains(got, "9000-9001/udp") {
		t.Errorf("runtime ports = %q: the reload did not happen", got)
	}

	// Now narrow to one port inside the range, pruning.
	pruned := value.MapOf("name", zone, "ports", []any{"9000/udp"}, "prune_ports", true)
	if changed, _ := apply(false, pruned); !changed {
		t.Error("pruning changed nothing")
	}
	if got := firewalldList(t, liveFirewalld(t, c, false, "list_ports", value.MapOf("zone", zone))); strings.Join(got, " ") != "9000/udp" {
		t.Errorf("after pruning, ports = %q, want exactly 9000/udp", got)
	}
	if changed, _ := apply(false, value.MapOf("name", zone, "ports", []any{"9000/udp"}, "prune_ports", true)); changed {
		t.Error("a second pruning apply changed something")
	}
	// Services, sources and the rule were not declared this time and not
	// pruned, so they stay.
	if got := firewalldList(t, liveFirewalld(t, c, false, "list_services", value.MapOf("zone", zone))); len(got) != 2 {
		t.Errorf("services = %q, want http and https untouched", got)
	}
}
