package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// Values that would put something other than one address, one port or
// one comment into a rule.
//
// The first is the one the issue 321 audit reproduced (M-37): pfAddress
// only trimmed, loadAnchor joined rules with a newline, and so this
// `from` became a second line of pf.conf — a rule nobody declared,
// loaded into the anchor with everything else. The rest are pf's own
// syntax, which a value has no legitimate use for here, and the leading
// `-` and `+` shapes ufw would read from argv as something other than a
// value.
var firewallValuesThatAreNotValues = []struct {
	what string
	rule firewallRule
}{
	{"a newline in from, carrying a second rule",
		firewallRule{Action: "allow", From: "10.0.0.1 \n pass out quick all"}},
	{"a newline in to",
		firewallRule{Action: "allow", To: "10.0.0.1\npass in quick all"}},
	{"a carriage return", firewallRule{Action: "allow", From: "10.0.0.1\rpass"}},
	{"a NUL", firewallRule{Action: "allow", From: "10.0.0.1\x00"}},
	{"a newline in the port", firewallRule{Action: "allow", Port: "22\npass in quick all"}},
	{"a newline in a port list", firewallRule{Action: "allow", Port: "22,\n80"}},
	{"a newline in the comment", firewallRule{Action: "allow", Port: "22", Comment: "ssh\npass"}},
	{"more pf after the address", firewallRule{Action: "allow", From: "10.0.0.1 port 22"}},
	{"a pf list", firewallRule{Action: "allow", From: "{ 10.0.0.1, 10.0.0.2 }"}},
	{"a negation", firewallRule{Action: "deny", From: "!10.0.0.1"}},
	{"a table that closes early", firewallRule{Action: "allow", From: "<t> port 22 <u>"}},
	{"a table with a space", firewallRule{Action: "allow", From: "<a b>"}},
	{"an empty table", firewallRule{Action: "allow", From: "<>"}},
	{"a host name", firewallRule{Action: "allow", From: "bastion.example.com"}},
	{"an interface", firewallRule{Action: "allow", From: "em0"}},
	{"an IPv6 zone", firewallRule{Action: "allow", From: "fe80::1%em0"}},
	{"an address that is not one", firewallRule{Action: "allow", From: "10.0.0.256"}},
	{"a leading dash ufw would read as an option", firewallRule{Action: "allow", From: "--dry-run"}},
	{"a port that is an option", firewallRule{Action: "allow", Port: "--force"}},
	{"a signed port, which strconv would take", firewallRule{Action: "allow", Port: "+22"}},
	{"port zero", firewallRule{Action: "allow", Port: "0"}},
	{"a port past the top", firewallRule{Action: "allow", Port: "65536"}},
	{"a range the wrong way round", firewallRule{Action: "allow", Port: "22:21"}},
	{"a range with a third part", firewallRule{Action: "allow", Port: "1:2:3"}},
	{"a pf comment in the port", firewallRule{Action: "allow", Port: "22#"}},
	{"pf's own brace in the port", firewallRule{Action: "allow", Port: "{22}"}},
	{"a service name ending in a dash", firewallRule{Action: "allow", Port: "ssh-"}},
}

// Every one of them is refused by the shared check, which both providers'
// states and functions call before a provider sees the rule — so ufw's
// argv is covered by the same refusal as pf's text.
func TestFirewallValidateRefusesValuesThatAreNotValues(t *testing.T) {
	for _, tc := range firewallValuesThatAreNotValues {
		if err := tc.rule.validate(); err == nil {
			t.Errorf("%s: %q was accepted", tc.what, tc.rule.describe())
		}
	}
}

// And pf refuses them itself, before pfctl runs at all.
//
// Apply is reachable without validate — these tests reach it that way —
// so the renderer holds the line too. Nothing may run: not the load, and
// not even the read, because a rule that cannot be rendered has no
// business looking at the anchor.
func TestPFRefusesValuesThatAreNotValuesBeforePfctlRuns(t *testing.T) {
	pfConf(t, "anchor \"halite\"\n")
	for _, tc := range firewallValuesThatAreNotValues {
		if tc.rule.Comment != "" {
			continue // pf does not write a comment; validate covers it
		}
		c, runner := pfFixture(t, map[string]exec.Result{
			"pfctl -a halite -s rules": {Stdout: ""},
		})
		if _, err := pfp.Apply(c, tc.rule, false, false); err == nil {
			t.Errorf("%s: pf accepted it", tc.what)
		}
		if len(runner.Ran) != 0 {
			t.Errorf("%s: pfctl ran: %v", tc.what, runner.RanCommands())
		}
	}
}

// The boundary itself refuses a rule that is two lines, however it was
// built.
func TestPFLoadAnchorRefusesARuleWithALineBreak(t *testing.T) {
	for _, rule := range []string{
		"pass in quick from 10.0.0.1 to any\npass out quick all",
		"pass in quick from 10.0.0.1 to any\rpass out quick all",
	} {
		c, runner := pfFixture(t, nil)
		if err := pfp.loadAnchor(c, []string{"block drop in quick all", rule}); err == nil {
			t.Errorf("%q was loaded", rule)
		}
		if len(runner.Ran) != 0 {
			t.Errorf("pfctl ran: %v", runner.RanCommands())
		}
	}
}

// What the parameters document still renders, and renders exactly as it
// did before the check.
func TestPFLegitimateValuesStillRenderAsBefore(t *testing.T) {
	for _, tc := range []struct {
		what string
		rule firewallRule
		want string
	}{
		{"an IPv4 address",
			firewallRule{Action: "allow", From: "192.0.2.4", Port: "22", Protocol: "tcp"},
			"pass in quick proto tcp from 192.0.2.4 to any port 22"},
		{"an IPv4 network",
			firewallRule{Action: "deny", From: "10.0.0.0/8"},
			"block drop in quick from 10.0.0.0/8 to any"},
		{"an IPv6 address",
			firewallRule{Action: "allow", From: "2001:db8::1", Port: "22", Protocol: "tcp"},
			"pass in quick proto tcp from 2001:db8::1 to any port 22"},
		{"an IPv6 network, to",
			firewallRule{Action: "reject", To: "2001:db8::/32"},
			"block return in quick from any to 2001:db8::/32"},
		{"any, in either case",
			firewallRule{Action: "allow", From: "any", To: "ANY", Port: "22"},
			"pass in quick from any to any port 22"},
		{"a pf table",
			firewallRule{Action: "allow", From: "<bastions>", Port: "22", Protocol: "tcp"},
			"pass in quick proto tcp from <bastions> to any port 22"},
		{"a port range",
			firewallRule{Action: "allow", Port: "6000:6007", Protocol: "tcp"},
			"pass in quick proto tcp from any to any port 6000:6007"},
		{"a port list with a range in it",
			firewallRule{Action: "allow", Port: "80,6000:6007", Protocol: "tcp"},
			"pass in quick proto tcp from any to any port { 80, 6000:6007 }"},
		{"a service name",
			firewallRule{Action: "allow", Port: "ssh", Protocol: "tcp"},
			"pass in quick proto tcp from any to any port ssh"},
		{"the top port",
			firewallRule{Action: "allow", Port: "65535", Protocol: "udp"},
			"pass in quick proto udp from any to any port 65535"},
		{"surrounding space, trimmed as it always was",
			firewallRule{Action: "allow", From: " 192.0.2.4 ", Port: "22"},
			"pass in quick from 192.0.2.4 to any port 22"},
	} {
		if err := tc.rule.validate(); err != nil {
			t.Errorf("%s: refused by validate: %v", tc.what, err)
			continue
		}
		got, err := pfRule(tc.rule)
		if err != nil {
			t.Errorf("%s: refused by pf: %v", tc.what, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s:\n  got  %s\n  want %s", tc.what, got, tc.want)
		}
	}
}

// ufw's argv is built from the same values, and the same legitimate ones
// get through validate to it unchanged.
func TestUFWLegitimateValuesStillReachItsArgv(t *testing.T) {
	r := firewallRule{Action: "allow", From: "2001:db8::/32", Port: "6000:6007", Protocol: "tcp",
		Comment: "x11 from the lab — a dash in a comment is still a comment"}
	if err := r.validate(); err != nil {
		t.Fatalf("refused: %v", err)
	}
	got := strings.Join(ufwRuleArgs(r), " ")
	want := "allow from 2001:db8::/32 to any port 6000:6007 proto tcp comment " + r.Comment
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}
