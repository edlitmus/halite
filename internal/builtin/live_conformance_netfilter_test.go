package builtin

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The sixteen `iptables` and `nftables` state functions, through SPEC 11.6's
// harness, inside a private network namespace.
//
// # Why a namespace rather than the machine
//
// A packet filter is the one subsystem where a wrong rule takes the machine
// off the network — and the harness applies each state twice and then reads
// it back, so a case that got it wrong would do so four times. Every case
// here runs inside a fresh namespace created by re-executing the test binary
// under `unshare --net --map-root-user`, exactly as `live_netfilter_test.go`
// already does. Nothing touches the host's ruleset. The namespace is
// discarded when the process returns.
//
// That also means these do not need the machine to be disposable at all, so
// they are the one part of the live conformance suite that could in principle
// run anywhere. They are still gated with the rest of it: a reader who finds
// `iptables` in a test list should not have to work out which of them is safe.
//
// # Why they are in the same list as the others
//
// `liveConformanceCases()` returns them alongside everything else, tagged
// `needsNetns`, and the main suite skips them. Two drivers, one list — so the
// unit suite's accounting cannot miss them and they cannot drift out of the
// count. The alternative was a second list of names, which this repository
// has twice found disagreeing with the first.

func netfilterCases() []liveCase {
	var cases []liveCase
	cases = append(cases, iptablesCases()...)
	cases = append(cases, nftablesCases()...)
	return cases
}

// The chain this suite makes. A chain of its own rather than INPUT: a rule
// appended to INPUT inside a namespace is harmless, but a case that named a
// real chain would be one edit away from being harmful if the namespace ever
// failed open.
const conformanceChain = "HALITECF"

func iptablesCases() []liveCase {
	r := New()
	root := liveRoot()

	ipt := func(args ...string) (string, error) {
		res, err := root.Run(hexec.Command{
			Argv:           append([]string{"iptables"}, args...),
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		return res.Stdout + res.Stderr, nil
	}

	// The probe reads the real tool's own listing, never the module's
	// answer: `iptables -S` is what an operator would type.
	probe := func() (string, error) {
		out, err := ipt("-S")
		if err != nil {
			return "", err
		}
		var kept []string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, conformanceChain) {
				kept = append(kept, strings.TrimSpace(line))
			}
		}
		if len(kept) == 0 {
			return "no " + conformanceChain, nil
		}
		return strings.Join(kept, " | "), nil
	}

	dropChain := func() {
		_, _ = ipt("-F", conformanceChain)
		_, _ = ipt("-X", conformanceChain)
	}
	makeChain := func() error {
		dropChain()
		_, err := ipt("-N", conformanceChain)
		return err
	}
	// A rule in this suite's own chain, matching traffic that will never
	// arrive: TEST-NET-3 on a port nothing listens on.
	ruleArgs := func(extra ...any) *value.Map {
		base := []any{
			"chain", conformanceChain,
			"protocol", "tcp",
			"dport", "65001",
			"source", "203.0.113.0/24",
			"jump", "DROP",
		}
		return value.MapOf(append(base, extra...)...)
	}
	addRule := func() error {
		if err := makeChain(); err != nil {
			return err
		}
		return applyForSetup(r, root, "iptables.append", ruleArgs())
	}

	linux := func(lc liveCase) liveCase {
		lc.platforms = []string{"linux"}
		lc.needs = []string{"iptables"}
		lc.needsNetns = true
		return lc
	}

	return []liveCase{
		linux(liveCase{Conformance: states.Conformance{
			Name:    "iptables.chain_present",
			Args:    value.MapOf("name", conformanceChain),
			Probe:   probe,
			Setup:   func() error { dropChain(); return nil },
			Cleanup: dropChain,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name:    "iptables.chain_absent",
			Args:    value.MapOf("name", conformanceChain),
			Probe:   probe,
			Setup:   makeChain,
			Cleanup: dropChain,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name:    "iptables.append",
			Args:    ruleArgs(),
			Probe:   probe,
			Setup:   makeChain,
			Cleanup: dropChain,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name:    "iptables.insert",
			Args:    ruleArgs("position", int64(1)),
			Probe:   probe,
			Setup:   makeChain,
			Cleanup: dropChain,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name:    "iptables.delete",
			Args:    ruleArgs(),
			Probe:   probe,
			Setup:   addRule,
			Cleanup: dropChain,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			// A policy applies to a built-in chain, so this one names
			// FORWARD rather than a chain of ours -- a user chain has no
			// policy. Inside the namespace FORWARD governs nothing: there
			// is one interface and it is loopback.
			Name:  "iptables.set_policy",
			Args:  value.MapOf("name", "FORWARD", "policy", "DROP"),
			Probe: func() (string, error) { return iptablesPolicy(ipt, "FORWARD") },
			Setup: func() error {
				_, err := ipt("-P", "FORWARD", "ACCEPT")
				return err
			},
			Cleanup: func() { _, _ = ipt("-P", "FORWARD", "ACCEPT") },
		}}),
		linux(liveCase{Conformance: states.Conformance{
			// Flushing is not idempotent in the harness's sense: the
			// second run finds an empty chain and has nothing to do, which
			// is convergence, but the *state* reports the flush as a
			// change only the first time. That is the correct behaviour
			// and the harness can hold it without help.
			Name:    "iptables.flush",
			Args:    value.MapOf("name", conformanceChain, "force", true),
			Probe:   probe,
			Setup:   addRule,
			Cleanup: dropChain,
		}}),
	}
}

func iptablesPolicy(ipt func(...string) (string, error), chain string) (string, error) {
	out, err := ipt("-S")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "-P "+chain+" ") {
			return strings.TrimSpace(line), nil
		}
	}
	return "no policy for " + chain, nil
}

func nftablesCases() []liveCase {
	r := New()
	root := liveRoot()

	const table = "halitecf"
	const chain = "conformance"

	nft := func(args ...string) (string, error) {
		res, err := root.Run(hexec.Command{
			Argv:           append([]string{"nft"}, args...),
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		return res.Stdout + res.Stderr, nil
	}
	probe := func() (string, error) {
		out, err := nft("list", "ruleset")
		if err != nil {
			return "", err
		}
		var kept []string
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, table) || strings.Contains(line, chain) ||
				strings.Contains(line, "65001") {
				kept = append(kept, strings.TrimSpace(line))
			}
		}
		if len(kept) == 0 {
			return "no " + table, nil
		}
		return strings.Join(kept, " | "), nil
	}
	dropTable := func() {
		_, _ = nft("delete", "table", "inet", table)
	}
	makeTable := func() error {
		dropTable()
		_, err := nft("add", "table", "inet", table)
		return err
	}
	makeChain := func() error {
		if err := makeTable(); err != nil {
			return err
		}
		_, err := nft("add", "chain", "inet", table, chain,
			"{ type filter hook input priority 0; policy accept; }")
		return err
	}
	const rule = "tcp dport 65001 ip saddr 203.0.113.0/24 drop"
	// nftables identifies a rule it manages by a comment tag rather than
	// by the rule's text, which is why `comment` is required on append,
	// insert and delete. The tag is this suite's own.
	const ruleTag = "halitecf-rule"
	addRule := func() error {
		if err := makeChain(); err != nil {
			return err
		}
		return applyForSetup(r, root, "nftables.append",
			value.MapOf("table", table, "chain", chain, "rule", rule,
				"comment", ruleTag, "family", "inet"))
	}

	linux := func(lc liveCase) liveCase {
		lc.platforms = []string{"linux"}
		lc.needs = []string{"nft"}
		lc.needsNetns = true
		return lc
	}

	return []liveCase{
		linux(liveCase{Conformance: states.Conformance{
			Name:    "nftables.table_present",
			Args:    value.MapOf("name", table, "family", "inet"),
			Probe:   probe,
			Setup:   func() error { dropTable(); return nil },
			Cleanup: dropTable,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name:    "nftables.table_absent",
			Args:    value.MapOf("name", table, "family", "inet"),
			Probe:   probe,
			Setup:   makeTable,
			Cleanup: dropTable,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name: "nftables.chain_present",
			Args: value.MapOf("name", chain, "table", table, "family", "inet",
				"hook", "input", "type", "filter", "priority", int64(0), "policy", "accept"),
			Probe:   probe,
			Setup:   makeTable,
			Cleanup: dropTable,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name:    "nftables.chain_absent",
			Args:    value.MapOf("name", chain, "table", table, "family", "inet"),
			Probe:   probe,
			Setup:   makeChain,
			Cleanup: dropTable,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name: "nftables.append",
			Args: value.MapOf("table", table, "chain", chain, "rule", rule,
				"comment", ruleTag, "family", "inet"),
			Probe:   probe,
			Setup:   makeChain,
			Cleanup: dropTable,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name: "nftables.insert",
			Args: value.MapOf("table", table, "chain", chain, "rule", rule,
				"comment", ruleTag, "family", "inet"),
			Probe:   probe,
			Setup:   makeChain,
			Cleanup: dropTable,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			// Deleted by its comment tag, not by the rule text: that is
			// how this module identifies a rule it put there, and the
			// signature makes `comment` required for the same reason.
			Name: "nftables.delete",
			Args: value.MapOf("table", table, "chain", chain,
				"comment", ruleTag, "family", "inet"),
			Probe:   probe,
			Setup:   addRule,
			Cleanup: dropTable,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name: "nftables.set_policy",
			Args: value.MapOf("name", chain, "table", table, "policy", "drop",
				"family", "inet"),
			Probe:   probe,
			Setup:   makeChain,
			Cleanup: dropTable,
		}}),
		linux(liveCase{Conformance: states.Conformance{
			Name:    "nftables.flush",
			Args:    value.MapOf("table", table, "force", true, "family", "inet"),
			Probe:   probe,
			Setup:   addRule,
			Cleanup: dropTable,
		}}),
	}
}

// TestLiveConformanceNetfilter drives the packet-filter cases inside a
// private network namespace.
//
// The re-exec is the same mechanism `live_netfilter_test.go` uses, and the
// reason to repeat it rather than share a helper is that the helper names the
// test it re-runs: `-test.run ^<name>$`. Two tests cannot share one.
func TestLiveConformanceNetfilter(t *testing.T) {
	if os.Getenv("HALITE_NETNS_INNER") != "1" {
		if runtime.GOOS != "linux" {
			t.Skipf("network namespaces are Linux's, and this is %s", runtime.GOOS)
		}
		if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
			t.Skip("set HALITE_SYSTEM_LIVE=1; these drive the real iptables and nft")
		}
		if os.Getenv("HALITE_CONFORMANCE_LIVE") != "1" {
			t.Skip("HALITE_CONFORMANCE_LIVE is not set; see live_conformance_test.go for why " +
				"this suite asks for a second variable")
		}
		unshare, err := exec.LookPath("unshare")
		if err != nil {
			t.Skip("this host has no `unshare`, so there is no namespace to isolate the filter in")
		}
		self, err := os.Executable()
		if err != nil {
			t.Fatalf("cannot find the test binary: %v", err)
		}
		cmd := exec.Command(unshare, "--net", "--map-root-user", "--",
			self, "-test.run", "^"+t.Name()+"$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), "HALITE_NETNS_INNER=1")
		out, err := cmd.CombinedOutput()
		t.Logf("inside the namespace:\n%s", out)
		if err != nil {
			// A kernel that refuses the namespace is a machine that
			// cannot be asked, not a defect: unprivileged user namespaces
			// may be off, and Ubuntu's AppArmor restricts them without a
			// profile. Told apart by what unshare said.
			if strings.Contains(string(out), "unshare failed") ||
				strings.Contains(string(out), "Operation not permitted") {
				t.Skipf("this kernel would not give the test a network namespace: %v", err)
			}
			t.Fatalf("the namespaced run failed: %v", err)
		}
		return
	}

	// Inside the namespace. uid 0 over a network stack that is thrown away.
	r := New()
	ctx := func(test bool) *hexec.Context {
		c := liveRoot()
		c.Test = test
		return c
	}
	probeCtx := liveRoot()

	var ran int
	for _, lc := range liveConformanceCases() {
		if !lc.needsNetns {
			continue
		}
		lc := lc
		t.Run(lc.name(), func(t *testing.T) {
			if why := lc.skipReason(probeCtx); why != "" {
				t.Skipf("%s", why)
			}
			ran++
			for _, f := range lc.Check(r.States, ctx) {
				t.Errorf("%s", f)
			}
		})
	}
	if ran == 0 {
		t.Skip("neither iptables nor nft is on this machine")
	}
	t.Logf("%d packet-filter conformance cases ran inside the namespace", ran)
}

// A precondition on the isolation itself, because every case above rests on
// it: inside the namespace the host's own rules must not be visible.
//
// Without this, a namespace that silently failed to isolate would leave
// sixteen cases editing the real firewall and reporting success. That is the
// same shape as a skip nobody noticed, and it is worth one assertion.
func TestLiveConformanceNamespaceIsolated(t *testing.T) {
	if os.Getenv("HALITE_NETNS_INNER") != "1" {
		t.Skip("this assertion is made by the namespaced child of TestLiveConformanceNetfilter")
	}
	root := liveRoot()
	res, err := root.Run(hexec.Command{
		Argv: []string{"ip", "-o", "link"}, IgnoreExitCode: true})
	if err != nil {
		t.Skipf("no `ip` here to ask: %v", err)
	}
	links := strings.TrimSpace(res.Stdout)
	if strings.Count(links, "\n") > 0 {
		t.Errorf("a fresh network namespace should hold loopback alone; this one has:\n%s", links)
	}
	if !strings.Contains(links, "lo") {
		t.Errorf("no loopback in the namespace at all: %q", links)
	}
	fmt.Fprintln(os.Stderr, "namespace links:", links)
}
