package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// A real `nft -j list ruleset`, captured from nft 1.0.9 inside a network
// namespace: one inet table, three chains (two base, one plain), and
// three rules, two of them carrying a halite comment.
const nftListSample = `{"nftables": [{"metainfo": {"version": "1.0.9", "release_name": "Old Doc Yak #3", "json_schema_version": 1}}, {"table": {"family": "inet", "name": "filter", "handle": 1}}, {"chain": {"family": "inet", "table": "filter", "name": "input", "handle": 1, "type": "filter", "hook": "input", "prio": 0, "policy": "drop"}}, {"chain": {"family": "inet", "table": "filter", "name": "forward", "handle": 2, "type": "filter", "hook": "forward", "prio": 0, "policy": "accept"}}, {"chain": {"family": "inet", "table": "filter", "name": "guard", "handle": 3}}, {"rule": {"family": "inet", "table": "filter", "chain": "input", "handle": 4, "comment": "halite:conntrack", "expr": [{"match": {"op": "in", "left": {"ct": {"key": "state"}}, "right": ["established", "related"]}}, {"accept": null}]}}, {"rule": {"family": "inet", "table": "filter", "chain": "input", "handle": 5, "comment": "halite:ssh", "expr": [{"match": {"op": "==", "left": {"payload": {"protocol": "tcp", "field": "dport"}}, "right": 22}}, {"accept": null}]}}, {"rule": {"family": "inet", "table": "filter", "chain": "input", "handle": 6, "expr": [{"jump": {"target": "guard"}}]}}]}`

func TestNFTParsesListJSON(t *testing.T) {
	rs, err := nftParseList(nftListSample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !nftFindTable(rs, "inet", "filter") {
		t.Error("the inet filter table was not parsed")
	}
	input := nftFindChain(rs, "inet", "filter", "input")
	if input == nil || !input.Base() {
		t.Fatalf("input chain parsed as %+v", input)
	}
	if input.Policy != "drop" || input.Hook != "input" || input.Prio != "0" {
		t.Errorf("input chain: policy=%q hook=%q prio=%q", input.Policy, input.Hook, input.Prio)
	}
	if guard := nftFindChain(rs, "inet", "filter", "guard"); guard == nil || guard.Base() {
		t.Errorf("guard chain should be a plain chain: %+v", guard)
	}
	if ru := nftFindRuleByComment(rs, "inet", "filter", "input", "halite:ssh"); ru == nil || ru.Handle != 5 {
		t.Errorf("halite:ssh rule parsed as %+v", ru)
	}
	if n := nftRuleCount(rs, "inet", "filter", "input"); n != 3 {
		t.Errorf("input chain rule count = %d, want 3", n)
	}
	// A rule with no comment is found by nothing, which is the point of
	// the comment-as-identity rule.
	if ru := nftFindRuleByComment(rs, "inet", "filter", "input", ""); ru != nil {
		t.Errorf("an empty comment matched a rule: %+v", ru)
	}
}

func TestNFTFamilyValidation(t *testing.T) {
	for _, ok := range []string{"ip", "ip6", "inet", "arp", "bridge", "netdev"} {
		if err := nftCheckFamily(ok); err != nil {
			t.Errorf("family %q rejected: %v", ok, err)
		}
	}
	if err := nftCheckFamily("ipv4"); err == nil {
		t.Error("`ipv4` (an iptables spelling) was accepted as an nft family")
	}
}

func TestNFTRuleLine(t *testing.T) {
	got := nftRuleLine("add", "inet", "filter", "input", "tcp dport 22 accept", "halite:ssh", -1)
	want := "add rule inet filter input tcp dport 22 accept comment \"halite:ssh\"\n"
	if got != want {
		t.Errorf("add line =\n  %q\nwant\n  %q", got, want)
	}
	got = nftRuleLine("insert", "inet", "filter", "input", "ip saddr 10.0.0.0/8 drop", "halite:block", 2)
	want = "insert rule inet filter input index 2 ip saddr 10.0.0.0/8 drop comment \"halite:block\"\n"
	if got != want {
		t.Errorf("insert line =\n  %q\nwant\n  %q", got, want)
	}
	// No comment: no trailing comment clause.
	got = nftRuleLine("add", "ip", "nat", "post", "masquerade", "", -1)
	if strings.Contains(got, "comment") {
		t.Errorf("a rule with no comment got a comment clause: %q", got)
	}
}

func TestNFTChainSpec(t *testing.T) {
	spec, err := nftChainSpec(value.MapOf("hook", "input", "type", "filter", "priority", "0", "policy", "drop"))
	if err != nil || spec != "{ type filter hook input priority 0 ; policy drop ; }" {
		t.Errorf("base chain spec = %q, %v", spec, err)
	}
	// No hook: a plain chain, no body.
	spec, err = nftChainSpec(value.MapOf("policy", "accept"))
	if err != nil || spec != "" {
		t.Errorf("plain chain spec = %q, %v", spec, err)
	}
	if _, err := nftChainSpec(value.MapOf("hook", "input", "policy", "bogus")); err == nil {
		t.Error("a bogus base-chain policy was accepted")
	}
}

// ---- behaviour, scripted runner ----

func nftCtx(responses map[string]exec.Result) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
		Lookup: func(name string) string { return "/usr/sbin/" + name },
	}
}

func nftScriptsRun(c *exec.Context) []string {
	var out []string
	for _, cmd := range c.Runner.(*exec.RecordingRunner).Ran {
		if len(cmd.Argv) >= 2 && cmd.Argv[0] == "nft" && cmd.Argv[1] == "-f" {
			out = append(out, strings.TrimSpace(cmd.Stdin))
		}
	}
	return out
}

// append is a no-op when a rule with that comment is already in the chain.
func TestNFTAppendIdempotentOnComment(t *testing.T) {
	listChain := (exec.Command{Argv: []string{"nft", "-j", "list", "chain", "inet", "filter", "input"}}).String()
	responses := map[string]exec.Result{listChain: {Stdout: nftListSample}}

	// halite:ssh is present -> no change, no script run.
	c := nftCtx(responses)
	out, err := nftAddRule(c, value.MapOf(
		"table", "filter", "chain", "input", "rule", "tcp dport 22 accept", "comment", "halite:ssh"), "append")
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("append of a rule whose comment is already present reported a change: %v", out)
	}
	if s := nftScriptsRun(c); len(s) != 0 {
		t.Errorf("append ran nft -f - when there was nothing to do: %v", s)
	}

	// A comment not in the chain -> the rule is added.
	c2 := nftCtx(responses)
	out, err = nftAddRule(c2, value.MapOf(
		"table", "filter", "chain", "input", "rule", "udp dport 53 accept", "comment", "halite:dns"), "append")
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("append of a new comment reported no change: %v", out)
	}
	scripts := nftScriptsRun(c2)
	if len(scripts) != 1 || !strings.Contains(scripts[0], `add rule inet filter input udp dport 53 accept comment "halite:dns"`) {
		t.Errorf("append ran: %v", scripts)
	}

	// No comment -> appended, but the result carries a warning that it
	// was not idempotent.
	c3 := nftCtx(responses)
	out, err = nftAddRule(c3, value.MapOf(
		"table", "filter", "chain", "input", "rule", "tcp dport 8080 accept"), "append")
	if err != nil {
		t.Fatal(err)
	}
	wv, _ := out.(*value.Map).GetString("warnings")
	list, _ := wv.([]any)
	if len(list) == 0 || !strings.Contains(value.KeyString(list[0]), "no comment") {
		t.Errorf("a comment-less append did not warn: %v", out)
	}

	// Test mode predicts and runs nothing.
	c4 := nftCtx(responses)
	c4.Test = true
	_, _ = nftAddRule(c4, value.MapOf(
		"table", "filter", "chain", "input", "rule", "udp dport 53 accept", "comment", "halite:dns"), "append")
	if s := nftScriptsRun(c4); len(s) != 0 {
		t.Errorf("a test run executed a script: %v", s)
	}
}

// delete finds a rule by its comment and removes it by handle.
func TestNFTDeleteByComment(t *testing.T) {
	listChain := (exec.Command{Argv: []string{"nft", "-j", "list", "chain", "inet", "filter", "input"}}).String()
	c := nftCtx(map[string]exec.Result{listChain: {Stdout: nftListSample}})
	out, err := nftDeleteRule(c, value.MapOf("table", "filter", "chain", "input", "comment", "halite:ssh"))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("delete of a present rule reported no change: %v", out)
	}
	scripts := nftScriptsRun(c)
	if len(scripts) != 1 || scripts[0] != "delete rule inet filter input handle 5" {
		t.Errorf("delete ran: %v", scripts)
	}

	// A comment that matches nothing is a no-op.
	c2 := nftCtx(map[string]exec.Result{listChain: {Stdout: nftListSample}})
	out, _ = nftDeleteRule(c2, value.MapOf("table", "filter", "chain", "input", "comment", "halite:nope"))
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("delete of an absent comment reported a change: %v", out)
	}
}

// flush refuses the destructive scopes without force.
func TestNFTFlushGuards(t *testing.T) {
	listRuleset := (exec.Command{Argv: []string{"nft", "-j", "list", "ruleset"}}).String()
	responses := map[string]exec.Result{listRuleset: {Stdout: nftListSample}}

	// Whole ruleset, no force.
	c := nftCtx(responses)
	if _, err := nftFlushFn(c, value.NewMap(0)); err == nil {
		t.Error("flush ruleset was allowed without force")
	} else if !strings.Contains(err.Error(), "every table") {
		t.Errorf("the refusal does not explain the scope: %v", err)
	}

	// Whole table, no force.
	c2 := nftCtx(responses)
	if _, err := nftFlushFn(c2, value.MapOf("table", "filter")); err == nil {
		t.Error("flush table was allowed without force")
	}

	// A base chain with policy drop, no force (input is drop in the sample).
	c3 := nftCtx(responses)
	if _, err := nftFlushFn(c3, value.MapOf("table", "filter", "chain", "input")); err == nil {
		t.Error("flushing a drop-policy base chain was allowed without force")
	} else if !strings.Contains(err.Error(), "letting traffic through") {
		t.Errorf("the refusal does not explain the danger: %v", err)
	}

	// forward is a base chain with policy accept -- flushing it is fine
	// (it has no rules in the sample, so it is a no-op, but not refused).
	c4 := nftCtx(responses)
	if _, err := nftFlushFn(c4, value.MapOf("table", "filter", "chain", "forward")); err != nil {
		t.Errorf("flushing an accept-policy base chain was refused: %v", err)
	}

	// A forced ruleset flush is allowed.
	c5 := nftCtx(responses)
	c5.Test = true
	if _, err := nftFlushFn(c5, value.MapOf("force", true)); err != nil {
		t.Errorf("a forced ruleset flush was refused: %v", err)
	}
}

// The rule states refuse to run without a comment, because the comment
// is the only thing that makes them idempotent.
func TestNFTRuleStateRequiresAComment(t *testing.T) {
	c := nftCtx(nil)
	res, err := nftRuleState(c, value.MapOf("table", "filter", "chain", "input", "rule", "tcp dport 22 accept"), "append")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed() || !strings.Contains(res.Comment, "identity") {
		t.Errorf("a comment-less rule state should fail explaining why: %+v", res)
	}
}

func TestNFTRefusesWithoutTheTool(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if _, err := nftListRaw(c, "ruleset"); err == nil || !strings.Contains(err.Error(), "nftables") {
		t.Errorf("the refusal does not name the package: %v", err)
	}
}

func TestNFTRefusesOnANonLinuxPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this is about the platforms nftables is not declared for")
	}
	_, err := New().Exec.Call(&exec.Context{}, "nftables.list", value.NewMap(0))
	if err == nil || !strings.Contains(err.Error(), "this node is "+runtime.GOOS) {
		t.Errorf("nftables.list did not refuse by platform: %v", err)
	}
}
