package builtin

import (
	"runtime"
	"strings"
	"testing"
	"time"

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
	// Called through the helper rather than the registry: off Linux the
	// registry refuses the whole module by platform first (that path is
	// TestJournaldRefusesOnANonLinuxPlatform), and this assertion is
	// about the tool check, which is the same everywhere.
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if _, err := journaldLines(c, []string{"journalctl", "-q", "--no-pager", "-N"}); err == nil ||
		!strings.Contains(err.Error(), "journalctl") {
		t.Errorf("the refusal does not name journalctl: %v", err)
	}
	if _, err := journaldQueryFn(c, value.NewMap(0)); err == nil ||
		!strings.Contains(err.Error(), "journalctl") {
		t.Errorf("query's refusal does not name journalctl: %v", err)
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

// `journalctl --list-boots` has two output shapes, and both are read.
//
// Every fixture here was captured from a real machine in this project's
// Vultr lab (contrib/tofu) on 2026-09-13, not written from the
// documentation. The defect these exist for is that systemd before v250
// **accepts `-o json` and ignores it**: journalctl exits 0 and prints
// its ordinary table, so the module's only symptom was a JSON parse
// error naming the boot index as an unexpected character.
//
// That is not an old-machine problem. systemd 239 is RHEL 8 and systemd
// 249 is Ubuntu 22.04, both tier 1 in SPEC 27.1, so `journald.list_boots`
// answered with an error rather than a list on two supported platforms.
func TestListBootsReadsBothOfSystemdsOutputShapes(t *testing.T) {
	for _, c := range []struct {
		name    string
		stdout  string
		wantID  string
		wantIdx int64
		// nil when the fixture's timestamps are unreadable.
		wantFirst any
	}{
		{
			name:      "systemd 252 on Rocky 9, which emits JSON",
			stdout:    `[{"index":0,"boot_id":"94edce74c7bf438993ce789b54915c22","first_entry":1789319247066407,"last_entry":1789320711204285}]`,
			wantID:    "94edce74c7bf438993ce789b54915c22",
			wantIdx:   0,
			wantFirst: int64(1789319247066407),
		},
		{
			name:      "systemd 259 on Ubuntu 26.04, which emits JSON",
			stdout:    `[{"index":0,"boot_id":"3c3b80deab9e459b86533649bb86c0be","first_entry":1789319149581402,"last_entry":1789320715089233}]`,
			wantID:    "3c3b80deab9e459b86533649bb86c0be",
			wantIdx:   0,
			wantFirst: int64(1789319149581402),
		},
		{
			name:      "systemd 239 on AlmaLinux 8, which prints a table",
			stdout:    " 0 050983458345492eb517fb01ca3d078f Sun 2026-09-13 17:08:30 UTC—Sun 2026-09-13 17:31:49 UTC\n",
			wantID:    "050983458345492eb517fb01ca3d078f",
			wantIdx:   0,
			wantFirst: time.Date(2026, 9, 13, 17, 8, 30, 0, time.UTC).UnixMicro(),
		},
		{
			name:      "systemd 249 on Ubuntu 22.04, which prints a table",
			stdout:    " 0 3e19eb5ae09b4dcda814ac99dc156fb0 Sun 2026-09-13 17:05:38 UTC—Sun 2026-09-13 17:31:52 UTC\n",
			wantID:    "3e19eb5ae09b4dcda814ac99dc156fb0",
			wantIdx:   0,
			wantFirst: time.Date(2026, 9, 13, 17, 5, 38, 0, time.UTC).UnixMicro(),
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			boots, err := journaldParseBoots(c.stdout)
			if err != nil {
				t.Fatalf("did not parse: %v", err)
			}
			if len(boots) != 1 {
				t.Fatalf("got %d boots, want 1", len(boots))
			}
			m, ok := boots[0].(*value.Map)
			if !ok {
				t.Fatalf("a boot came back as %T, not a map", boots[0])
			}
			if id, _ := m.GetString("boot_id"); id != c.wantID {
				t.Errorf("boot_id = %v, want %q", id, c.wantID)
			}
			if idx, _ := m.GetString("index"); idx != c.wantIdx {
				t.Errorf("index = %v (%T), want %d", idx, idx, c.wantIdx)
			}
			// The same key, the same type, from both shapes: a caller
			// must never have to ask which systemd answered.
			if first, _ := m.GetString("first_entry"); first != c.wantFirst {
				t.Errorf("first_entry = %v (%T), want %v (%T)",
					first, first, c.wantFirst, c.wantFirst)
			}
		})
	}
}

// A local zone abbreviation is refused rather than read as UTC.
//
// This is the reason `--utc` is in the argv. Without it, systemd 239
// prints the *local* abbreviation, and Go parses one it cannot resolve
// as offset zero under a fabricated zone of that name -- so a Pacific
// host's boot would be recorded seven hours from where it happened, with
// no error anywhere. The fixture is that same machine with TZ set.
func TestABootTimeInAnUnresolvableZoneIsNilRatherThanWrong(t *testing.T) {
	const pacific = " 0 050983458345492eb517fb01ca3d078f " +
		"Sun 2026-09-13 10:08:30 PDT—Sun 2026-09-13 10:32:57 PDT\n"

	boots, err := journaldParseBoots(pacific)
	if err != nil {
		t.Fatalf("did not parse: %v", err)
	}
	m := boots[0].(*value.Map)
	// The identity still reads, because that part is unambiguous.
	if id, _ := m.GetString("boot_id"); id != "050983458345492eb517fb01ca3d078f" {
		t.Errorf("boot_id = %v", id)
	}
	first, _ := m.GetString("first_entry")
	if first != nil {
		t.Errorf("first_entry = %v, want nil: PDT cannot be resolved and 0 would be a "+
			"wrong answer rather than a missing one", first)
	}
}

// The argv carries --utc, which is what makes the table above readable.
func TestTheBootListIsAskedForInUTC(t *testing.T) {
	argv := journaldListBootsArgv()
	found := false
	for _, a := range argv {
		if a == "--utc" {
			found = true
		}
	}
	if !found {
		t.Errorf("%v does not pass --utc, so a pre-v250 systemd will print local "+
			"abbreviations this build cannot resolve", argv)
	}
}
