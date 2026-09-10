package builtin

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `journald`, read against the real journal on the machine running the
// tests.
//
// This half needs no privilege and no gate: `journalctl` shows a
// non-root caller its own entries, which is enough to exercise the JSON
// stream parser, the field readers and the boot list against real
// output. It skips where there is no journal to read.
func TestLiveJournaldReadsTheRealJournal(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("the journal is a systemd thing; this is %s", runtime.GOOS)
	}
	c := &exec.Context{}
	if c.Which("journalctl") == "" {
		t.Skip("this host has no journalctl")
	}
	if _, err := os.Stat("/run/systemd/journal"); err != nil {
		t.Skip("systemd-journald is not running here")
	}
	r := New()

	call := func(fn string, kv ...any) *value.Map {
		t.Helper()
		args := value.NewMap(len(kv) / 2)
		for i := 0; i+1 < len(kv); i += 2 {
			args.Set(kv[i].(string), kv[i+1])
		}
		out, err := r.Exec.Call(c, fn, args)
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		m, _ := out.(*value.Map)
		return m
	}

	// query, scoped to this test's own uid so it works without root.
	uidMatch := "_UID=" + strconv.Itoa(os.Geteuid())
	q := call("journald.query", "match", []any{uidMatch}, "lines", int64(20))
	entries, _ := q.GetString("entries")
	list, _ := entries.([]any)
	if len(list) == 0 {
		t.Skip("this caller has no journal entries of its own to read")
	}
	// Every entry is a record with at least a cursor and a realtime stamp.
	for _, e := range list {
		m := e.(*value.Map)
		if cur, _ := m.GetString("__CURSOR"); cur == "" {
			t.Errorf("an entry came back with no __CURSOR: %v", m)
			break
		}
	}
	cursor, _ := q.GetString("cursor")
	if cursor == "" {
		t.Fatal("query returned entries but no cursor for the next poll")
	}

	// The cursor round-trips: asking again after it returns only what is
	// newer, which for a quiet uid is usually nothing.
	q2 := call("journald.query", "match", []any{uidMatch}, "after_cursor", cursor, "lines", int64(50))
	c2, _ := q2.GetString("count")
	if n, ok := c2.(int64); !ok || n < 0 {
		t.Errorf("a second query after the cursor returned count=%v", c2)
	}

	// fields returns a bare list of names; _TRANSPORT is in every journal.
	rawFields, err := r.Exec.Call(c, "journald.fields", value.NewMap(0))
	if err != nil {
		t.Fatalf("fields: %v", err)
	}
	names, _ := rawFields.([]any)
	if len(names) == 0 {
		t.Error("journald.fields returned nothing")
	}
	var hasTransport bool
	for _, n := range names {
		if value.KeyString(n) == "_TRANSPORT" {
			hasTransport = true
		}
	}
	if !hasTransport {
		t.Errorf("_TRANSPORT is in every journal and journald.fields did not list it: %v", names)
	}

	rawVals, err := r.Exec.Call(c, "journald.field_values", value.MapOf("field", "_TRANSPORT"))
	if err != nil {
		t.Fatalf("field_values: %v", err)
	}
	vals, _ := rawVals.([]any)
	if len(vals) == 0 {
		t.Error("_TRANSPORT has values in every journal and field_values returned none")
	}

	// list_boots: at least the current boot.
	rawBoots, err := r.Exec.Call(c, "journald.list_boots", value.NewMap(0))
	if err != nil {
		t.Fatalf("list_boots: %v", err)
	}
	boots, _ := rawBoots.([]any)
	if len(boots) == 0 {
		t.Error("list_boots returned no boots")
	} else {
		b := boots[len(boots)-1].(*value.Map)
		if id, _ := b.GetString("boot_id"); id == "" {
			t.Errorf("a boot entry has no boot_id: %v", b)
		}
	}

	// disk_usage: a byte count and the sentence it came from.
	du := call("journald.disk_usage")
	if txt, _ := du.GetString("text"); !strings.Contains(txt.(string), "file system") {
		t.Errorf("disk_usage text = %v", txt)
	}
	if b, _ := du.GetString("bytes"); b == nil {
		t.Errorf("disk_usage could not parse a byte count from %v", du)
	}
}

// The control verbs over journald's own varlink socket, and the
// journalctl fallback. Gated: the socket is root-only and rotating a
// real journal is a real action.
func TestLiveJournaldControlOverVarlink(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this rotate and sync the real journal")
	}
	if os.Geteuid() != 0 {
		t.Skip("journald's varlink socket is root-only")
	}
	if _, err := os.Stat(JournaldVarlinkSocket); err != nil {
		t.Skipf("no journald varlink socket at %s", JournaldVarlinkSocket)
	}
	r := New()
	c := &exec.Context{}

	// sync goes over varlink.
	out, err := r.Exec.Call(c, "journald.sync", value.NewMap(0))
	if err != nil {
		t.Fatalf("journald.sync: %v", err)
	}
	if via, _ := out.(*value.Map).GetString("via"); via != "varlink" {
		t.Errorf("journald.sync went via %v, want varlink", via)
	}

	// rotate and flush too, over varlink.
	for _, fn := range []string{"journald.rotate", "journald.flush"} {
		out, err := r.Exec.Call(c, fn, value.NewMap(0))
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		if via, _ := out.(*value.Map).GetString("via"); via != "varlink" {
			t.Errorf("%s went via %v, want varlink", fn, via)
		}
	}

	// Point the socket at nothing: the fallback to journalctl runs.
	old := JournaldVarlinkSocket
	JournaldVarlinkSocket = "/run/halite-test-no-such-journal.sock"
	t.Cleanup(func() { JournaldVarlinkSocket = old })
	out, err = r.Exec.Call(c, "journald.sync", value.NewMap(0))
	if err != nil {
		t.Fatalf("journald.sync via fallback: %v", err)
	}
	if via, _ := out.(*value.Map).GetString("via"); via != "journalctl --sync" {
		t.Errorf("with the socket gone, journald.sync went via %v, want the journalctl fallback", via)
	}
}
