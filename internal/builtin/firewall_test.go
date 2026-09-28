package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// ufw takes two rule grammars and the short one is not a prefix of the
// long one.
//
// `ufw allow 22/tcp` and `ufw allow from 10.0.0.0/8 to any port 22 proto
// tcp` are the two, and mixing them produces a rule ufw parses as
// something else — which is a firewall rule that is not the one the tree
// asked for, and the worst class of defect this module can have.
func TestTheRuleGrammarPicksOneFormOrTheOther(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule firewallRule
		want string
	}{
		{
			"a bare port takes the short form",
			firewallRule{Action: "allow", Port: "22", Protocol: "tcp"},
			"allow 22/tcp",
		},
		{
			"a port with no protocol stays short",
			firewallRule{Action: "allow", Port: "80"},
			"allow 80",
		},
		{
			"an address forces the long form, and the unset side is `any`",
			firewallRule{Action: "allow", Port: "22", Protocol: "tcp", From: "10.0.0.0/8"},
			"allow from 10.0.0.0/8 to any port 22 proto tcp",
		},
		{
			"a rule about an address alone needs no port clause",
			firewallRule{Action: "deny", From: "192.0.2.1"},
			"deny from 192.0.2.1 to any",
		},
		{
			"outbound puts `out` where ufw wants it",
			firewallRule{Action: "allow", Port: "53", Protocol: "udp", Direction: "out"},
			"allow out 53/udp",
		},
		{
			"a comment is appended, not interpolated",
			firewallRule{Action: "allow", Port: "22", Comment: "bastion"},
			"allow 22 comment bastion",
		},
	} {
		got := strings.Join(ufwRuleArgs(tc.rule), " ")
		if got != tc.want {
			t.Errorf("%s:\n got  ufw %s\n want ufw %s", tc.name, got, tc.want)
		}
	}
}

// A protocol with no port cannot be written in the short form.
//
// `ufw allow tcp` is not a rule. Rather than emitting it and letting ufw
// reject something that looks plausible, the long form carries it.
func TestAProtocolWithNoPortDoesNotProduceANonsenseRule(t *testing.T) {
	got := strings.Join(ufwRuleArgs(firewallRule{Action: "allow", Protocol: "tcp", From: "10.0.0.0/8"}), " ")
	if !strings.Contains(got, "proto tcp") {
		t.Errorf("the protocol was lost: ufw %s", got)
	}
	if strings.Contains(got, "allow tcp") {
		t.Errorf("a bare protocol was emitted as a port: ufw %s", got)
	}
}

// ufw says whether it did anything, and both "already there" and
// "already gone" are successes that changed nothing.
func TestUFWSaysWhetherItChangedAnything(t *testing.T) {
	for _, tc := range []struct {
		out  string
		want bool
	}{
		{"Rule added\nRule added (v6)\n", true},
		{"Skipping adding existing rule\nSkipping adding existing rule (v6)\n", false},
		{"Could not delete non-existent rule\n", false},
		{"Rule deleted\n", true},
	} {
		if got := ufwChanged(tc.out); got != tc.want {
			t.Errorf("ufwChanged(%q) = %v, want %v", strings.TrimSpace(tc.out), got, tc.want)
		}
	}
}

// The status line carries both halves an operator needs.
func TestStatusReadsWhetherItIsOnAndWhatItDefaultsTo(t *testing.T) {
	st := parseUFWStatus("Status: active\n" +
		"Logging: on (low)\n" +
		"Default: deny (incoming), allow (outgoing), disabled (routed)\n" +
		"New profiles: skip\n")
	if !st.Enabled {
		t.Error("an active firewall read as off")
	}
	for direction, want := range map[string]string{
		"incoming": "deny", "outgoing": "allow", "routed": "disabled",
	} {
		if st.Defaults[direction] != want {
			t.Errorf("default %s = %q, want %q", direction, st.Defaults[direction], want)
		}
	}

	off := parseUFWStatus("Status: inactive\n")
	if off.Enabled {
		t.Error("an inactive firewall read as on")
	}
}

// A state ID that is a port is the rule; one that is a sentence is a
// label.
//
// `22/tcp:` should be a rule with no arguments at all, and `allow the
// bastion:` should not silently become a rule about a port named "allow
// the bastion".
func TestAStateIDIsAPortOnlyWhenItLooksLikeOne(t *testing.T) {
	for _, tc := range []struct {
		id   string
		port bool
	}{
		{"22", true},
		{"22/tcp", true},
		{"6000:6007", true},
		{"80,443/tcp", true},
		{"allow the bastion", false},
		{"22/sctp", false},
		{"ssh", false},
		{"", false},
	} {
		if got := looksLikePortSpec(tc.id); got != tc.port {
			t.Errorf("looksLikePortSpec(%q) = %v, want %v", tc.id, got, tc.port)
		}
	}

	// And the reading agrees: the ID supplies the port and the protocol.
	r := firewallRuleFrom(value.MapOf("name", "22/tcp"), "allow")
	if r.Port != "22" || r.Protocol != "tcp" {
		t.Errorf("the state ID gave port %q proto %q", r.Port, r.Protocol)
	}
	// An explicit argument wins over the ID.
	r = firewallRuleFrom(value.MapOf("name", "22/tcp", "port", "2222"), "allow")
	if r.Port != "2222" {
		t.Errorf("the ID overrode an explicit port: %q", r.Port)
	}
	// A label leaves the port to the arguments.
	r = firewallRuleFrom(value.MapOf("name", "allow the bastion", "from", "10.0.0.1"), "allow")
	if r.Port != "" {
		t.Errorf("a label was read as a port: %q", r.Port)
	}
}

// A rule with neither a port nor an address would permit everything.
func TestARuleThatWouldPermitEverythingIsRefused(t *testing.T) {
	err := firewallRule{Action: "allow", Direction: "in"}.validate()
	if err == nil {
		t.Fatal("a rule with no port and no address was accepted")
	}
	if !strings.Contains(err.Error(), "everything") {
		t.Errorf("the refusal does not say what the rule would do: %v", err)
	}
	// And the ordinary ones are fine.
	for _, r := range []firewallRule{
		{Action: "allow", Port: "22"},
		{Action: "deny", From: "192.0.2.1"},
		{Action: "reject", Port: "25", Protocol: "tcp"},
	} {
		if err := r.validate(); err != nil {
			t.Errorf("%s was refused: %v", r.describe(), err)
		}
	}
}

// A dry-run delete is read from what ufw actually prints.
//
// The fixtures are **captured**, not written from the manual: they are the
// tails of `ufw --dry-run --force delete allow 65010/tcp` on ufw 0.36.2,
// against a rule that was there and one that was not, taken from the linux
// fleet leg. A fixture built from documentation tests the documentation, and
// this is the second time in one change that mattered -- the step that
// captured these was itself wrong the first time, using `ufw --force allow`,
// which is a syntax error.
//
// The defect: `ufwChanged` looks for the phrases ufw prints when nothing
// changed, and a dry-run delete prints none of them. So the reading was
// "changed" every time and `firewall.absent` reported a removal on every run,
// for ever, against a rule already gone. DIVERGENCE 5.157.
func TestDryRunDeleteReadsWhetherARuleWouldGo(t *testing.T) {
	// Captured: a rule was there, so ufw would take it out.
	const wouldRemove = `### END LOGGING ###
COMMIT
Rules updated
Rules updated (v6)
`
	// Captured: nothing to remove, so ufw prints the ruleset and stops.
	const nothingToRemove = `### END LOGGING ###
COMMIT
`
	if !ufwDryRunDeleteChanged(wouldRemove) {
		t.Error("a dry-run delete that would remove a rule reported no change; " +
			"ufw says `Rules updated` when there is one to take out")
	}
	if ufwDryRunDeleteChanged(nothingToRemove) {
		t.Error("a dry-run delete with nothing to remove reported a change, which is " +
			"how firewall.absent came to report a removal on every run for ever")
	}

	// And the negative reading, which stays for every path that acts. These
	// are ufw's own words on a real run.
	if ufwChanged("Could not delete non-existent rule\nCould not delete non-existent rule (v6)\n") {
		t.Error("a real delete of a rule that is not there is not a change")
	}
	if !ufwChanged("Rules updated\nRules updated (v6)\n") {
		t.Error("a real delete that removed a rule is a change")
	}
	if ufwChanged("Skipping adding existing rule\nSkipping adding existing rule (v6)\n") {
		t.Error("adding a rule that is already there is not a change")
	}
}

// And through Apply, which is the call site.
//
// The test above exercises `ufwDryRunDeleteChanged` directly, so deleting its
// call site in `Apply` left it passing -- the same mistake this session made
// once already, on `tools/ledger`: a test of the helper is not a test of the
// wiring. This one goes through the provider with ufw's answers scripted, so
// removing the branch fails it.
func TestApplyReadsADryRunDeleteThroughTheProvider(t *testing.T) {
	rule := firewallRule{Action: "allow", Port: "65010", Protocol: "tcp"}
	argv := append([]string{"ufw", "--dry-run", "delete"}, ufwRuleArgs(rule)...)
	key := exec.Command{Argv: argv}.String()

	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"a rule is there to remove", "### END LOGGING ###\nCOMMIT\nRules updated\nRules updated (v6)\n", true},
		{"nothing to remove", "### END LOGGING ###\nCOMMIT\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCtx(true)
			c.Runner = &exec.RecordingRunner{
				Responses: map[string]exec.Result{key: {Code: 0, Stdout: tc.out}},
			}
			got, err := ufwProvider{}.Apply(c, rule, true, true)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if got != tc.want {
				t.Errorf("Apply reported changed=%v, want %v, for ufw saying:\n%s",
					got, tc.want, tc.out)
			}
		})
	}
}
