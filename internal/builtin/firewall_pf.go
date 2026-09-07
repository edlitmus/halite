package builtin

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
)

// PFAnchor is the anchor halite loads its rules into.
//
// A variable so a test can point it elsewhere, and a single name rather
// than one per rule because pf evaluates an anchor's rules as a set.
var PFAnchor = "halite"

// PFConfPath is the ruleset an operator maintains, read only to check
// that it references the anchor.
var PFConfPath = "/etc/pf.conf"

// pfProvider manages pf, the firewall on FreeBSD, OpenBSD, NetBSD and
// DragonFly — and on four of the five hosts this project's own fleet
// runs on, which is why it is the second provider rather than
// `nftables`.
//
// # It manages an anchor, and not pf.conf
//
// pf is not a tool that adds rules one at a time. It loads a ruleset,
// and `pfctl -f /etc/pf.conf` replaces the whole of it. A configuration
// management system that owned that file would own every rule on the
// host, including the ones an operator wrote by hand and the ones
// another tool put there, and would silently discard them on the first
// run.
//
// So halite loads into an **anchor**: `pfctl -a halite -f -` replaces
// the rules inside `anchor "halite"` and touches nothing else. The
// operator keeps pf.conf and decides where in the evaluation order the
// managed rules sit, which is a decision only they can make — pf is
// last-match-wins, so where the anchor is placed changes what it does.
//
// The cost is that pf.conf has to reference the anchor, and if it does
// not, the rules load successfully and are never evaluated. That is the
// worst failure this module could have — a firewall reporting rules it
// is not enforcing — so `Apply` checks for the reference and refuses
// with the line to add rather than writing rules into the void.
//
// # Every rule is `quick`
//
// pf is last-match-wins; ufw, and the `firewall` module's own shape, is
// first-match-wins. Emitting `quick` on every rule makes the first match
// decisive, which is what somebody writing `firewall.allowed` means. It
// also makes a rule's effect independent of the order the anchor happens
// to hold them in, which matters because this module rewrites the whole
// anchor on every change and nothing here promises an order.
//
// # What pf does not have
//
// A default policy per direction. ufw has `default deny (incoming)` as a
// setting; pf has whatever the last matching rule in the ruleset says,
// which is a property of the operator's pf.conf and not a knob. The
// interface comment on `firewallProvider` predicted that a second
// provider would reshape it, and this is where: `SetDefault` refuses,
// by name, rather than pretending. `firewall.status` reports no
// defaults on pf for the same reason — reporting a guess would be worse
// than reporting nothing.
type pfProvider struct{}

func (pfProvider) Name() string { return "pf" }

// Available requires pfctl and one of the platforms whose pf this
// understands.
//
// macOS is excluded deliberately. It has pf and it has pfctl, and its
// ruleset is Apple's: /etc/pf.conf loads anchors the system owns, the
// firewall is off by default and enabling it from underneath the system
// is not something a configuration management tool should do quietly. A
// macOS provider would be a different one, not this.
func (pfProvider) Available(c *exec.Context) bool {
	switch runtime.GOOS {
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		return c.Which("pfctl") != ""
	default:
		return false
	}
}

// Status reads `pfctl -s info`, whose first line is the only part of it
// this needs:
//
//	Status: Enabled for 4 days 09:52:03           Debug: Urgent
//
// Defaults are left empty. See the type comment: pf has no per-direction
// default policy, and inventing one from the last rule in the ruleset
// would be a guess presented as a fact.
func (pfProvider) Status(c *exec.Context) (firewallStatus, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"pfctl", "-s", "info"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return firewallStatus{}, err
	}
	if res.Code != 0 {
		return firewallStatus{}, fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return parsePFInfo(res.Stdout), nil
}

func parsePFInfo(stdout string) firewallStatus {
	st := firewallStatus{Defaults: map[string]string{}}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if !strings.HasPrefix(line, "Status:") {
			continue
		}
		// "Status: Enabled for 4 days" — the word after the colon is
		// the whole answer, and the uptime that follows it is not.
		rest := strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		word, _, _ := strings.Cut(rest, " ")
		st.Enabled = strings.EqualFold(word, "Enabled")
		break
	}
	return st
}

func (pfProvider) SetEnabled(c *exec.Context, on bool) error {
	flag := "-d"
	if on {
		flag = "-e"
	}
	res, err := c.Run(exec.Command{Argv: []string{"pfctl", flag}, IgnoreExitCode: true})
	if err != nil {
		return err
	}
	// pfctl -e on an already-enabled pf exits non-zero and says
	// "pfctl: pf already enabled". That is the asked-for state, not a
	// failure, and treating it as one would make `firewall.enabled`
	// fail on every run after the first.
	out := res.Stderr + res.Stdout
	if res.Code != 0 && !strings.Contains(out, "already enabled") && !strings.Contains(out, "not enabled") {
		return fmt.Errorf("%s", firstLine(out))
	}
	return nil
}

// SetDefault refuses, because pf has no such thing.
//
// The refusal names what to do instead. A default policy in pf is the
// last matching rule of the operator's own ruleset — `block all` at the
// top of pf.conf is how it is nearly always written — and that is a line
// in a file halite deliberately does not own.
func (pfProvider) SetDefault(c *exec.Context, direction, policy string) error {
	return fmt.Errorf(
		"pf has no default policy per direction: what happens to traffic no rule matched "+
			"is decided by the last matching rule of the ruleset, which is %s and which "+
			"halite does not own. Write it there — `block all` near the top of %s is the "+
			"usual spelling — and keep the per-rule permissions here. "+
			"(asked for %s %s)",
		PFConfPath, PFConfPath, policy, direction)
}

// Apply rewrites the anchor with the rule added or removed.
//
// The whole anchor, because that is pf's unit: there is no `pfctl` verb
// that adds one rule to a loaded set. Reading the current set, changing
// it, and loading it back is therefore the only shape available, and it
// is why a rule's text is its identity here — two rules that normalize
// to the same line are the same rule. Normalized rather than rendered
// because pf reprints a rule in its own spelling rather than the one it
// was given; see normalizePFRule.
func (p pfProvider) Apply(c *exec.Context, r firewallRule, remove, dryRun bool) (bool, error) {
	line, err := pfRule(r)
	if err != nil {
		return false, err
	}
	current, err := p.anchorRules(c)
	if err != nil {
		return false, err
	}

	key := normalizePFRule(line)
	want := make([]string, 0, len(current)+1)
	found := false
	for _, existing := range current {
		if normalizePFRule(existing) == key {
			found = true
			continue
		}
		want = append(want, existing)
	}
	switch {
	case remove && !found:
		return false, nil
	case !remove && found:
		return false, nil
	case !remove:
		want = append(want, line)
	}
	sort.Strings(want)

	if dryRun {
		return true, nil
	}
	if err := p.checkAnchorIsReferenced(c); err != nil {
		return false, err
	}
	if err := p.loadAnchor(c, want); err != nil {
		return false, err
	}
	return true, nil
}

// anchorRules reads what the anchor currently holds.
//
// An anchor that has never been loaded does not exist, and `pfctl -a`
// against it fails. That is an empty rule set rather than an error: a
// node where nothing has been applied yet is the ordinary starting
// state, and failing there would make the first `firewall.allowed` on
// every node fail.
func (pfProvider) anchorRules(c *exec.Context) ([]string, error) {
	res, err := c.Run(exec.Command{
		Argv:           []string{"pfctl", "-a", PFAnchor, "-s", "rules"},
		IgnoreExitCode: true,
	})
	if err != nil {
		return nil, err
	}
	if res.Code != 0 {
		out := res.Stderr + res.Stdout
		if strings.Contains(out, "No such file or directory") ||
			strings.Contains(out, "anchor") && strings.Contains(out, "not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("%s", firstLine(out))
	}
	var rules []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if line = strings.TrimSpace(strings.TrimRight(line, "\r")); line != "" {
			rules = append(rules, line)
		}
	}
	return rules, nil
}

// loadAnchor replaces the anchor's rules with these.
func (pfProvider) loadAnchor(c *exec.Context, rules []string) error {
	body := ""
	if len(rules) > 0 {
		body = strings.Join(rules, "\n") + "\n"
	}
	res, err := c.Run(exec.Command{
		Argv:           []string{"pfctl", "-a", PFAnchor, "-f", "-"},
		Stdin:          body,
		IgnoreExitCode: true,
	})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("loading the %s anchor: %s", PFAnchor, firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// checkAnchorIsReferenced refuses to load rules pf will never evaluate.
//
// This is the failure worth spending a file read on. `pfctl -a halite -f
// -` succeeds whether or not pf.conf contains `anchor "halite"`; without
// it the rules are loaded, `pfctl -a halite -s rules` lists them back,
// and no packet is ever matched against them. Everything reports
// correctly and the firewall does nothing — which is the exact shape of
// defect this project keeps finding, and the one place here it can be
// prevented rather than recorded.
func (pfProvider) checkAnchorIsReferenced(c *exec.Context) error {
	raw, err := os.ReadFile(PFConfPath)
	if err != nil {
		// Unreadable is not absent. A node whose pf.conf this process
		// cannot read is one where the check cannot be made, and
		// refusing there would stop a root-run state on a technicality.
		return nil
	}
	if pfConfReferencesAnchor(string(raw), PFAnchor) {
		return nil
	}
	return fmt.Errorf(
		"%s does not reference the %q anchor, so rules loaded into it would never be "+
			"evaluated: pf would report them and match nothing against them. Add a line "+
			"to %s where these rules should sit in the evaluation order — pf is "+
			"last-match-wins, so the position is yours to choose:\n"+
			"    anchor %q\n"+
			"then run this again",
		PFConfPath, PFAnchor, PFConfPath, PFAnchor)
}

// pfConfReferencesAnchor looks for the anchor being loaded, ignoring
// comments.
//
// `anchor "halite"` and `anchor halite` are both accepted by pf, and a
// nested path such as `anchor "halite/*"` references it too. A commented
// line does not, which is the case worth getting right: an operator who
// commented the anchor out to debug something would otherwise be told
// everything is fine.
func pfConfReferencesAnchor(conf, anchor string) bool {
	for _, line := range strings.Split(conf, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasPrefix(line, "anchor") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(line, "anchor"))
		name := strings.Trim(strings.Fields(rest + " ")[0], `"'`)
		name = strings.TrimSuffix(strings.TrimSuffix(name, "/*"), "/")
		if name == anchor {
			return true
		}
	}
	return false
}

// Reload re-reads the operator's ruleset.
//
// `pfctl -f /etc/pf.conf` rather than the anchor, because reloading is
// what an operator means after editing that file, and reloading only the
// anchor would re-apply what halite already knows and change nothing.
// States are not dropped: pf keeps its state table across a ruleset
// load, which is the whole reason this is safe to run on a machine
// somebody is connected to.
func (pfProvider) Reload(c *exec.Context) error {
	res, err := c.Run(exec.Command{
		Argv:           []string{"pfctl", "-f", PFConfPath},
		IgnoreExitCode: true,
	})
	if err != nil {
		return err
	}
	if res.Code != 0 {
		return fmt.Errorf("%s", firstLine(res.Stderr+res.Stdout))
	}
	return nil
}

// pfRule renders one rule as pf reads it.
//
//	pass in quick proto tcp from 10.0.0.0/8 to any port 22
//	block drop in quick proto tcp from any to any port 23
//	block return in quick from 192.0.2.4 to any
//
// `block drop` and `block return` are the difference between a port that
// hangs and one that refuses, which is what the module's `deny` and
// `reject` mean. `any` is written out rather than omitted because pf
// accepts both and the explicit form is what `pfctl -s rules` prints
// back — and this module compares rendered text with what pf printed, so
// the two spellings have to be the same one.
func pfRule(r firewallRule) (string, error) {
	var b strings.Builder
	switch strings.ToLower(r.Action) {
	case "allow", "accept", "pass":
		b.WriteString("pass")
	case "deny", "drop":
		b.WriteString("block drop")
	case "reject":
		b.WriteString("block return")
	default:
		return "", fmt.Errorf("%q is not an action pf takes; it is allow, deny or reject", r.Action)
	}

	direction := strings.ToLower(r.Direction)
	if direction == "" {
		direction = "in"
	}
	if direction != "in" && direction != "out" {
		return "", fmt.Errorf("%q is not a direction; it is in or out", r.Direction)
	}
	b.WriteString(" " + direction + " quick")

	if r.Protocol != "" {
		proto := strings.ToLower(r.Protocol)
		if proto != "tcp" && proto != "udp" {
			return "", fmt.Errorf("%q is not a protocol this module writes for pf; it is tcp or udp", r.Protocol)
		}
		b.WriteString(" proto " + proto)
	}

	b.WriteString(" from " + pfAddress(r.From))
	b.WriteString(" to " + pfAddress(r.To))

	if r.Port != "" {
		port, err := pfPort(r.Port)
		if err != nil {
			return "", err
		}
		b.WriteString(" port " + port)
	}
	return b.String(), nil
}

// normalizePFRule folds a rule into the one spelling both sides of the
// comparison in Apply can be written in.
//
// `pfRule` renders what to load; `pfctl -a halite -s rules` prints what
// is loaded, and pf does not print back the text it was given. It
// reprints from its own parsed form, which adds the spellings this
// undoes. Comparing the two raw was this provider's first defect found
// on a real pf: mail.edlitmus.info loaded two rules, matched neither of
// them on the next run, and reported both as added again on every run.
//
//	loaded:  block drop in quick proto tcp from any to any port 9999
//	printed: block drop in quick proto tcp from any to any port = 9999
//	loaded:  pass in quick proto tcp from any to any port 9998
//	printed: pass ... port = 9998 flags S/SA keep state
//
// The port operator is pf writing the comparison it parsed. The flags
// and the state tracking are pf's defaults for a `pass` rule, applied
// whether or not they were asked for and printed as though they were.
//
// `from any to any` collapses to `all` because pf prints one or the
// other depending on what else the rule constrains, and folding both
// sides to a single token means neither has to predict which.
//
// Two known gaps, both of which need more than a spelling change. A
// port list renders as one rule and pf expands it into one rule per
// port, so `{ 80, 443 }` cannot match whatever it is compared against;
// that wants the list expanded at render time. And a rule carrying pf
// options this does not write — `modulate state`, an interface — would
// print back with them.
func normalizePFRule(rule string) string {
	rule = strings.Join(strings.Fields(rule), " ")
	rule = strings.ReplaceAll(rule, "port = ", "port ")
	rule = strings.ReplaceAll(rule, " from any to any", " all")
	for _, suffix := range []string{" flags S/SA keep state", " keep state"} {
		if strings.HasSuffix(rule, suffix) {
			return strings.TrimSuffix(rule, suffix)
		}
	}
	return rule
}

// pfAddress writes an address the way pf does, with `any` for empty.
func pfAddress(a string) string {
	if a = strings.TrimSpace(a); a == "" || strings.EqualFold(a, "any") {
		return "any"
	}
	return a
}

// pfPort renders a port, a range, or a list.
//
// The module's range separator is a colon, which is ufw's and iptables';
// pf writes a range as `2000:3000` too, so a range passes through. A
// list is `{ 80, 443 }` in pf and `80,443` in the module, which is the
// one spelling that has to be translated.
func pfPort(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.ContainsAny(p, " \t\n") {
		return "", fmt.Errorf("%q is not a port, a range or a list", p)
	}
	if !strings.Contains(p, ",") {
		return p, nil
	}
	parts := strings.Split(p, ",")
	for i, part := range parts {
		if parts[i] = strings.TrimSpace(part); parts[i] == "" {
			return "", fmt.Errorf("%q has an empty entry in its port list", p)
		}
	}
	return "{ " + strings.Join(parts, ", ") + " }", nil
}
