package builtin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// Every fixture below is firewall-cmd's real output, captured on
// 2026-09-30 from two lab hosts: Rocky Linux 9.8 with firewalld 1.3.4,
// and AlmaLinux 8.10 with firewalld 0.9.11. Where the two printed
// different bytes, both are here and the test runs against each; where
// they printed the same bytes the constant says so. Nothing is written
// from firewall-cmd(1). The zone name in them, `halite-cap-6409`, is the
// throwaway zone the capture made and deleted.

// Identical on both hosts: a newline-terminated, space-separated list.
const firewalldServicesPublicSample = "cockpit dhcpv6-client ssh\n"

// An empty listing is a lone newline, not nothing (both hosts).
const firewalldEmptySample = "\n"

// `--permanent --zone=halite-cap-6409 --list-ports` and `--list-sources`
// (both hosts). The sources show that firewalld stores what it was
// given: `198.51.100.7/24` is not rewritten to its network.
const firewalldPortsSample = "8080/tcp 9000-9001/udp\n"
const firewalldSourcesSample = "192.0.2.0/24 198.51.100.7/24 2001:db8::/32\n"

// `--list-rich-rules` after adding
//
//	rule family=ipv4 source address=192.0.2.0/24 service name=ssh accept
//
// unquoted. It comes back with every value quoted -- the whole reason
// membership is asked of `--query-rich-rule` (both hosts, same bytes).
const firewalldRichRulesSample = `rule family="ipv4" source address="192.0.2.0/24" service name="ssh" accept
rule family="ipv4" source address="192.0.2.0/24" port port="8443" protocol="tcp" log prefix="halite test" level="info" limit value="3/m" accept
`

const firewalldRichRuleAsGiven = "rule family=ipv4 source address=192.0.2.0/24 service name=ssh accept"

// `--get-zones` on both hosts before anything was created.
const firewalldZonesSample = "block dmz drop external home internal nm-shared public trusted work\n"

// The two hosts' versions, which are the two lines that differ in
// `--version`.
var firewalldVersionSamples = map[string]string{
	"rocky9 (1.3.4)": "1.3.4\n",
	"alma8 (0.9.11)": "0.9.11\n",
}

// Error lines that differ between the versions, with their real exit
// codes. The code is the same on both; the words are not, which is why
// nothing here matches on the words beyond carrying them through.
var firewalldInvalidServiceSamples = map[string]string{
	"rocky9 (1.3.4)": "Error: INVALID_SERVICE: Zone 'halite-cap-6409': 'nosuchservice' not among existing services\n",
	"alma8 (0.9.11)": "Error: INVALID_SERVICE: 'nosuchservice' not among existing services\n",
}

func firewalldArgv(args ...string) string {
	return exec.Command{Argv: append([]string{"firewall-cmd"}, args...)}.String()
}

func firewalldCtx(test bool, responses map[string]exec.Result) (*exec.Context, *exec.RecordingRunner) {
	runner := &exec.RecordingRunner{Responses: responses, Default: exec.Result{Stdout: "success\n"}}
	c := newCtx(test)
	c.Runner = runner
	c.Lookup = func(name string) string { return "/usr/bin/" + name }
	return c, runner
}

// firewalldCall and firewalldApply call a function through its real
// signature -- defaults filled, undeclared parameters refused -- without
// the registry's platform check, so this file runs on the macOS and
// FreeBSD legs too. On Linux, TestFirewalldIsRegistered holds the
// registry to the same modules.
func firewalldCall(c *exec.Context, function string, args *value.Map) (any, error) {
	for _, m := range firewalldExecModules() {
		if m.Sig.Function != function {
			continue
		}
		bound, errs := m.Sig.Bind(nil, args)
		if len(errs) > 0 {
			return nil, fmt.Errorf("firewalld.%s: %v", function, errs)
		}
		return m.Fn(c, bound)
	}
	return nil, fmt.Errorf("firewalld.%s is not a function", function)
}

func firewalldApply(c *exec.Context, args *value.Map) (states.Result, error) {
	m := firewalldStateModule()
	bound, errs := m.Sig.Bind(nil, args)
	if len(errs) > 0 {
		return states.Result{}, fmt.Errorf("firewalld.present: %v", errs)
	}
	return m.Fn(c, bound)
}

func TestFirewalldIsRegistered(t *testing.T) {
	r := New()
	for _, m := range firewalldExecModules() {
		if !r.Exec.Has("firewalld." + m.Sig.Function) {
			t.Errorf("firewalld.%s is not registered", m.Sig.Function)
		}
	}
	if !r.States.Has("firewalld.present") {
		t.Error("firewalld.present is not registered")
	}
}

var (
	firewalldYes = exec.Result{Stdout: "yes\n"}
	firewalldNo  = exec.Result{Stdout: "no\n", Code: 1}
)

func TestFirewalldListingsSplitTheWayFirewallCmdPrintsThem(t *testing.T) {
	if got := firewalldWords(firewalldServicesPublicSample); strings.Join(got, ",") != "cockpit,dhcpv6-client,ssh" {
		t.Errorf("services = %q", got)
	}
	if got := firewalldWords(firewalldEmptySample); got == nil || len(got) != 0 {
		t.Errorf("an empty listing = %#v, want an empty, non-nil list", got)
	}
	if got := firewalldWords(firewalldSourcesSample); len(got) != 3 || got[1] != "198.51.100.7/24" {
		t.Errorf("sources = %q", got)
	}
	rules := firewalldLines(firewalldRichRulesSample)
	if len(rules) != 2 {
		t.Fatalf("rich rules split into %d, want 2 (one per line, not per word): %q", len(rules), rules)
	}
	if !strings.Contains(rules[1], `log prefix="halite test"`) {
		t.Errorf("the second rule lost its quoted, space-bearing prefix: %q", rules[1])
	}
	if got := firewalldLines(firewalldEmptySample); len(got) != 0 {
		t.Errorf("no rich rules = %q", got)
	}
}

func TestFirewalldVersionReadsBothReleases(t *testing.T) {
	for host, out := range firewalldVersionSamples {
		c, _ := firewalldCtx(false, map[string]exec.Result{firewalldArgv("--version"): {Stdout: out}})
		got, err := firewalldCall(c, "version", value.NewMap(0))
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if got != strings.TrimSpace(out) {
			t.Errorf("%s: version = %#v", host, got)
		}
	}
}

func TestFirewalldReadsAskThePermanentConfigurationUnlessToldNot(t *testing.T) {
	c, runner := firewalldCtx(false, map[string]exec.Result{
		firewalldArgv("--permanent", "--zone=public", "--list-services"): {Stdout: firewalldServicesPublicSample},
		firewalldArgv("--zone=public", "--list-services"):                {Stdout: "ssh\n"},
	})
	got, err := firewalldCall(c, "list_services", value.MapOf("zone", "public"))
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := got.([]any); len(list) != 3 {
		t.Errorf("permanent services = %#v", got)
	}
	got, err = firewalldCall(c, "list_services", value.MapOf("zone", "public", "permanent", false))
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := got.([]any); len(list) != 1 {
		t.Errorf("runtime services = %#v", got)
	}
	want := []string{
		firewalldArgv("--permanent", "--zone=public", "--list-services"),
		firewalldArgv("--zone=public", "--list-services"),
	}
	if got := runner.RanCommands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran %q, want %q", got, want)
	}
}

func TestFirewalldGetRichRulesKeepsEachRuleWhole(t *testing.T) {
	c, _ := firewalldCtx(false, map[string]exec.Result{
		firewalldArgv("--permanent", "--zone=halite-cap-6409", "--list-rich-rules"): {Stdout: firewalldRichRulesSample},
	})
	got, err := firewalldCall(c, "get_rich_rules", value.MapOf("zone", "halite-cap-6409"))
	if err != nil {
		t.Fatal(err)
	}
	list, _ := got.([]any)
	if len(list) != 2 {
		t.Fatalf("rich rules = %#v", got)
	}
}

// The error line is firewalld's, carried through, on both releases.
func TestFirewalldCarriesFirewallCmdsOwnError(t *testing.T) {
	for host, stderr := range firewalldInvalidServiceSamples {
		c, _ := firewalldCtx(false, map[string]exec.Result{
			firewalldArgv("--permanent", "--zone=z", "--query-service=nosuchservice"): firewalldNo,
			firewalldArgv("--permanent", "--zone=z", "--add-service=nosuchservice"):   {Stderr: stderr, Code: 101},
		})
		_, err := firewalldCall(c, "add_service", value.MapOf("service", "nosuchservice", "zone", "z"))
		if err == nil || !strings.Contains(err.Error(), "INVALID_SERVICE") || !strings.Contains(err.Error(), "exit 101") {
			t.Errorf("%s: err = %v, want firewalld's INVALID_SERVICE and its exit code", host, err)
		}
	}
	// 112 from a query is a failure to answer, not a "no": a zone that
	// does not exist must not read as "the service is absent, add it".
	c, runner := firewalldCtx(false, map[string]exec.Result{
		firewalldArgv("--permanent", "--zone=nosuchzone", "--query-service=http"): {
			Stderr: "Error: INVALID_ZONE: nosuchzone\n", Code: 112},
	})
	_, err := firewalldCall(c, "add_service", value.MapOf("service", "http", "zone", "nosuchzone"))
	if err == nil || !strings.Contains(err.Error(), "INVALID_ZONE") {
		t.Errorf("err = %v, want INVALID_ZONE", err)
	}
	if n := len(runner.Ran); n != 1 {
		t.Errorf("ran %d commands after a failed query, want only the query: %q", n, runner.RanCommands())
	}
}

func TestFirewalldAddAsksBeforeItChanges(t *testing.T) {
	query := firewalldArgv("--permanent", "--zone=z", "--query-port=8080/tcp")
	add := firewalldArgv("--permanent", "--zone=z", "--add-port=8080/tcp")
	args := value.MapOf("zone", "z", "port", "8080/tcp")

	// Already there: nothing is changed and no add is run.
	c, runner := firewalldCtx(false, map[string]exec.Result{query: firewalldYes})
	got, err := firewalldCall(c, "add_port", args)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := got.(*value.Map).Get("changed"); changed != false {
		t.Errorf("present port reported changed: %#v", got)
	}
	if strings.Contains(strings.Join(runner.RanCommands(), "\n"), "--add-port") {
		t.Errorf("an add ran for a port that was present: %q", runner.RanCommands())
	}

	// Absent, test mode: predicted, not run.
	c, runner = firewalldCtx(true, map[string]exec.Result{query: firewalldNo})
	got, err = firewalldCall(c, "add_port", args)
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := got.(*value.Map).Get("changed"); changed != true {
		t.Errorf("test mode did not predict the add: %#v", got)
	}
	if strings.Contains(strings.Join(runner.RanCommands(), "\n"), "--add-port") {
		t.Errorf("test mode ran the add: %q", runner.RanCommands())
	}

	// Absent: added.
	c, runner = firewalldCtx(false, map[string]exec.Result{query: firewalldNo})
	if _, err := firewalldCall(c, "add_port", args); err != nil {
		t.Fatal(err)
	}
	if got := runner.RanCommands(); len(got) != 2 || got[1] != add {
		t.Errorf("ran %q, want the query then %q", got, add)
	}
}

func TestFirewalldPassesARichRuleAsOneArgument(t *testing.T) {
	c, runner := firewalldCtx(false, map[string]exec.Result{
		firewalldArgv("--permanent", "--zone=z", "--query-rich-rule="+firewalldRichRuleAsGiven): firewalldNo,
	})
	if _, err := firewalldCall(c, "add_rich_rule",
		value.MapOf("zone", "z", "rule", firewalldRichRuleAsGiven)); err != nil {
		t.Fatal(err)
	}
	last := runner.Ran[len(runner.Ran)-1].Argv
	if want := "--add-rich-rule=" + firewalldRichRuleAsGiven; last[len(last)-1] != want {
		t.Errorf("last argument = %q, want the whole rule in one: %q", last[len(last)-1], want)
	}
}

// firewall-cmd created a zone called `bad/name` during the capture, on
// both hosts, as /etc/firewalld/zones/bad/name.xml.
func TestFirewalldRefusesAZoneNameFirewallCmdWouldTurnIntoAPath(t *testing.T) {
	for _, bad := range []string{"bad/name", "../etc", "", "this-zone-name-is-rather-long-x", "has space"} {
		c, runner := firewalldCtx(false, nil)
		if _, err := firewalldCall(c, "new_zone", value.MapOf("zone", bad)); err == nil {
			t.Errorf("new_zone(%q) was accepted", bad)
		}
		if len(runner.Ran) != 0 {
			t.Errorf("new_zone(%q) ran %q before refusing", bad, runner.RanCommands())
		}
	}
	for _, good := range []string{"halite-test", "nm-shared", "public", "a_b"} {
		if err := firewalldZoneName(good); err != nil {
			t.Errorf("%q refused: %v", good, err)
		}
	}
}

func TestFirewalldNewZoneReloadsUnlessToldNot(t *testing.T) {
	zones := firewalldArgv("--permanent", "--get-zones")
	c, runner := firewalldCtx(false, map[string]exec.Result{zones: {Stdout: firewalldZonesSample}})
	if _, err := firewalldCall(c, "new_zone", value.MapOf("zone", "halite-t")); err != nil {
		t.Fatal(err)
	}
	want := []string{zones, firewalldArgv("--permanent", "--new-zone=halite-t"), firewalldArgv("--reload")}
	if got := runner.RanCommands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran %q, want %q", got, want)
	}

	c, runner = firewalldCtx(false, map[string]exec.Result{zones: {Stdout: firewalldZonesSample}})
	if _, err := firewalldCall(c, "new_zone", value.MapOf("zone", "halite-t", "restart", false)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(runner.RanCommands(), "\n"), "--reload") {
		t.Errorf("restart: false reloaded: %q", runner.RanCommands())
	}

	// A zone that exists is left alone.
	c, runner = firewalldCtx(false, map[string]exec.Result{zones: {Stdout: firewalldZonesSample}})
	if _, err := firewalldCall(c, "new_zone", value.MapOf("zone", "public")); err != nil {
		t.Fatal(err)
	}
	if len(runner.Ran) != 1 {
		t.Errorf("an existing zone ran %q", runner.RanCommands())
	}
}

func TestFirewalldWillNotDeleteTheDefaultZone(t *testing.T) {
	c, runner := firewalldCtx(false, map[string]exec.Result{
		firewalldArgv("--get-default-zone"):         {Stdout: "public\n"},
		firewalldArgv("--permanent", "--get-zones"): {Stdout: firewalldZonesSample},
	})
	_, err := firewalldCall(c, "delete_zone", value.MapOf("zone", "public"))
	if err == nil || !strings.Contains(err.Error(), "default zone") {
		t.Errorf("err = %v, want a refusal naming the default zone", err)
	}
	if strings.Contains(strings.Join(runner.RanCommands(), "\n"), "--delete-zone") {
		t.Errorf("the default zone was deleted: %q", runner.RanCommands())
	}
}

// ---- firewalld.present ----

// The rich rule was given unquoted and firewalld lists it quoted. The
// query says it is there, so the state must say so too. Deciding from
// the listing instead is DIVERGENCE 5.31's defect, and this is the test
// that would see it come back.
func TestFirewalldPresentFindsARichRuleFirewalldRespelled(t *testing.T) {
	z := "halite-cap-6409"
	c, runner := firewalldCtx(false, map[string]exec.Result{
		firewalldArgv("--permanent", "--get-zones"):                                              {Stdout: "public " + z + "\n"},
		firewalldArgv("--permanent", "--zone="+z, "--list-rich-rules"):                           {Stdout: firewalldRichRulesSample},
		firewalldArgv("--permanent", "--zone="+z, "--query-rich-rule="+firewalldRichRuleAsGiven): firewalldYes,
	})
	res, err := firewalldApply(c,
		value.MapOf("name", z, "rich_rules", []any{firewalldRichRuleAsGiven}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed() || res.Changes.Len() != 0 {
		t.Errorf("a rule firewalld holds, spelled differently, was not found: %s %#v", res.Comment, res.Changes)
	}
	for _, ran := range runner.RanCommands() {
		if strings.Contains(ran, "--add-rich-rule") || strings.Contains(ran, "--reload") {
			t.Errorf("ran %q on a converged zone", ran)
		}
	}
}

func TestFirewalldPresentCreatesAZoneAndPredictsItFirst(t *testing.T) {
	z := "halite-t"
	responses := map[string]exec.Result{
		firewalldArgv("--permanent", "--get-zones"): {Stdout: firewalldZonesSample},
	}
	args := func() *value.Map {
		return value.MapOf("name", z, "services", []any{"http", "https"}, "ports", []any{"8080/tcp"})
	}

	c, runner := firewalldCtx(true, responses)
	res, err := firewalldApply(c, args())
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Errorf("test mode result = %v, want nil (would change): %s", *res.Result, res.Comment)
	}
	for _, key := range []string{"zone", "services", "ports"} {
		if _, ok := res.Changes.Get(key); !ok {
			t.Errorf("prediction lacks %s: %#v", key, res.Changes)
		}
	}
	// The zone does not exist, so nothing may be asked of it: firewall-cmd
	// would answer INVALID_ZONE.
	if got := runner.RanCommands(); len(got) != 1 {
		t.Errorf("test mode against a missing zone ran %q, want only --get-zones", got)
	}

	c, runner = firewalldCtx(false, responses)
	res, err = firewalldApply(c, args())
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed() {
		t.Fatalf("apply failed: %s", res.Comment)
	}
	want := []string{
		firewalldArgv("--permanent", "--get-zones"),
		firewalldArgv("--permanent", "--new-zone="+z),
		firewalldArgv("--permanent", "--zone="+z, "--add-service=http"),
		firewalldArgv("--permanent", "--zone="+z, "--add-service=https"),
		firewalldArgv("--permanent", "--zone="+z, "--add-port=8080/tcp"),
		firewalldArgv("--reload"),
	}
	if got := runner.RanCommands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// `--query-port=9000/udp` answers yes while `9000-9001/udp` is open --
// captured on both hosts. Pruning removes the range, so a pruning state
// that believed the query would remove the range and never add the port.
func TestFirewalldPruneDoesNotTrustARangeThatCoversThePort(t *testing.T) {
	z := "halite-t"
	c, runner := firewalldCtx(false, map[string]exec.Result{
		firewalldArgv("--permanent", "--get-zones"):                        {Stdout: "public " + z + "\n"},
		firewalldArgv("--permanent", "--zone="+z, "--list-ports"):          {Stdout: firewalldPortsSample},
		firewalldArgv("--permanent", "--zone="+z, "--query-port=9000/udp"): firewalldYes,
	})
	res, err := firewalldApply(c,
		value.MapOf("name", z, "ports", []any{"9000/udp"}, "prune_ports", true))
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed() {
		t.Fatal(res.Comment)
	}
	ran := strings.Join(runner.RanCommands(), "\n")
	for _, want := range []string{"--remove-port=8080/tcp", "--remove-port=9000-9001/udp", "--add-port=9000/udp"} {
		if !strings.Contains(ran, want) {
			t.Errorf("did not run %s; ran\n%s", want, ran)
		}
	}
}

func TestFirewalldPresentChecksTheDefaultZoneAndNeverSetsIt(t *testing.T) {
	c, runner := firewalldCtx(false, map[string]exec.Result{
		firewalldArgv("--get-default-zone"): {Stdout: "public\n"},
	})
	res, err := firewalldApply(c, value.MapOf("name", "work", "default", true))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed() || !strings.Contains(res.Comment, "--set-default-zone=work") {
		t.Errorf("result = %s, want a failure naming the command that would do it", res.Comment)
	}
	if strings.Contains(strings.Join(runner.RanCommands(), "\n"), "--set-default-zone") {
		t.Errorf("the default zone was changed: %q", runner.RanCommands())
	}
}

func TestFirewalldPresentRefusesWhatItDoesNotManage(t *testing.T) {
	for _, param := range []string{"masquerade", "interfaces", "prune_rich_rules", "port_fwd", "block_icmp"} {
		c, runner := firewalldCtx(false, nil)
		res, err := firewalldApply(c, value.MapOf("name", "halite-t", param, true))
		if err == nil && !res.Failed() {
			t.Errorf("%s was accepted and silently ignored", param)
		}
		if len(runner.Ran) != 0 {
			t.Errorf("%s ran %q before refusing", param, runner.RanCommands())
		}
	}
}

// OSRunner turns a non-zero exit into an error unless the command asks
// for the code; the query's "no" is exit 1, so a command that did not
// ask would read every absent member as a failure on a real host while
// passing here. DIVERGENCE 5.114.
func TestFirewalldCallsAskForTheirExitCode(t *testing.T) {
	z := "halite-t"
	c, runner := firewalldCtx(false, map[string]exec.Result{
		firewalldArgv("--permanent", "--get-zones"):                       {Stdout: "public " + z + "\n"},
		firewalldArgv("--permanent", "--zone="+z, "--query-service=http"): firewalldNo,
		firewalldArgv("--permanent", "--zone="+z, "--list-sources"):       {Stdout: firewalldEmptySample},
	})
	if _, err := firewalldApply(c,
		value.MapOf("name", z, "services", []any{"http"}, "sources", []any{"192.0.2.0/24"}, "prune_sources", true)); err != nil {
		t.Fatal(err)
	}
	if len(runner.Ran) < 5 {
		t.Fatalf("ran only %q", runner.RanCommands())
	}
	for _, ran := range runner.Ran {
		if !ran.IgnoreExitCode {
			t.Errorf("%q does not set IgnoreExitCode", ran.String())
		}
	}
}
