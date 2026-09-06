package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// pfp is the provider under test. A value rather than a literal at each
// call site, because `pfProvider{}.Apply(...)` inside an `if` is a
// composite literal Go will not parse there.
var pfp = pfProvider{}

// pfFixture gives a context whose pfctl says what a test says.
func pfFixture(t *testing.T, responses map[string]exec.Result) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	if responses == nil {
		responses = map[string]exec.Result{}
	}
	runner := &exec.RecordingRunner{Responses: responses}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "pfctl" {
			return "/sbin/pfctl"
		}
		return ""
	}
	return c, runner
}

// pfConf points PFConfPath at a file a test writes, so the anchor check
// can be exercised from any host.
func pfConf(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pf.conf")
	if body != "" {
		writeFile(t, path, body)
	}
	old := PFConfPath
	PFConfPath = path
	t.Cleanup(func() { PFConfPath = old })
}

// A rule renders the way pf reads it, and the way pf prints it back.
//
// The second half is what makes the identity work: this provider
// compares its rendered text against what `pfctl -s rules` printed, so
// `any` has to be written out rather than omitted even though pf accepts
// both spellings.
func TestAPFRuleRendersTheWayPFPrintsItBack(t *testing.T) {
	for _, tc := range []struct {
		what string
		rule firewallRule
		want string
	}{
		{"a port from a network",
			firewallRule{Action: "allow", Port: "22", Protocol: "tcp", From: "10.0.0.0/8"},
			"pass in quick proto tcp from 10.0.0.0/8 to any port 22"},
		{"deny drops silently",
			firewallRule{Action: "deny", Port: "23", Protocol: "tcp"},
			"block drop in quick proto tcp from any to any port 23"},
		{"reject answers, which is the difference an operator cares about",
			firewallRule{Action: "reject", Port: "25", Protocol: "tcp"},
			"block return in quick proto tcp from any to any port 25"},
		{"a whole address, no port",
			firewallRule{Action: "deny", From: "192.0.2.4"},
			"block drop in quick from 192.0.2.4 to any"},
		{"outbound",
			firewallRule{Action: "allow", Direction: "out", Port: "53", Protocol: "udp"},
			"pass out quick proto udp from any to any port 53"},
		{"a range passes through, because pf spells it the same way",
			firewallRule{Action: "allow", Port: "6000:6007", Protocol: "tcp"},
			"pass in quick proto tcp from any to any port 6000:6007"},
		{"a list is the one spelling that has to be translated",
			firewallRule{Action: "allow", Port: "80,443", Protocol: "tcp"},
			"pass in quick proto tcp from any to any port { 80, 443 }"},
		{"`any` written out is the same as empty",
			firewallRule{Action: "allow", From: "any", To: "ANY", Port: "22"},
			"pass in quick from any to any port 22"},
	} {
		got, err := pfRule(tc.rule)
		if err != nil {
			t.Errorf("%s: %v", tc.what, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s:\n  got  %s\n  want %s", tc.what, got, tc.want)
		}
	}
}

// Every rule carries `quick`, so the first match decides.
//
// pf is last-match-wins and this module's shape is first-match-wins.
// Without `quick` a later rule in the anchor silently overrides an
// earlier one, and since the anchor is rewritten and sorted on every
// change, which rule won would depend on alphabetical order — which is
// not a thing anybody writing `firewall.allowed` is thinking about.
func TestEveryPFRuleIsQuick(t *testing.T) {
	for _, r := range []firewallRule{
		{Action: "allow", Port: "22"},
		{Action: "deny", From: "192.0.2.0/24"},
		{Action: "reject", Port: "25", Protocol: "tcp", Direction: "out"},
	} {
		got, err := pfRule(r)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, " quick") {
			t.Errorf("a rule without quick is one a later rule can override silently: %s", got)
		}
	}
}

// A rule pf would not take is refused here rather than at pfctl.
func TestAPFRuleThatPFWouldRefuseIsRefusedHere(t *testing.T) {
	for _, tc := range []struct {
		what string
		rule firewallRule
	}{
		{"an action pf has no word for", firewallRule{Action: "log", Port: "22"}},
		{"no action at all", firewallRule{Port: "22"}},
		{"a direction that is not in or out", firewallRule{Action: "allow", Direction: "sideways"}},
		{"a protocol this module does not write", firewallRule{Action: "allow", Protocol: "icmp"}},
		{"a port with a space in it", firewallRule{Action: "allow", Port: "80 443"}},
		{"an empty entry in a port list", firewallRule{Action: "allow", Port: "80,,443"}},
	} {
		if got, err := pfRule(tc.rule); err == nil {
			t.Errorf("%s was accepted and rendered as %q", tc.what, got)
		}
	}
}

// `pfctl -s info` says enabled or disabled, and the uptime after it is
// not part of the answer.
func TestPFStatusReadsTheFirstWordAndNotTheUptime(t *testing.T) {
	on := parsePFInfo("Status: Enabled for 4 days 09:52:03           Debug: Urgent\n" +
		"\nState Table                          Total             Rate\n")
	if !on.Enabled {
		t.Error("an enabled pf read as disabled")
	}
	off := parsePFInfo("Status: Disabled                                  Debug: Urgent\n")
	if off.Enabled {
		t.Error("a disabled pf read as enabled")
	}
	// And no defaults are invented. pf has no per-direction default
	// policy; reporting one would be a guess presented as a fact.
	if len(on.Defaults) != 0 {
		t.Errorf("pf reported default policies it does not have: %v", on.Defaults)
	}
}

// SetDefault refuses, and says where the answer actually lives.
//
// This is the interface reshaping that `firewallProvider`'s comment
// predicted. ufw has a default policy per direction as a setting; pf has
// the last matching rule of a file halite does not own. Silently doing
// nothing would be the worst of the three options.
func TestPFRefusesADefaultPolicyAndSaysWhy(t *testing.T) {
	c, runner := pfFixture(t, nil)
	err := pfp.SetDefault(c, "incoming", "deny")
	if err == nil {
		t.Fatal("pf accepted a default policy it has no way to set")
	}
	for _, want := range []string{"pf.conf", "block all", "last matching rule"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if len(runner.Ran) != 0 {
		t.Errorf("a refused default ran %v", runner.RanCommands())
	}
}

// Rules that pf would load and never evaluate are refused.
//
// This is the failure worth the file read. `pfctl -a halite -f -`
// succeeds whether or not pf.conf references the anchor; without the
// reference the rules load, list back correctly, and match no packet.
// Everything reports right and the firewall does nothing.
func TestRulesAreRefusedWhenPFConfDoesNotReferenceTheAnchor(t *testing.T) {
	rules := "pass in quick proto tcp from any to any port 22\n"

	// No reference: refused, with the line to add.
	pfConf(t, "set skip on lo0\nblock all\npass out quick\n")
	c, runner := pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Code: 1, Stderr: "pfctl: No such file or directory\n"},
	})
	_, err := pfp.Apply(c, firewallRule{Action: "allow", Port: "22", Protocol: "tcp"}, false, false)
	if err == nil {
		t.Fatal("rules were loaded into an anchor nothing references; pf would report them " +
			"and match nothing against them")
	}
	for _, want := range []string{"anchor", "never be evaluated", "last-match-wins"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	for _, ran := range runner.RanCommands() {
		if strings.Contains(ran, "-f -") {
			t.Errorf("rules were loaded anyway: %v", runner.RanCommands())
		}
	}

	// Referenced: loaded.
	pfConf(t, "set skip on lo0\nblock all\nanchor \"halite\"\npass out quick\n")
	c, runner = pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Code: 1, Stderr: "pfctl: No such file or directory\n"},
	})
	changed, err := pfp.Apply(c, firewallRule{Action: "allow", Port: "22", Protocol: "tcp"}, false, false)
	if err != nil {
		t.Fatalf("a referenced anchor was refused: %v", err)
	}
	if !changed {
		t.Error("adding a rule to an empty anchor reported no change")
	}
	loaded := false
	for _, cmd := range runner.Ran {
		if strings.Contains(cmd.String(), "-f -") {
			loaded = true
			if cmd.Stdin != rules {
				t.Errorf("loaded %q, want %q", cmd.Stdin, rules)
			}
		}
	}
	if !loaded {
		t.Errorf("nothing was loaded: %v", runner.RanCommands())
	}
}

// The anchor reference is read the way pf reads it, and a commented-out
// one does not count.
//
// An operator who commented the anchor out to debug something would
// otherwise be told everything is fine — which is the case this check
// exists for, arriving by a different route.
func TestThePFConfAnchorReferenceIsReadProperly(t *testing.T) {
	for _, tc := range []struct {
		what string
		conf string
		want bool
	}{
		{"quoted", `anchor "halite"`, true},
		{"unquoted", `anchor halite`, true},
		{"a nested path still references it", `anchor "halite/*"`, true},
		{"indented", "    anchor \"halite\"", true},
		{"commented out", `# anchor "halite"`, false},
		{"commented out with no space", `#anchor "halite"`, false},
		{"a different anchor", `anchor "something-else"`, false},
		{"a prefix that is not the anchor", `anchor "halite-old"`, false},
		{"nothing at all", "block all\npass out\n", false},
	} {
		if got := pfConfReferencesAnchor(tc.conf, "halite"); got != tc.want {
			t.Errorf("%s: %q read as %v, want %v", tc.what, tc.conf, got, tc.want)
		}
	}
}

// An unreadable pf.conf does not refuse.
//
// Unreadable is not absent: a node whose pf.conf this process cannot
// read is one where the check cannot be made, and refusing there would
// stop a state on a technicality rather than on a fault.
func TestAnUnreadablePFConfDoesNotRefuse(t *testing.T) {
	pfConf(t, "")
	if _, err := os.Stat(PFConfPath); err == nil {
		t.Fatal("this test needs a pf.conf that is not there")
	}
	c, _ := pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Code: 1, Stderr: "pfctl: No such file or directory\n"},
	})
	if _, err := pfp.Apply(c, firewallRule{Action: "allow", Port: "22"}, false, false); err != nil {
		t.Errorf("an unreadable pf.conf refused the rule: %v", err)
	}
}

// Applying a rule that is already there changes nothing, and removing
// one that is not there changes nothing.
//
// The whole anchor is rewritten on every change, so without this a state
// would report a change on every run and reload the firewall each time.
func TestPFApplyIsIdempotent(t *testing.T) {
	existing := "pass in quick proto tcp from any to any port 22\n" +
		"block drop in quick proto tcp from any to any port 23\n"
	pfConf(t, "anchor \"halite\"\n")

	c, runner := pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Stdout: existing},
	})
	changed, err := pfp.Apply(c, firewallRule{Action: "allow", Port: "22", Protocol: "tcp"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a rule that was already loaded reported a change")
	}
	for _, ran := range runner.RanCommands() {
		if strings.Contains(ran, "-f -") {
			t.Errorf("an unchanged anchor was reloaded: %v", runner.RanCommands())
		}
	}

	c, runner = pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Stdout: existing},
	})
	changed, err = pfp.Apply(c, firewallRule{Action: "allow", Port: "9999", Protocol: "tcp"}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("removing a rule that is not loaded reported a change")
	}

	// Removing one that is there rewrites the anchor without it, and
	// keeps the other.
	c, runner = pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Stdout: existing},
	})
	changed, err = pfp.Apply(c, firewallRule{Action: "deny", Port: "23", Protocol: "tcp"}, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("removing a loaded rule reported no change")
	}
	for _, cmd := range runner.Ran {
		if !strings.Contains(cmd.String(), "-f -") {
			continue
		}
		if strings.Contains(cmd.Stdin, "port 23") {
			t.Errorf("the removed rule is still in the anchor: %q", cmd.Stdin)
		}
		if !strings.Contains(cmd.Stdin, "port 22") {
			t.Errorf("removing one rule dropped another: %q", cmd.Stdin)
		}
	}
}

// Test mode reports what would change and loads nothing.
func TestPFApplyInTestModeLoadsNothing(t *testing.T) {
	pfConf(t, "anchor \"halite\"\n")
	c, runner := pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Stdout: ""},
	})
	changed, err := pfp.Apply(c, firewallRule{Action: "allow", Port: "22"}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("test mode predicted no change where there is one")
	}
	for _, ran := range runner.RanCommands() {
		if strings.Contains(ran, "-f -") {
			t.Errorf("test mode loaded rules: %v", runner.RanCommands())
		}
	}
}

// An anchor that has never been loaded is an empty rule set, not an
// error.
//
// It is the ordinary starting state on every node, and failing there
// would make the first `firewall.allowed` fail everywhere.
func TestAnAnchorThatDoesNotExistYetIsEmpty(t *testing.T) {
	c, _ := pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Code: 1, Stderr: "pfctl: No such file or directory\n"},
	})
	rules, err := pfp.anchorRules(c)
	if err != nil {
		t.Fatalf("an anchor that has never been loaded was an error: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("it read %d rules", len(rules))
	}

	// A failure that is not that one is still a failure.
	c, _ = pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Code: 1, Stderr: "pfctl: /dev/pf: Permission denied\n"},
	})
	if _, err := pfp.anchorRules(c); err == nil {
		t.Error("a permission failure read as an empty anchor")
	}
}

// Enabling a pf that is already enabled is the asked-for state, not a
// failure.
//
// `pfctl -e` exits non-zero and says so, and treating that as an error
// would make `firewall.enabled` fail on every run after the first.
func TestEnablingAnAlreadyEnabledPFIsNotAFailure(t *testing.T) {
	c, _ := pfFixture(t, map[string]exec.Result{
		"pfctl -e": {Code: 1, Stderr: "pfctl: pf already enabled\n"},
	})
	if err := pfp.SetEnabled(c, true); err != nil {
		t.Errorf("an already-enabled pf reported a failure: %v", err)
	}

	c, _ = pfFixture(t, map[string]exec.Result{
		"pfctl -d": {Code: 1, Stderr: "pfctl: pf not enabled\n"},
	})
	if err := pfp.SetEnabled(c, false); err != nil {
		t.Errorf("disabling an already-disabled pf reported a failure: %v", err)
	}

	// A real failure is still one.
	c, _ = pfFixture(t, map[string]exec.Result{
		"pfctl -e": {Code: 1, Stderr: "pfctl: /dev/pf: Permission denied\n"},
	})
	if err := pfp.SetEnabled(c, true); err == nil {
		t.Error("a permission failure was reported as success")
	}
}

// pf is offered on the platforms whose pf this understands, and not on
// macOS.
func TestPFIsOfferedOnTheRightPlatforms(t *testing.T) {
	c, _ := pfFixture(t, nil)
	available := pfp.Available(c)
	switch goos := runtime.GOOS; goos {
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		if !available {
			t.Errorf("pf is not offered on %s, where pfctl was found", goos)
		}
	default:
		if available {
			t.Errorf("pf is offered on %s; macOS has pfctl and Apple'''s ruleset, and "+
				"everything else has no pf at all", goos)
		}
	}

	// And a node with no pfctl never gets it, whatever the platform.
	c.Lookup = func(string) string { return "" }
	if pfp.Available(c) {
		t.Error("pf is offered on a node with no pfctl")
	}
}

// pf is registered, so a FreeBSD node reaches it through `firewall`.
func TestPFIsRegisteredAsAFirewallProvider(t *testing.T) {
	found := false
	for _, p := range firewallProviders {
		if p.Name() == "pf" {
			found = true
		}
	}
	if !found {
		t.Error("pf is not in firewallProviders, so no node can pick it")
	}
}
