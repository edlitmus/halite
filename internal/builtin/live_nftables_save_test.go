package builtin

import (
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// **A second nftables.save after traffic changes nothing** (issue 321,
// M-40). A rule with `counter` listed as `counter packets 2 bytes 168`,
// and those numbers move with every packet, so on a host with traffic
// save never converged. Traffic is sent through the rule between the two
// saves, inside a throwaway network namespace.
func TestLiveNFTablesSaveConvergesWithACounter(t *testing.T) {
	netnsReexec(t)
	if out, err := osexec.Command("ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
		t.Skipf("the namespace's loopback could not be raised: %v %s", err, out)
	}
	r := New()
	c := &exec.Context{}
	call := func(fn string, kv ...any) *value.Map {
		t.Helper()
		out, err := r.Exec.Call(c, fn, value.MapOf(kv...))
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		m, _ := out.(*value.Map)
		return m
	}
	changed := func(m *value.Map) bool { v, _ := m.GetString("changed"); return v == true }
	traffic := func() {
		t.Helper()
		conn, err := net.Dial("udp", "127.0.0.1:9")
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		for i := 0; i < 5; i++ {
			_, _ = conn.Write([]byte("x"))
		}
	}

	call("nftables.table_present", "name", "filter", "family", "inet")
	call("nftables.chain_present", "name", "output", "table", "filter",
		"hook", "output", "priority", "0", "policy", "accept")
	call("nftables.append", "table", "filter", "chain", "output", "family", "inet",
		"rule", "udp dport 9 counter accept", "comment", "count-discard")
	traffic()

	path := filepath.Join(t.TempDir(), "nftables.conf")
	if !changed(call("nftables.save", "path", path)) {
		t.Fatal("the first save changed nothing")
	}
	first, _ := os.ReadFile(path)
	traffic()
	if changed(call("nftables.save", "path", path)) {
		second, _ := os.ReadFile(path)
		t.Errorf("a save after traffic through a counter reported a change\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if !strings.Contains(string(first), "counter") {
		t.Errorf("the saved rule lost its counter:\n%s", first)
	}
}
