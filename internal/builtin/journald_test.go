package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

func TestJournaldQueryArgv(t *testing.T) {
	argv, err := journaldQueryArgv(journaldQueryOpts{
		Unit: "sshd.service", Priority: "err", Since: "-1h", Boot: "0",
		Lines: 50, Reverse: true,
		Match: []string{"_UID=0", "PRIORITY=3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	want := "journalctl -q --no-pager -o json -r -u sshd.service -p err --since -1h -b 0 -n 50 _UID=0 PRIORITY=3"
	if got != want {
		t.Errorf("argv =\n  %s\nwant\n  %s", got, want)
	}

	// lines=0 means no limit.
	argv, _ = journaldQueryArgv(journaldQueryOpts{Lines: 0})
	if !strings.Contains(strings.Join(argv, " "), "--lines=all") {
		t.Errorf("lines=0 did not become --lines=all: %v", argv)
	}
	// A negative line count is a mistake.
	if _, err := journaldQueryArgv(journaldQueryOpts{Lines: -3}); err == nil {
		t.Error("a negative line count was accepted")
	}
	// A malformed match is refused before journalctl sees it.
	if _, err := journaldQueryArgv(journaldQueryOpts{Lines: 1, Match: []string{"not a match"}}); err == nil {
		t.Error("a bare word was accepted as a match")
	}
	// The disjunction marker is allowed.
	if _, err := journaldQueryArgv(journaldQueryOpts{Lines: 1, Match: []string{"_UID=0", "+", "_UID=1000"}}); err != nil {
		t.Errorf("the `+` disjunction marker was refused: %v", err)
	}
}

func TestJournaldValidField(t *testing.T) {
	for _, ok := range []string{"MESSAGE", "_SYSTEMD_UNIT", "PRIORITY", "_PID", "SYSLOG_IDENTIFIER"} {
		if !journaldValidField(ok) {
			t.Errorf("field %q rejected", ok)
		}
	}
	for _, bad := range []string{"", "message", "unit-name", "A B", "FIELD;rm -rf"} {
		if journaldValidField(bad) {
			t.Errorf("%q accepted as a field name", bad)
		}
	}
}

func TestJournaldParseJSONStream(t *testing.T) {
	stream := `{"__CURSOR":"s=aaa;i=1","MESSAGE":"first","PRIORITY":"6"}

{"__CURSOR":"s=aaa;i=2","MESSAGE":"second","_PID":"42"}
`
	entries, cursor, err := journaldParseJSONStream(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(entries))
	}
	if cursor != "s=aaa;i=2" {
		t.Errorf("cursor = %q, want the last entry's", cursor)
	}
	first := entries[0].(*value.Map)
	if m, _ := first.GetString("MESSAGE"); m != "first" {
		t.Errorf("first entry MESSAGE = %v", m)
	}

	// A line that is not JSON is an error, not a silently dropped entry.
	if _, _, err := journaldParseJSONStream("{ok:1}\nnot json\n"); err == nil {
		t.Error("a non-JSON line was accepted")
	}
}

func TestJournaldParseUsage(t *testing.T) {
	for _, c := range []struct {
		text string
		want int64
	}{
		{"Archived and active journals take up 82.3M in the file system.", 86297804}, // 82.3 * 1048576
		{"take up 1.2G in the file system.", 1288490188},                             // 1.2 * 1073741824
		{"Journals take up 512.0K in the file system.", 512 << 10},
		{"take up 900.0B in the file system.", 900},
	} {
		got, ok := journaldParseUsage(c.text)
		if !ok || got != c.want {
			t.Errorf("journaldParseUsage(%q) = %d, %v; want %d", c.text, got, ok, c.want)
		}
	}
	if _, ok := journaldParseUsage("nothing useful here"); ok {
		t.Error("a sentence with no size parsed")
	}
}

// The control verbs try varlink, and fall back to journalctl when the
// socket is not there.
func TestJournaldControlFallsBackToJournalctl(t *testing.T) {
	old := JournaldVarlinkSocket
	JournaldVarlinkSocket = "/nonexistent/halite-test/journal.sock"
	t.Cleanup(func() { JournaldVarlinkSocket = old })

	c := &exec.Context{
		Runner: &exec.RecordingRunner{},
		Lookup: func(string) string { return "/usr/bin/journalctl" },
	}
	out, err := journaldControl(c, "Synchronize", "io.systemd.Journal.Synchronize", "--sync", "synced")
	if err != nil {
		t.Fatal(err)
	}
	m := out.(*value.Map)
	if via, _ := m.GetString("via"); via != "journalctl --sync" {
		t.Errorf("via = %v, want the journalctl fallback", via)
	}
	var ran bool
	for _, cmd := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if cmd == "journalctl --sync" {
			ran = true
		}
	}
	if !ran {
		t.Errorf("the fallback did not run journalctl --sync: %v", c.Runner.(*exec.RecordingRunner).RanCommands())
	}

	// Test mode runs neither path.
	c2 := &exec.Context{Test: true, Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "/usr/bin/journalctl" }}
	out, _ = journaldControl(c2, "Rotate", "io.systemd.Journal.Rotate", "--rotate", "rotated")
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("test mode did not predict a change: %v", out)
	}
	if len(c2.Runner.(*exec.RecordingRunner).RanCommands()) != 0 {
		t.Errorf("a test run executed a command: %v", c2.Runner.(*exec.RecordingRunner).RanCommands())
	}
}

func TestJournaldVacuumNeedsACriterion(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "/usr/bin/journalctl" }}
	if _, err := journaldVacuumFn(c, value.NewMap(0)); err == nil {
		t.Error("vacuum with no size, time or files was accepted")
	}
	// With a size it builds the flag.
	c2 := &exec.Context{Test: true, Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "/usr/bin/journalctl" }}
	out, err := journaldVacuumFn(c2, value.MapOf("size", "500M"))
	if err != nil {
		t.Fatal(err)
	}
	cmd, _ := out.(*value.Map).GetString("command")
	if !strings.Contains(cmd.(string), "--vacuum-size=500M") {
		t.Errorf("vacuum command = %v", cmd)
	}
}

func TestJournaldRefusesWithoutJournalctl(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if _, err := New().Exec.Call(c, "journald.fields", value.NewMap(0)); err == nil ||
		!strings.Contains(err.Error(), "journalctl") {
		t.Errorf("the refusal does not name journalctl: %v", err)
	}
}

func TestJournaldRefusesOnANonLinuxPlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this is about the platforms journald is not declared for")
	}
	_, err := New().Exec.Call(&exec.Context{}, "journald.query", value.NewMap(0))
	if err == nil || !strings.Contains(err.Error(), "this node is "+runtime.GOOS) {
		t.Errorf("journald.query did not refuse by platform: %v", err)
	}
}
