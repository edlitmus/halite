package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// `sqlite3 TCC.db "SELECT client, client_type, auth_value ..."` output:
// one row per client, columns joined with '|'. auth_value 2 is allowed,
// 0 is denied; client_type 1 is a path, 0 a bundle id.
const assistiveRowsOutput = `/System/Library/CoreServices/Foo.app/Contents/MacOS/Foo|1|2
at.obdev.LaunchBar|0|2
com.1password.1password|0|0
`

func macAssistiveCtx(t *testing.T, responses map[string]exec.Result) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	runner := &exec.RecordingRunner{Responses: responses}
	c := newCtx(false)
	c.Runner = runner
	c.Lookup = func(name string) string {
		if name == "sqlite3" {
			return "/usr/bin/sqlite3"
		}
		return ""
	}
	return c, runner
}

// lastSQL returns the statement of the most recent sqlite3 invocation.
func lastSQL(t *testing.T, runner *exec.RecordingRunner) string {
	t.Helper()
	for i := len(runner.Ran) - 1; i >= 0; i-- {
		argv := runner.Ran[i].Argv
		if len(argv) == 3 && argv[0] == "sqlite3" {
			return argv[2]
		}
	}
	t.Fatalf("no sqlite3 command was run: %v", runner.RanCommands())
	return ""
}

func TestMacAssistiveListParses(t *testing.T) {
	c, runner := macAssistiveCtx(t, nil)
	runner.Default = exec.Result{Stdout: assistiveRowsOutput}

	rows, err := macAssistiveList(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("read %d rows, want 3: %+v", len(rows), rows)
	}
	if rows[0].Client != "/System/Library/CoreServices/Foo.app/Contents/MacOS/Foo" ||
		rows[0].ClientType != 1 || !rows[0].Enabled {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].Client != "at.obdev.LaunchBar" || rows[1].ClientType != 0 || !rows[1].Enabled {
		t.Errorf("row 1 = %+v", rows[1])
	}
	// auth_value 0 is denied, so the entry exists but is not enabled.
	if rows[2].Client != "com.1password.1password" || rows[2].Enabled {
		t.Errorf("row 2 = %+v", rows[2])
	}
	if !strings.Contains(lastSQL(t, runner), "service = 'kTCCServiceAccessibility'") {
		t.Errorf("select did not scope to the Accessibility service: %q", lastSQL(t, runner))
	}
}

func TestMacAssistiveInstalledVsEnabled(t *testing.T) {
	c, runner := macAssistiveCtx(t, nil)
	runner.Default = exec.Result{Stdout: assistiveRowsOutput}

	// A denied entry: on the list, so installed, but not enabled.
	row, err := macAssistiveFind(c, "com.1password.1password")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil {
		t.Fatal("a client with a denied entry read back as not installed")
	}
	if row.Enabled {
		t.Error("a denied entry read back as enabled")
	}

	// A client with no row at all.
	row, err = macAssistiveFind(c, "com.example.absent")
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Errorf("an absent client read back as %+v", row)
	}
}

func TestMacAssistiveInstallArgv(t *testing.T) {
	c, runner := macAssistiveCtx(t, nil)

	if err := macAssistiveInstall(c, "com.example.app", true); err != nil {
		t.Fatal(err)
	}
	sql := lastSQL(t, runner)
	for _, want := range []string{
		"INSERT OR REPLACE INTO access",
		"'kTCCServiceAccessibility', 'com.example.app', 0, 2, 4, 1, 'UNUSED'",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("install SQL missing %q:\n%s", want, sql)
		}
	}

	// A path client is client_type 1; enable=false writes auth_value 0.
	if err := macAssistiveInstall(c, "/opt/homebrew/bin/thing", false); err != nil {
		t.Fatal(err)
	}
	sql = lastSQL(t, runner)
	if !strings.Contains(sql, "'/opt/homebrew/bin/thing', 1, 0, 4, 1, 'UNUSED'") {
		t.Errorf("path install SQL = %s", sql)
	}
}

func TestMacAssistiveEnableAndRemoveArgv(t *testing.T) {
	c, runner := macAssistiveCtx(t, nil)

	if err := macAssistiveEnable(c, "com.example.app", false); err != nil {
		t.Fatal(err)
	}
	if sql := lastSQL(t, runner); sql != "UPDATE access SET auth_value = 0 WHERE service = 'kTCCServiceAccessibility' AND client = 'com.example.app'" {
		t.Errorf("enable SQL = %s", sql)
	}

	if err := macAssistiveRemove(c, "com.example.app"); err != nil {
		t.Fatal(err)
	}
	if sql := lastSQL(t, runner); sql != "DELETE FROM access WHERE service = 'kTCCServiceAccessibility' AND client = 'com.example.app'" {
		t.Errorf("remove SQL = %s", sql)
	}
}

func TestMacAssistiveQuoteIsEscaped(t *testing.T) {
	c, runner := macAssistiveCtx(t, nil)
	if err := macAssistiveRemove(c, "a'b"); err != nil {
		t.Fatal(err)
	}
	if sql := lastSQL(t, runner); !strings.Contains(sql, "client = 'a''b'") {
		t.Errorf("a quote in the client was not doubled: %s", sql)
	}
}

func TestMacAssistiveInstallSurfacesFullDiskAccess(t *testing.T) {
	c, runner := macAssistiveCtx(t, nil)
	runner.Default = exec.Result{Code: 1, Stderr: "Error: attempt to write a readonly database\n"}

	err := macAssistiveInstall(c, "com.example.app", true)
	if err == nil {
		t.Fatal("a readonly TCC.db was not an error")
	}
	if !strings.Contains(err.Error(), "Full Disk Access") {
		t.Errorf("the error did not name Full Disk Access: %v", err)
	}
}

func TestMacAssistiveNeedsSqlite3(t *testing.T) {
	c, _ := macAssistiveCtx(t, nil)
	c.Lookup = func(string) string { return "" }
	if _, err := macAssistiveList(c); err == nil || !strings.Contains(err.Error(), "sqlite3") {
		t.Errorf("a missing sqlite3 was not reported: %v", err)
	}
}

func TestMacAssistiveRegisteredAndRestricted(t *testing.T) {
	r := New()
	for _, name := range []string{
		"mac_assistive.list", "mac_assistive.installed", "mac_assistive.enabled",
		"mac_assistive.install", "mac_assistive.enable", "mac_assistive.remove",
	} {
		sig, ok := r.Exec.Signatures().Lookup(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if len(sig.Platforms) != 1 || sig.Platforms[0] != "darwin" {
			t.Errorf("%s platforms = %v", name, sig.Platforms)
		}
	}
	// The mutating three need root.
	for _, name := range []string{"mac_assistive.install", "mac_assistive.enable", "mac_assistive.remove"} {
		sig, _ := r.Exec.Signatures().Lookup(name)
		if !sig.Mutates {
			t.Errorf("%s is not marked as mutating", name)
		}
		if len(sig.Privileges) != 1 || sig.Privileges[0] != "root" {
			t.Errorf("%s privileges = %v", name, sig.Privileges)
		}
	}
}
