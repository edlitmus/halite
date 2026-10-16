package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// The lines below stand in for `pfctl -a halite -s rules`.
//
// Their *shape* is captured: `port = N`, and `flags S/SA keep state` on
// a tcp pass, are what mail.edlitmus.info printed for the rules in
// TestPFNormalizeMatchesWhatPFPrints (DIVERGENCE 5.31). The particular
// lines are **synthetic** — no real pf printed a rule with these
// addresses — and so is the assumption every test here rests on, that
// `pfctl -s rules` lists an anchor in the order it was loaded. That is
// pf's documented evaluation order and it is what the provider relies
// on, but nothing in this repository has watched a real pf print it.
const (
	pfPrintedAllowBastion = "pass in quick proto tcp from 10.0.0.5 to any port = 22 flags S/SA keep state"
	pfPrintedDenyTen      = "block drop in quick from 10.0.0.0/8 to any"
	pfPrintedAllowHTTP    = "pass in quick proto tcp from any to any port = 80 flags S/SA keep state"
)

var (
	ruleAllowBastion = firewallRule{Action: "allow", From: "10.0.0.5", Port: "22", Protocol: "tcp"}
	ruleDenyTen      = firewallRule{Action: "deny", From: "10.0.0.0/8"}
	ruleAllowHTTP    = firewallRule{Action: "allow", Port: "80", Protocol: "tcp"}
)

// pfLoaded applies one rule against an anchor that prints `printed`, and
// returns the lines loaded — nil when nothing was loaded.
func pfLoaded(t *testing.T, printed []string, r firewallRule, remove bool) []string {
	t.Helper()
	pfConf(t, "anchor \"halite\"\n")
	out := ""
	if len(printed) > 0 {
		out = strings.Join(printed, "\n") + "\n"
	}
	c, runner := pfFixture(t, map[string]exec.Result{
		"pfctl -a halite -s rules": {Stdout: out},
	})
	if _, err := pfp.Apply(c, r, remove, false); err != nil {
		t.Fatal(err)
	}
	var loaded []string
	loads := 0
	for _, cmd := range runner.Ran {
		if !strings.Contains(cmd.String(), "-f -") {
			continue
		}
		loads++
		loaded = strings.Split(strings.TrimSuffix(cmd.Stdin, "\n"), "\n")
	}
	if loads > 1 {
		t.Fatalf("one Apply loaded the anchor %d times: %v", loads, runner.RanCommands())
	}
	return loaded
}

func pfRendered(t *testing.T, r firewallRule) string {
	t.Helper()
	line, err := pfRule(r)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func samePFRules(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if normalizePFRule(a[i]) != normalizePFRule(b[i]) {
			return false
		}
	}
	return true
}

// An allow carved out of a wider deny matches, when it is declared first.
//
// This is the defect. Apply used to sort the anchor, and `block drop` and
// `block return` sort before `pass`, so every deny landed above every
// allow; with `quick`, the first match is the only one that counts, and
// the bastion's ssh was refused by the /8 above it however the states
// were written. Declared allow-then-deny, the anchor has to say
// allow-then-deny.
func TestPFKeepsAnAllowDeclaredBeforeAnOverlappingDenyAboveIt(t *testing.T) {
	// The first state runs against an empty anchor.
	first := pfLoaded(t, nil, ruleAllowBastion, false)
	if want := []string{pfRendered(t, ruleAllowBastion)}; !samePFRules(first, want) {
		t.Fatalf("the first rule loaded as %q, want %q", first, want)
	}

	// The second runs against what pf printed back after the first.
	second := pfLoaded(t, []string{pfPrintedAllowBastion}, ruleDenyTen, false)
	want := []string{pfPrintedAllowBastion, pfRendered(t, ruleDenyTen)}
	if !samePFRules(second, want) {
		t.Errorf("declared allow then deny, the anchor was loaded as\n  %s\nwant\n  %s\n"+
			"with `quick` the first match wins, so a deny above the allow means the allow "+
			"never matches", strings.Join(second, "\n  "), strings.Join(want, "\n  "))
	}
}

// And the other way round is the other way round.
//
// Not because block-before-pass is right, but because it is what was
// declared. Without this a fix that put every `pass` first would pass
// the test above and be the same defect facing the other way — one that
// fails open, which the sort at least never did.
func TestPFKeepsADenyDeclaredBeforeAnOverlappingAllowAboveIt(t *testing.T) {
	got := pfLoaded(t, []string{pfPrintedDenyTen}, ruleAllowBastion, false)
	want := []string{pfPrintedDenyTen, pfRendered(t, ruleAllowBastion)}
	if !samePFRules(got, want) {
		t.Errorf("declared deny then allow, the anchor was loaded as\n  %s\nwant\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// Removing a rule leaves the others where they were.
//
// The rest of the anchor is reloaded on a removal, and a reload that
// reordered what was left would change which of them wins as a side
// effect of deleting something unrelated.
func TestPFRemovingARuleKeepsTheOthersInOrder(t *testing.T) {
	printed := []string{pfPrintedAllowBastion, pfPrintedDenyTen, pfPrintedAllowHTTP}
	got := pfLoaded(t, printed, ruleAllowHTTP, true)
	want := []string{pfPrintedAllowBastion, pfPrintedDenyTen}
	if !samePFRules(got, want) {
		t.Errorf("removing the http rule loaded\n  %s\nwant\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// An anchor that already holds the declared rules is not reloaded, in
// either order.
//
// The first half is idempotence. The second half is the limit of what a
// per-rule state can know, written down so that it is a decision and not
// a surprise: each state sees its own rule and none of the others, so a
// rule that is present is converged wherever it sits. Swapping two
// states in an SLS file does not reorder an anchor that is already
// loaded — ufw behaves the same way — and an anchor written by a build
// that sorted keeps the sorted order until it is flushed (DIVERGENCE
// 5.315). If this half starts failing, something has begun reordering
// loaded rules, and that wants a ledger entry of its own.
func TestPFDoesNotReloadAnAnchorThatHoldsTheRule(t *testing.T) {
	for _, tc := range []struct {
		what    string
		printed []string
	}{
		{"in the declared order", []string{pfPrintedAllowBastion, pfPrintedDenyTen}},
		{"in the order a sorting build left", []string{pfPrintedDenyTen, pfPrintedAllowBastion}},
	} {
		for _, r := range []firewallRule{ruleAllowBastion, ruleDenyTen} {
			if got := pfLoaded(t, tc.printed, r, false); got != nil {
				t.Errorf("%s: applying %s, which is loaded, reloaded the anchor as %q",
					tc.what, r.describe(), got)
			}
		}
	}
}
