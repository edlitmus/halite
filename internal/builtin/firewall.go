package builtin

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// firewallProvider is one host firewall. `firewall` is a virtual module
// in the same shape `pkg` and `service` are: the provider is chosen from
// what the node has, so an SLS file says `firewall.allowed` and the
// right tool runs. SPEC sections 15.2 and 15.5.
//
// **The interface is shaped by ufw, because ufw is the only provider.**
// That is worth stating rather than pretending otherwise. A second
// provider will probably reshape it: firewalld thinks in zones and
// services, nftables and pf in a whole ruleset that is replaced at once
// rather than a set of rules added one at a time, and neither maps
// cleanly onto "allow this port from that address". Designing for those
// now, with none of them in front of me, would produce an abstraction
// fitted to a guess.
//
// What is here is the part every host firewall does have: whether it is
// on, what it does with traffic nobody wrote a rule for, and a set of
// rules that permit or refuse something. That much should survive.
type firewallProvider interface {
	// Name is the provider's module name, such as "ufw".
	Name() string
	// Available reports whether this provider can run on this node.
	Available(c *exec.Context) bool
	// Status reports whether the firewall is on and what its defaults
	// are.
	Status(c *exec.Context) (firewallStatus, error)
	// SetEnabled turns the firewall on or off.
	SetEnabled(c *exec.Context, on bool) error
	// SetDefault sets the policy for traffic no rule matches.
	SetDefault(c *exec.Context, direction, policy string) error
	// Apply adds or removes a rule, and reports whether anything
	// changed. In test mode it must report what *would* change without
	// changing it.
	Apply(c *exec.Context, r firewallRule, remove, dryRun bool) (changed bool, err error)
	// Reload re-reads the rules without dropping connections.
	Reload(c *exec.Context) error
}

var firewallProviders = []firewallProvider{
	ufwProvider{},
	pfProvider{},
}

// firewallStatus is what a provider reports about itself.
type firewallStatus struct {
	Enabled bool
	// Defaults maps a direction — incoming, outgoing, routed — to the
	// policy applied to traffic no rule matched.
	Defaults map[string]string
}

// firewallRule is one permit or refuse.
//
// Deliberately small. Every field here is one every provider has some
// spelling of; anything richer — ufw's application profiles, firewalld's
// services, rate limiting — belongs to a provider and would be a
// parameter that means nothing on the others.
type firewallRule struct {
	// Action is allow, deny or reject. deny drops silently; reject
	// answers, which is the difference between a port that hangs and one
	// that refuses.
	Action string
	// Port is a port, a range such as 6000:6007, or a comma-separated
	// list. Empty means every port, which is what a rule about a whole
	// address is.
	Port string
	// Protocol is tcp or udp. Empty means both.
	Protocol string
	// From and To are addresses or networks. Empty means anywhere.
	From string
	To   string
	// Direction is in or out. Empty means in, which is what a rule is
	// about nearly always.
	Direction string
	// Comment is recorded with the rule where the provider supports it.
	Comment string
}

func pickFirewallProvider(c *exec.Context) (firewallProvider, error) {
	for _, p := range firewallProviders {
		if p.Available(c) {
			return p, nil
		}
	}
	// firewalld is named because it is the one a RHEL node has, and it is
	// deliberately a module of its own rather than a provider here
	// (firewalld.go says why) -- so the refusal points at it instead of
	// leaving the operator to conclude it is unsupported.
	return nil, fmt.Errorf("no host firewall this build manages was found on this node; " +
		"`firewall` has ufw and pf providers. On a firewalld host use the `firewalld` module, and for " +
		"raw rules `iptables` or `nftables` (SPEC section 15.3)")
}

func registerFirewall(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "status",
				Doc:      "Report whether the host firewall is on, and what it does with traffic no rule matches.",
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickFirewallProvider(c)
				if err != nil {
					return nil, err
				}
				st, err := p.Status(c)
				if err != nil {
					return nil, err
				}
				defaults := value.NewMap(len(st.Defaults))
				for _, d := range []string{"incoming", "outgoing", "routed"} {
					if v, ok := st.Defaults[d]; ok {
						defaults.Set(d, v)
					}
				}
				return value.MapOf(
					"provider", p.Name(),
					"enabled", st.Enabled,
					"defaults", defaults,
				), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "enable",
				Doc:     "Turn the host firewall on.",
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return firewallSetEnabled(c, true)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "disable",
				Doc:     "Turn the host firewall off.",
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return firewallSetEnabled(c, false)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "set_default",
				Doc: "Set what the firewall does with traffic no rule matches.",
				Params: []signature.Param{
					choice("direction", "incoming", "Which traffic.", "incoming", "outgoing", "routed"),
					choice("policy", nil, "What to do with it.", "allow", "deny", "reject"),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickFirewallProvider(c)
				if err != nil {
					return nil, err
				}
				direction := states.Str(args, "direction", "incoming")
				policy := states.Str(args, "policy", "")
				if c.Test {
					return true, nil
				}
				return true, p.SetDefault(c, direction, policy)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "allow",
				Doc:        "Permit traffic.",
				Params:     firewallRuleParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return firewallApply(c, args, "allow", false)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "deny",
				Doc:        "Refuse traffic, silently.",
				Params:     firewallRuleParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return firewallApply(c, args, "deny", false)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "delete",
				Doc:        "Remove a rule.",
				Params:     append(firewallRuleParams(), choice("action", "allow", "The rule's action.", "allow", "deny", "reject")),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return firewallApply(c, args, states.Str(args, "action", "allow"), true)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "reload",
				Doc:     "Re-read the rules without dropping connections.",
				Mutates: true, TestMode: signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				p, err := pickFirewallProvider(c)
				if err != nil {
					return nil, err
				}
				if c.Test {
					return true, nil
				}
				return true, p.Reload(c)
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "enabled",
				Doc: "Ensure the host firewall is on, and that its defaults are what the estate says.",
				Params: []signature.Param{
					nameParam("Ignored; the firewall is the node's. Defaults to the state ID."),
					opt("enabled", signature.Bool, true, "false turns the firewall off."),
					opt("incoming", signature.String, "", "The default policy for incoming traffic."),
					opt("outgoing", signature.String, "", "The default policy for outgoing traffic."),
					opt("routed", signature.String, "", "The default policy for routed traffic."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: firewallEnabledState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "allowed",
				Doc:        "Ensure a rule permitting traffic is present.",
				Params:     firewallStateParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return firewallRuleState(c, args, "allow", false)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "denied",
				Doc:        "Ensure a rule refusing traffic is present.",
				Params:     firewallStateParams(),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return firewallRuleState(c, args, "deny", false)
			},
		},
		states.Module{
			Sig: signature.Signature{
				Module: "firewall", Function: "absent",
				Doc: "Ensure a rule is not present.",
				Params: append(firewallStateParams(),
					choice("action", "allow", "The action of the rule to remove.", "allow", "deny", "reject")),
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Section:    "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return firewallRuleState(c, args, states.Str(args, "action", "allow"), true)
			},
		},
	)
}

func firewallRuleParams() []signature.Param {
	return []signature.Param{
		opt("port", signature.String, "", "A port, a range such as 6000:6007, or a comma-separated list. Empty is every port."),
		opt("protocol", signature.String, "", "tcp or udp. Empty is both."),
		opt("from", signature.String, "", "An address or network. Empty is anywhere."),
		opt("to", signature.String, "", "An address or network on this host. Empty is anywhere."),
		choice("direction", "in", "Which way the traffic goes.", "in", "out"),
		opt("comment", signature.String, "", "Recorded with the rule."),
	}
}

func firewallStateParams() []signature.Param {
	return append([]signature.Param{
		nameParam("The port, or a name for the rule when `port` is given. Defaults to the state ID."),
	}, firewallRuleParams()...)
}

func firewallSetEnabled(c *exec.Context, on bool) (any, error) {
	p, err := pickFirewallProvider(c)
	if err != nil {
		return nil, err
	}
	st, err := p.Status(c)
	if err != nil {
		return nil, err
	}
	if st.Enabled == on {
		return false, nil
	}
	if c.Test {
		return true, nil
	}
	return true, p.SetEnabled(c, on)
}

// firewallRuleFrom reads a rule out of a call's arguments.
//
// The state's `name` is the port when nothing else names one, so that
// the common rule is one line:
//
//	22/tcp:
//	  firewall.allowed: []
//
// A `name` that is not a port is a label for the rule, which is what a
// state naming an address needs.
func firewallRuleFrom(args *value.Map, action string) firewallRule {
	r := firewallRule{
		Action:    action,
		Port:      states.Str(args, "port", ""),
		Protocol:  states.Str(args, "protocol", ""),
		From:      states.Str(args, "from", ""),
		To:        states.Str(args, "to", ""),
		Direction: states.Str(args, "direction", "in"),
		Comment:   states.Str(args, "comment", ""),
	}
	if r.Port == "" {
		if name := strings.TrimSpace(states.Str(args, "name", "")); looksLikePortSpec(name) {
			port, proto, found := strings.Cut(name, "/")
			r.Port = port
			if found && r.Protocol == "" {
				r.Protocol = proto
			}
		}
	}
	return r
}

// looksLikePortSpec reports whether a state ID is a port rather than a
// label, so that `22/tcp:` is a rule and `allow the bastion:` is a name.
func looksLikePortSpec(name string) bool {
	if name == "" {
		return false
	}
	body, proto, found := strings.Cut(name, "/")
	if found && proto != "tcp" && proto != "udp" {
		return false
	}
	// A port, a range, or a list of them: digits, colons and commas.
	for _, r := range body {
		switch {
		case r >= '0' && r <= '9', r == ':', r == ',':
		default:
			return false
		}
	}
	return body != ""
}

func firewallApply(c *exec.Context, args *value.Map, action string, remove bool) (any, error) {
	p, err := pickFirewallProvider(c)
	if err != nil {
		return nil, err
	}
	rule := firewallRuleFrom(args, action)
	if err := rule.validate(); err != nil {
		return nil, err
	}
	return p.Apply(c, rule, remove, c.Test)
}

func (r firewallRule) validate() error {
	switch r.Action {
	case "allow", "deny", "reject":
	default:
		return fmt.Errorf("%q is not a firewall action; it takes allow, deny or reject", r.Action)
	}
	if r.Protocol != "" && r.Protocol != "tcp" && r.Protocol != "udp" {
		return fmt.Errorf("%q is not a protocol this manages; it takes tcp or udp", r.Protocol)
	}
	if r.Direction != "" && r.Direction != "in" && r.Direction != "out" {
		return fmt.Errorf("%q is not a direction; it takes in or out", r.Direction)
	}
	// The values below reach another program's grammar: pf reads a rule
	// as a line of pf.conf from `pfctl -f -`, and ufw reads them as argv.
	// They used to be checked for nothing, so `from: "10.0.0.1 \n pass
	// out quick all"` rendered a second pf rule nobody declared
	// (DIVERGENCE 5.291). So each is held to the shape its parameter
	// documents rather than to "anything without a newline": a
	// blocklist of pf's syntax characters would have to be complete to
	// be worth anything, and an allowlist only has to be right.
	if err := validateFirewallAddress("from", r.From); err != nil {
		return err
	}
	if err := validateFirewallAddress("to", r.To); err != nil {
		return err
	}
	if r.Port != "" {
		if err := validateFirewallPort(r.Port); err != nil {
			return err
		}
	}
	// A comment is free text and pf does not write it at all, but ufw
	// takes it as an argument and stores it; a line break has no
	// business in either.
	if strings.IndexFunc(r.Comment, unicode.IsControl) >= 0 {
		return fmt.Errorf("the comment %q contains a control character", r.Comment)
	}
	// A rule with nothing in it would be "allow everything", which is
	// not something to write by accident.
	if r.Port == "" && r.From == "" && r.To == "" {
		return fmt.Errorf("a rule needs at least a port or an address; one with neither permits everything")
	}
	return nil
}

// validateFirewallAddress holds an address to what the parameter says it
// is: "an address or network", or empty or `any` for anywhere.
//
// Accepted: an IPv4 or IPv6 address, a network in CIDR form, `any`, and
// a pf table written `<name>` — the last because a table is how pf
// spells "these addresses", and refusing it would push an operator
// towards writing the list into the rule. ufw has no tables and refuses
// one itself.
//
// Refused, deliberately: host names, interface names, negation (`!`)
// and lists (`{ a, b }`). pf would take all of them, and the renderer
// used to pass them through, but none is what the parameter documents,
// a host name is resolved once at load time and then printed back as an
// address that no longer matches the rule (DIVERGENCE 5.31's identity),
// and the characters a list or a negation needs are exactly the pf
// syntax that a value must not be able to carry. An IPv6 zone is refused
// for the same reason: `%` is not something pf reads there.
//
// net/netip rather than a pattern, so that `10.0.0.256` and `::g` are
// refused for being what they are, not for a character.
func validateFirewallAddress(field, a string) error {
	if strings.IndexFunc(a, unicode.IsControl) >= 0 {
		return fmt.Errorf("`%s` %q contains a control character; it takes one address or network", field, a)
	}
	a = strings.TrimSpace(a)
	if a == "" || strings.EqualFold(a, "any") {
		return nil
	}
	if strings.HasPrefix(a, "<") && strings.HasSuffix(a, ">") {
		if pfTableName(a[1 : len(a)-1]) {
			return nil
		}
		return fmt.Errorf("`%s` %q is not a pf table name: it takes letters, digits, `_` and `-`, "+
			"at most 31 of them", field, a)
	}
	if addr, err := netip.ParseAddr(a); err == nil && addr.Zone() == "" {
		return nil
	}
	if _, err := netip.ParsePrefix(a); err == nil {
		return nil
	}
	return fmt.Errorf("`%s` %q is not an address this module writes: it takes an IPv4 or IPv6 "+
		"address, a network such as 10.0.0.0/8, `any`, or a pf table as `<name>`", field, a)
}

// pfTableName is the subset of pf's table names this accepts: pf allows
// 31 characters (PF_TABLE_NAME_SIZE less its terminator), and the
// characters are held to the ones a name needs so that `>` and
// everything after it cannot be smuggled in.
func pfTableName(name string) bool {
	if name == "" || len(name) > 31 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// validateFirewallPort holds a port to what the parameter says it is: a
// port, a range such as 6000:6007, or a comma-separated list of either —
// and, because both providers take one, a service name such as `ssh`.
//
// Digits are checked as digits rather than through strconv, which takes
// `+22`; a leading sign, like a leading `-`, is a value ufw would read
// as something other than a port.
func validateFirewallPort(p string) error {
	if strings.IndexFunc(p, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return fmt.Errorf("port %q contains white space or a control character; "+
			"it takes a port, a range such as 6000:6007, or a list such as 80,443", p)
	}
	for _, entry := range strings.Split(p, ",") {
		if entry == "" {
			return fmt.Errorf("port %q has an empty entry in its list", p)
		}
		if lo, hi, isRange := strings.Cut(entry, ":"); isRange {
			l, lok := firewallPortNumber(lo)
			h, hok := firewallPortNumber(hi)
			if !lok || !hok || l > h {
				return fmt.Errorf("port %q: %q is not a range of ports, low:high between 1 and 65535", p, entry)
			}
			continue
		}
		if _, ok := firewallPortNumber(entry); ok || firewallServiceName(entry) {
			continue
		}
		return fmt.Errorf("port %q: %q is not a port between 1 and 65535 or a service name", p, entry)
	}
	return nil
}

func firewallPortNumber(s string) (int, bool) {
	if s == "" || len(s) > 5 {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, n >= 1 && n <= 65535
}

// firewallServiceName is the shape RFC 6335 gives a service name: up to
// 15 letters, digits and hyphens, starting with a letter, with no hyphen
// at either end.
func firewallServiceName(s string) bool {
	if s == "" || len(s) > 15 || s[len(s)-1] == '-' {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case (r >= '0' && r <= '9' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// describe renders a rule for a comment or a change set.
func (r firewallRule) describe() string {
	var b strings.Builder
	b.WriteString(r.Action)
	if r.Direction == "out" {
		b.WriteString(" out")
	}
	if r.From != "" {
		b.WriteString(" from " + r.From)
	}
	if r.To != "" {
		b.WriteString(" to " + r.To)
	}
	if r.Port != "" {
		b.WriteString(" port " + r.Port)
	}
	if r.Protocol != "" {
		b.WriteString("/" + r.Protocol)
	}
	return b.String()
}

func firewallEnabledState(c *exec.Context, args *value.Map) (states.Result, error) {
	p, err := pickFirewallProvider(c)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	st, err := p.Status(c)
	if err != nil {
		return states.False(fmt.Sprintf("The firewall's state could not be read: %v", err)), nil
	}

	wantOn := states.Bool(args, "enabled", true)
	changes := value.NewMap(4)
	if st.Enabled != wantOn {
		changes.Set("enabled", states.Change(st.Enabled, wantOn))
	}

	wantDefaults := map[string]string{}
	for _, direction := range []string{"incoming", "outgoing", "routed"} {
		want := states.Str(args, direction, "")
		if want == "" {
			continue
		}
		switch want {
		case "allow", "deny", "reject":
		default:
			return states.False(fmt.Sprintf("%q is not a policy for %s; it takes allow, deny or reject",
				want, direction)), nil
		}
		if st.Defaults[direction] != want {
			wantDefaults[direction] = want
			changes.Set("default_"+direction, states.Change(st.Defaults[direction], want))
		}
	}

	if changes.Len() == 0 {
		return states.True(fmt.Sprintf("The %s firewall is already as declared.", p.Name())), nil
	}
	if c.Test {
		return states.WouldChange(fmt.Sprintf("The %s firewall would be changed.", p.Name()), changes), nil
	}

	// Defaults before enabling, so that a firewall coming up for the
	// first time comes up with the policy the estate asked for rather
	// than with its own for as long as it takes to run the next line.
	for direction, policy := range wantDefaults {
		if err := p.SetDefault(c, direction, policy); err != nil {
			return states.False(fmt.Sprintf("The default for %s could not be set: %v", direction, err)), nil
		}
	}
	if st.Enabled != wantOn {
		if err := p.SetEnabled(c, wantOn); err != nil {
			return states.False(fmt.Sprintf("The firewall could not be turned %s: %v",
				onOff(wantOn), err)), nil
		}
	}
	return states.Changed(fmt.Sprintf("The %s firewall was changed.", p.Name()), changes), nil
}

func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func firewallRuleState(c *exec.Context, args *value.Map, action string, remove bool) (states.Result, error) {
	p, err := pickFirewallProvider(c)
	if err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}
	rule := firewallRuleFrom(args, action)
	if err := rule.validate(); err != nil {
		return states.False(fmt.Sprintf("%v", err)), nil
	}

	verb, past := "added", "was added"
	if remove {
		verb, past = "removed", "was removed"
	}

	// The provider answers whether this would change anything, because
	// it is the only thing that knows: ufw is idempotent and says so,
	// and asking it is more reliable than reproducing its own matching
	// rules here against a status listing it does not emit for
	// machines.
	changed, err := p.Apply(c, rule, remove, true)
	if err != nil {
		return states.False(fmt.Sprintf("The rule could not be checked: %v", err)), nil
	}
	if !changed {
		return states.True(fmt.Sprintf("`%s` is already %s.", rule.describe(),
			map[bool]string{true: "absent", false: "present"}[remove])), nil
	}

	changes := value.MapOf(rule.describe(), states.Change(
		map[bool]any{true: "present", false: nil}[remove],
		map[bool]any{true: nil, false: "present"}[remove],
	))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("`%s` would be %s.", rule.describe(), verb), changes), nil
	}
	if _, err := p.Apply(c, rule, remove, false); err != nil {
		return states.False(fmt.Sprintf("The rule could not be %s: %v", verb, err)), nil
	}
	return states.Changed(fmt.Sprintf("`%s` %s.", rule.describe(), past), changes), nil
}
