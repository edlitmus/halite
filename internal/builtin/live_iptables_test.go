package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// A rule this module adds is one `iptables` reports, the tool's own `-C`
// agrees is present, and a second add leaves alone -- all against a real
// iptables 1.8.10 inside a throwaway network namespace.
func TestLiveIPTablesManagesARealFilterTable(t *testing.T) {
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
	rulesIn := func(table, chain string) []string {
		t.Helper()
		out, err := r.Exec.Call(c, "iptables.get_rules", value.NewMap(0))
		if err != nil {
			t.Fatalf("get_rules: %v", err)
		}
		tv, _ := out.(*value.Map).GetString("tables")
		tbl, _ := tv.(*value.Map).GetString(table)
		if tbl == nil {
			return nil
		}
		cv, _ := tbl.(*value.Map).GetString("chains")
		ch, _ := cv.(*value.Map).GetString(chain)
		if ch == nil {
			return nil
		}
		rv, _ := ch.(*value.Map).GetString("rules")
		list, _ := rv.([]any)
		out2 := make([]string, len(list))
		for i, e := range list {
			out2[i] = value.KeyString(e)
		}
		return out2
	}

	// version reports the backend, which on any modern distro is nf_tables.
	v, err := r.Exec.Call(c, "iptables.version", value.NewMap(0))
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if b, _ := v.(*value.Map).GetString("backend"); b != "nf_tables" && b != "legacy" {
		t.Errorf("backend read as %q", b)
	}

	// A fresh namespace: INPUT exists, policy ACCEPT, no rules.
	if got := rulesIn("filter", "INPUT"); len(got) != 0 {
		t.Fatalf("a fresh namespace already has INPUT rules: %v", got)
	}

	// append, built from parts, then idempotence.
	add := call("iptables.append", "chain", "INPUT",
		"protocol", "tcp", "dport", "22", "comment", "halite:ssh", "jump", "ACCEPT")
	if changed, _ := add.GetString("changed"); changed != true {
		t.Fatalf("append reported no change: %v", add)
	}
	got := rulesIn("filter", "INPUT")
	if len(got) != 1 || !strings.Contains(got[0], "--dport 22") || !strings.Contains(got[0], "halite:ssh") {
		t.Fatalf("INPUT after append: %v", got)
	}
	again := call("iptables.append", "chain", "INPUT",
		"protocol", "tcp", "dport", "22", "comment", "halite:ssh", "jump", "ACCEPT")
	if changed, _ := again.GetString("changed"); changed != false {
		t.Errorf("a second append reported a change: %v", again)
	}
	if got := rulesIn("filter", "INPUT"); len(got) != 1 {
		t.Errorf("a second append added a duplicate rule: %v", got)
	}

	// check agrees.
	chk, err := r.Exec.Call(c, "iptables.check", value.MapOf(
		"chain", "INPUT", "protocol", "tcp", "dport", "22", "comment", "halite:ssh", "jump", "ACCEPT"))
	if err != nil || chk != true {
		t.Errorf("iptables.check on the present rule returned %v, %v", chk, err)
	}
	chk, _ = r.Exec.Call(c, "iptables.check", value.MapOf("chain", "INPUT", "protocol", "tcp", "dport", "9999", "jump", "ACCEPT"))
	if chk != false {
		t.Errorf("iptables.check on an absent rule returned %v", chk)
	}

	// insert goes to the front.
	call("iptables.insert", "chain", "INPUT", "position", int64(1),
		"ctstate", "RELATED,ESTABLISHED", "jump", "ACCEPT")
	if got := rulesIn("filter", "INPUT"); len(got) != 2 || !strings.Contains(got[0], "ctstate") {
		t.Errorf("insert did not go to the front: %v", got)
	}

	// a user chain, and a jump to it.
	nc := call("iptables.new_chain", "chain", "halite-in")
	if changed, _ := nc.GetString("changed"); changed != true {
		t.Errorf("new_chain reported no change: %v", nc)
	}
	if changed, _ := call("iptables.new_chain", "chain", "halite-in").GetString("changed"); changed != false {
		t.Error("a second new_chain reported a change")
	}
	call("iptables.append", "chain", "INPUT", "jump", "halite-in")
	call("iptables.append", "chain", "halite-in", "source", "10.0.0.0/8", "jump", "DROP")
	if got := rulesIn("filter", "halite-in"); len(got) != 1 || !strings.Contains(got[0], "10.0.0.0/8") {
		t.Errorf("halite-in after append: %v", got)
	}

	// policy on a built-in chain.
	sp := call("iptables.set_policy", "chain", "FORWARD", "policy", "DROP")
	if changed, _ := sp.GetString("changed"); changed != true {
		t.Errorf("set_policy reported no change: %v", sp)
	}
	pol, _ := r.Exec.Call(c, "iptables.get_policy", value.MapOf("chain", "FORWARD"))
	if pol != "DROP" {
		t.Errorf("get_policy after set = %v", pol)
	}
	if changed, _ := call("iptables.set_policy", "chain", "FORWARD", "policy", "DROP").GetString("changed"); changed != false {
		t.Error("a second set_policy reported a change")
	}

	// flush the user chain (allowed), then remove the jump and the chain.
	call("iptables.flush", "chain", "halite-in")
	if got := rulesIn("filter", "halite-in"); len(got) != 0 {
		t.Errorf("halite-in not empty after flush: %v", got)
	}
	call("iptables.delete", "chain", "INPUT", "jump", "halite-in")
	dc := call("iptables.delete_chain", "chain", "halite-in")
	if changed, _ := dc.GetString("changed"); changed != true {
		t.Errorf("delete_chain reported no change: %v", dc)
	}

	// The dangerous-flush guard: INPUT to DROP, then a bare flush is
	// refused, and a forced one is not.
	call("iptables.set_policy", "chain", "INPUT", "policy", "DROP")
	if _, err := r.Exec.Call(c, "iptables.flush", value.MapOf("chain", "INPUT")); err == nil {
		t.Error("flushing a DROP-policy INPUT was allowed without force")
	} else if !strings.Contains(err.Error(), "off the network") {
		t.Errorf("the flush refusal does not explain the danger: %v", err)
	}
	if _, err := r.Exec.Call(c, "iptables.flush", value.MapOf("chain", "INPUT", "force", true)); err != nil {
		t.Errorf("a forced flush was refused: %v", err)
	}
	if got := rulesIn("filter", "INPUT"); len(got) != 0 {
		t.Errorf("INPUT not empty after a forced flush: %v", got)
	}
}

// The `iptables` states converge and are idempotent on a real table.
func TestLiveIPTablesStatesConverge(t *testing.T) {
	netnsReexec(t)
	r := New()
	c := &exec.Context{}

	st := func(fn string, kv ...any) (changed, second bool) {
		t.Helper()
		args := value.NewMap(len(kv) / 2)
		for i := 0; i+1 < len(kv); i += 2 {
			args.Set(kv[i].(string), kv[i+1])
		}
		first, err := r.States.Call(c, fn, args)
		if err != nil || first.Failed() {
			t.Fatalf("%s first run: %v / %+v", fn, err, first)
		}
		args2 := value.NewMap(len(kv) / 2)
		for i := 0; i+1 < len(kv); i += 2 {
			args2.Set(kv[i].(string), kv[i+1])
		}
		again, err := r.States.Call(c, fn, args2)
		if err != nil || !again.Succeeded() {
			t.Fatalf("%s second run: %v / %+v", fn, err, again)
		}
		return first.HasChanges(), again.HasChanges()
	}

	if ch, again := st("iptables.chain_present", "name", "halite-fw"); !ch || again {
		t.Errorf("chain_present: changed=%v second-run-changed=%v", ch, again)
	}
	if ch, again := st("iptables.append", "name", "-p tcp --dport 8080 -j ACCEPT", "chain", "INPUT"); !ch || again {
		t.Errorf("append: changed=%v second-run-changed=%v", ch, again)
	}
	if ch, again := st("iptables.set_policy", "name", "FORWARD", "policy", "DROP"); !ch || again {
		t.Errorf("set_policy: changed=%v second-run-changed=%v", ch, again)
	}
	if ch, again := st("iptables.delete", "name", "-p tcp --dport 8080 -j ACCEPT", "chain", "INPUT"); !ch || again {
		t.Errorf("delete: changed=%v second-run-changed=%v", ch, again)
	}
	if ch, again := st("iptables.chain_absent", "name", "halite-fw"); !ch || again {
		t.Errorf("chain_absent: changed=%v second-run-changed=%v", ch, again)
	}
}
