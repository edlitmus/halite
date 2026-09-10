package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The `nftables` module, driven against a real nft inside a throwaway
// network namespace: tables and chains created and removed, a base
// chain's policy set, rules added idempotently by their comment,
// removed by comment, the flush guards shown to refuse, and the ruleset
// saved to a file. Every result is checked against a fresh `nft -j
// list`.
func TestLiveNFTablesManagesARealRuleset(t *testing.T) {
	netnsReexec(t)
	r := New()
	c := &exec.Context{}

	call := func(fn string, kv ...any) *value.Map {
		t.Helper()
		args := value.NewMap(len(kv) / 2)
		for i := 0; i+1 < len(kv); i += 2 {
			args.Set(kv[i].(string), kv[i+1])
		}
		out, err := r.Exec.Call(c, fn, args)
		if err != nil {
			t.Fatalf("%s(%v): %v", fn, kv, err)
		}
		m, _ := out.(*value.Map)
		return m
	}
	list := func() *nftRuleset {
		t.Helper()
		rs, err := nftListRaw(c, "ruleset")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return rs
	}
	changed := func(m *value.Map) bool {
		v, _ := m.GetString("changed")
		return v == true
	}

	if v := call("nftables.version"); func() bool { s, _ := v.GetString("version"); return s == "" }() {
		t.Errorf("version reported nothing: %v", v)
	}
	if rs := list(); len(rs.Tables) != 0 || len(rs.Rules) != 0 {
		t.Fatalf("a fresh namespace already has a ruleset: %+v", rs)
	}

	// table + chain.
	if !changed(call("nftables.table_present", "name", "filter", "family", "inet")) {
		t.Fatal("table_present reported no change")
	}
	if changed(call("nftables.table_present", "name", "filter", "family", "inet")) {
		t.Error("a second table_present reported a change")
	}
	call("nftables.chain_present", "name", "input", "table", "filter",
		"hook", "input", "priority", "0", "policy", "accept")
	if ch := nftFindChain(list(), "inet", "filter", "input"); ch == nil || !ch.Base() || ch.Policy != "accept" {
		t.Fatalf("input chain after chain_present: %+v", ch)
	}
	if changed(call("nftables.chain_present", "name", "input", "table", "filter",
		"hook", "input", "priority", "0", "policy", "accept")) {
		t.Error("a second chain_present reported a change")
	}

	// set_policy updates the base chain in place.
	if !changed(call("nftables.set_policy", "name", "input", "table", "filter", "policy", "drop")) {
		t.Error("set_policy reported no change")
	}
	if ch := nftFindChain(list(), "inet", "filter", "input"); ch.Policy != "drop" {
		t.Errorf("policy after set_policy = %q", ch.Policy)
	}
	if changed(call("nftables.set_policy", "name", "input", "table", "filter", "policy", "drop")) {
		t.Error("a second set_policy reported a change")
	}
	// Set it back to accept so the rest of the test can flush freely.
	call("nftables.set_policy", "name", "input", "table", "filter", "policy", "accept")

	// a plain chain.
	call("nftables.chain_present", "name", "guard", "table", "filter")
	if ch := nftFindChain(list(), "inet", "filter", "guard"); ch == nil || ch.Base() {
		t.Errorf("guard should be a plain chain: %+v", ch)
	}

	// a rule, idempotent on its comment.
	if !changed(call("nftables.append", "table", "filter", "chain", "input",
		"rule", "tcp dport 22 accept", "comment", "halite:ssh")) {
		t.Fatal("append reported no change")
	}
	if nftFindRuleByComment(list(), "inet", "filter", "input", "halite:ssh") == nil {
		t.Fatal("the appended rule is not in the chain")
	}
	if changed(call("nftables.append", "table", "filter", "chain", "input",
		"rule", "tcp dport 22 accept", "comment", "halite:ssh")) {
		t.Error("a second append of the same comment reported a change")
	}
	if n := nftRuleCount(list(), "inet", "filter", "input"); n != 1 {
		t.Errorf("input chain has %d rules after a repeated append, want 1", n)
	}

	// a comment-less rule warns and is not idempotent.
	out := call("nftables.append", "table", "filter", "chain", "input", "rule", "tcp dport 8080 accept")
	wv, _ := out.GetString("warnings")
	if list, _ := wv.([]any); len(list) == 0 {
		t.Error("a comment-less append did not warn")
	}
	call("nftables.append", "table", "filter", "chain", "input", "rule", "tcp dport 8080 accept")
	if n := nftRuleCount(list(), "inet", "filter", "input"); n != 3 {
		t.Errorf("two comment-less appends left %d rules, want 3 (they are not deduplicated)", n)
	}

	// insert goes before the rest.
	call("nftables.insert", "table", "filter", "chain", "input",
		"rule", "ct state established,related accept", "comment", "halite:ct")
	rs := list()
	// The inserted rule should have the lowest handle-order position: nft
	// lists rules in chain order, so it is rules[0] of this chain.
	var firstComment string
	for _, ru := range rs.Rules {
		if ru.Chain == "input" {
			firstComment = ru.Comment
			break
		}
	}
	if firstComment != "halite:ct" {
		t.Errorf("insert did not go to the front; first rule comment = %q", firstComment)
	}

	// check: syntax and presence.
	chk := call("nftables.check", "table", "filter", "chain", "input", "rule", "tcp dport 99 accept")
	if v, _ := chk.GetString("valid"); v != true {
		t.Errorf("check on a valid rule: %v", chk)
	}
	chk = call("nftables.check", "table", "filter", "chain", "input", "rule", "blorp nonsense")
	if v, _ := chk.GetString("valid"); v != false {
		t.Errorf("check on nonsense returned valid: %v", chk)
	}
	chk = call("nftables.check", "table", "filter", "chain", "input", "comment", "halite:ssh")
	if v, _ := chk.GetString("present"); v != true {
		t.Errorf("check for a present comment: %v", chk)
	}

	// delete by comment.
	if !changed(call("nftables.delete", "table", "filter", "chain", "input", "comment", "halite:ssh")) {
		t.Error("delete reported no change")
	}
	if nftFindRuleByComment(list(), "inet", "filter", "input", "halite:ssh") != nil {
		t.Error("the rule is still there after delete")
	}
	if changed(call("nftables.delete", "table", "filter", "chain", "input", "comment", "halite:ssh")) {
		t.Error("a second delete reported a change")
	}

	// flush guard: input is accept now, so a chain flush is allowed;
	// a table flush and a ruleset flush still need force.
	if _, err := r.Exec.Call(c, "nftables.flush", value.MapOf("table", "filter")); err == nil {
		t.Error("flush of a whole table was allowed without force")
	}
	if !changed(call("nftables.flush", "table", "filter", "chain", "input")) {
		t.Error("flushing a non-empty accept-policy chain reported no change")
	}
	if n := nftRuleCount(list(), "inet", "filter", "input"); n != 0 {
		t.Errorf("input not empty after flush: %d rules", n)
	}

	// save writes a restore file that flushes first.
	path := filepath.Join(t.TempDir(), "nftables.conf")
	call("nftables.save", "path", path)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("save wrote nothing readable: %v", err)
	}
	if !strings.HasPrefix(string(body), "#!/usr/sbin/nft -f") || !strings.Contains(string(body), "flush ruleset") {
		t.Errorf("the saved file is not a self-contained restore script:\n%s", body)
	}
	if changed(call("nftables.save", "path", path)) {
		t.Error("a second save reported a change")
	}

	// tear down.
	call("nftables.chain_absent", "name", "guard", "table", "filter")
	if !changed(call("nftables.table_absent", "name", "filter", "family", "inet")) {
		t.Error("table_absent reported no change")
	}
	if len(list().Tables) != 0 {
		t.Error("the table is still there after table_absent")
	}
}

// The `nftables` states converge and are idempotent on a real ruleset.
func TestLiveNFTablesStatesConverge(t *testing.T) {
	netnsReexec(t)
	r := New()
	c := &exec.Context{}

	st := func(fn string, kv ...any) (first, second bool) {
		t.Helper()
		mk := func() *value.Map {
			m := value.NewMap(len(kv) / 2)
			for i := 0; i+1 < len(kv); i += 2 {
				m.Set(kv[i].(string), kv[i+1])
			}
			return m
		}
		a, err := r.States.Call(c, fn, mk())
		if err != nil || a.Failed() {
			t.Fatalf("%s first run: %v / %+v", fn, err, a)
		}
		b, err := r.States.Call(c, fn, mk())
		if err != nil || !b.Succeeded() {
			t.Fatalf("%s second run: %v / %+v", fn, err, b)
		}
		return a.HasChanges(), b.HasChanges()
	}

	if f, s := st("nftables.table_present", "name", "haltest", "family", "inet"); !f || s {
		t.Errorf("table_present: first=%v second=%v", f, s)
	}
	if f, s := st("nftables.chain_present", "name", "input", "table", "haltest",
		"hook", "input", "priority", "0", "policy", "drop"); !f || s {
		t.Errorf("chain_present: first=%v second=%v", f, s)
	}
	if f, s := st("nftables.set_policy", "name", "input", "table", "haltest", "policy", "accept"); !f || s {
		t.Errorf("set_policy: first=%v second=%v", f, s)
	}
	if f, s := st("nftables.append", "name", "ssh", "table", "haltest", "chain", "input",
		"rule", "tcp dport 22 accept", "comment", "halite:ssh"); !f || s {
		t.Errorf("append: first=%v second=%v", f, s)
	}
	if f, s := st("nftables.delete", "name", "ssh", "table", "haltest", "chain", "input",
		"comment", "halite:ssh"); !f || s {
		t.Errorf("delete: first=%v second=%v", f, s)
	}
	if f, s := st("nftables.chain_absent", "name", "input", "table", "haltest"); !f || s {
		t.Errorf("chain_absent: first=%v second=%v", f, s)
	}
	if f, s := st("nftables.table_absent", "name", "haltest", "family", "inet"); !f || s {
		t.Errorf("table_absent: first=%v second=%v", f, s)
	}
}
