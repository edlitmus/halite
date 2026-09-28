package builtin

import (
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The four `firewall` states, driven against a real `ufw`.
//
// # The family where a wrong rule costs the machine
//
// This is the one set of states where getting it wrong takes the host off the
// network — which is why `iptables` and `nftables` are driven inside a private
// namespace rather than on the machine. `firewall` cannot go there: it is a
// virtual module over a provider, and `ufw` writes `/etc/ufw` and asks the
// running system to load rules, neither of which a network namespace makes
// private. So it runs on the machine, and the whole design of these cases is
// about making that safe.
//
// **Three of the four never enable the firewall.** `ufw allow`, `ufw delete`
// and their kind edit ufw's stored rules whether or not it is active, so a rule
// added while ufw is inactive is enforced by nothing at all. The port is 65010,
// which nothing listens on.
//
// That works because convergence here is read from **ufw's own words** rather
// than from `ufw status`: the provider looks for "Skipping adding existing
// rule" and "Could not delete non-existent rule", and test mode uses
// `ufw --dry-run`. Had it parsed `ufw status` instead, none of this would be
// possible — `status` prints no rules at all while ufw is inactive, so the
// cases would have had to turn the firewall on to see their own effect. A
// design decision made elsewhere, for other reasons, is what makes these three
// safe.
//
// **The fourth does enable it, with both defaults set to allow.** The state
// sets the defaults before it turns anything on -- checked, not assumed:
// `firewallEnabledState` runs its `SetDefault` loop before `SetEnabled` -- so
// the firewall comes up permitting everything and no connection is dropped.
// The original defaults are captured first and put back afterwards, because
// these are meant to be runnable on a real host by somebody who wants the
// answer for their own platform, and a test that leaves a firewall reconfigured
// is one nobody runs twice.
//
// # What stays uncovered, and why
//
// The `pf` provider. Its `SetDefault` refuses by design — pf has no
// per-direction default policy, and the refusal says so at length — so a
// `firewall.enabled` case on FreeBSD would exercise `pfctl -e` and nothing
// else. Enabling a packet filter on a host reachable only over SSH, to cover
// one line, is a trade this suite should not make on its own; and pf's rule
// path rewrites a whole anchor, which is a different enough mechanism to want
// its own case rather than a shared one. `unconformed` says so.
//
// DIVERGENCE 5.157.

// The port these rules name. High, unregistered, and nothing on any machine
// this runs on listens there -- so even if the firewall were active the rule
// would govern nothing.
const conformanceFirewallPort = "65010"

func firewallCases() []liveCase {
	root := liveRoot()
	r := New()

	// `ufw show added` lists the rules ufw holds, active or not. `ufw status`
	// would have been the obvious probe and is the wrong one: it prints
	// "Status: inactive" and no rules while ufw is off, so it cannot see what
	// these cases do.
	rulesProbe := func() (string, error) {
		res, err := root.Run(hexec.Command{
			Argv:           []string{"ufw", "show", "added"},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		var kept []string
		for _, line := range strings.Split(res.Stdout, "\n") {
			if strings.Contains(line, conformanceFirewallPort) {
				kept = append(kept, strings.TrimSpace(line))
			}
		}
		if len(kept) == 0 {
			return "no rule for " + conformanceFirewallPort, nil
		}
		sortStringsForProbe(kept)
		return strings.Join(kept, " | "), nil
	}

	// Removing by both spellings, because a rule added as `allow` and one
	// added as `deny` are different rules to ufw and a case that cleaned up
	// only its own action would leave the other behind for the next.
	clearRules := func() {
		for _, action := range []string{"allow", "deny"} {
			for _, dir := range [][]string{{}, {"out"}} {
				argv := append([]string{"ufw", "--force", "delete", action}, dir...)
				argv = append(argv, conformanceFirewallPort+"/tcp")
				_, _ = root.Run(hexec.Command{Argv: argv, IgnoreExitCode: true})
			}
		}
	}

	ruleArgs := func(extra ...any) *value.Map {
		base := []any{
			"name", "halite conformance " + conformanceFirewallPort,
			"port", conformanceFirewallPort,
			"protocol", "tcp",
		}
		return value.MapOf(append(base, extra...)...)
	}

	withUFW := func(lc liveCase) liveCase {
		lc.platforms = []string{"linux"}
		lc.needs = []string{"ufw"}
		return lc
	}

	cases := []liveCase{
		withUFW(liveCase{Conformance: states.Conformance{
			Name:    "firewall.allowed",
			Args:    ruleArgs(),
			Probe:   rulesProbe,
			Setup:   func() error { clearRules(); return nil },
			Cleanup: clearRules,
		}}),
		withUFW(liveCase{Conformance: states.Conformance{
			Name:    "firewall.denied",
			Args:    ruleArgs(),
			Probe:   rulesProbe,
			Setup:   func() error { clearRules(); return nil },
			Cleanup: clearRules,
		}}),
		withUFW(liveCase{Conformance: states.Conformance{
			// `absent` removes whatever action is there, so its setup adds
			// the rule through `firewall.allowed` -- the module's own way in,
			// rather than a second implementation of "add a rule" that could
			// pass while the module's was broken.
			Name:  "firewall.absent",
			Args:  ruleArgs(),
			Probe: rulesProbe,
			Setup: func() error {
				clearRules()
				_, err := r.States.Call(root, "firewall.allowed", ruleArgs())
				return err
			},
			Cleanup: clearRules,
		}}),
	}

	// ---- firewall.enabled ----
	//
	// The defaults are captured before anything is changed and restored
	// afterwards. `ufw status verbose` prints them as
	// "Default: allow (incoming), allow (outgoing), disabled (routed)".
	var savedIncoming, savedOutgoing string
	readDefaults := func() {
		res, err := root.Run(hexec.Command{
			Argv:           []string{"ufw", "status", "verbose"},
			IgnoreExitCode: true,
		})
		if err != nil || res.Code != 0 {
			return
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "Default:") {
				continue
			}
			for _, part := range strings.Split(strings.TrimPrefix(strings.TrimSpace(line), "Default:"), ",") {
				fields := strings.Fields(strings.TrimSpace(part))
				if len(fields) != 2 {
					continue
				}
				policy := fields[0]
				switch strings.Trim(fields[1], "()") {
				case "incoming":
					savedIncoming = policy
				case "outgoing":
					savedOutgoing = policy
				}
			}
			break
		}
	}
	statusProbe := func() (string, error) {
		res, err := root.Run(hexec.Command{
			Argv:           []string{"ufw", "status", "verbose"},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		var kept []string
		for _, line := range strings.Split(res.Stdout, "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "Status:") || strings.HasPrefix(t, "Default:") {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			return "ufw said nothing about its status", nil
		}
		return strings.Join(kept, " | "), nil
	}

	cases = append(cases, withUFW(liveCase{Conformance: states.Conformance{
		// Both defaults `allow`, so the firewall comes up permitting
		// everything: the state sets the defaults before it enables, so
		// nothing is ever blocked, not even briefly.
		Name: "firewall.enabled",
		Args: value.MapOf("name", "halite conformance", "enabled", true,
			"incoming", "allow", "outgoing", "allow"),
		Probe: statusProbe,
		Setup: func() error {
			readDefaults()
			// Off, and with a default this case will change, so there is
			// something to do. `deny` incoming is ufw's own default and is
			// harmless while ufw is inactive.
			_, _ = root.Run(hexec.Command{
				Argv: []string{"ufw", "--force", "disable"}, IgnoreExitCode: true})
			_, _ = root.Run(hexec.Command{
				Argv: []string{"ufw", "default", "deny", "incoming"}, IgnoreExitCode: true})
			return nil
		},
		Cleanup: func() {
			_, _ = root.Run(hexec.Command{
				Argv: []string{"ufw", "--force", "disable"}, IgnoreExitCode: true})
			for policy, direction := range map[string]string{
				savedIncoming: "incoming", savedOutgoing: "outgoing",
			} {
				if policy == "" {
					continue
				}
				_, _ = root.Run(hexec.Command{
					Argv:           []string{"ufw", "default", policy, direction},
					IgnoreExitCode: true,
				})
			}
		},
	}}))

	return cases
}
