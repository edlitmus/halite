package log

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/redact"
)

func fixed(l *Logger) *Logger {
	l.now = func() time.Time { return time.Date(2026, 8, 22, 15, 4, 5, 123456000, time.UTC) }
	return l
}

func TestLevelThreshold(t *testing.T) {
	var out strings.Builder
	l, err := New(Options{Level: Warn, Stderr: &out})
	if err != nil {
		t.Fatal(err)
	}
	fixed(l)
	l.Error("an error")
	l.Warn("a warning")
	l.Info("an info")
	l.Debug("a debug")

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d records at level warn, want 2:\n%s", len(lines), out.String())
	}
	for _, want := range []string{"an error", "a warning"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("%q was not written", want)
		}
	}
	for _, unwanted := range []string{"an info", "a debug"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("%q was written at level warn", unwanted)
		}
	}
}

func TestJSONRecordCarriesTheNamedFields(t *testing.T) {
	var out strings.Builder
	l, _ := New(Options{Level: Info, Stderr: &out, Fields: map[string]any{
		"component": "state", "node_id": "web1",
	}})
	fixed(l)
	l.Info("compiled", "sls", "web", "line", 12)

	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &rec); err != nil {
		t.Fatalf("the default format is not JSON: %v\n%s", err, out.String())
	}
	// SPEC 26.1 names these on every record.
	for k, want := range map[string]any{
		"ts": "2026-08-22T15:04:05.123456Z", "level": "info", "msg": "compiled",
		"component": "state", "node_id": "web1", "sls": "web",
	} {
		if rec[k] != want {
			t.Errorf("%s = %#v, want %#v", k, rec[k], want)
		}
	}
	if rec["line"] != float64(12) {
		t.Errorf("line = %#v", rec["line"])
	}
}

func TestConsoleFormatLeadsWithTheMessage(t *testing.T) {
	var out strings.Builder
	l, _ := New(Options{Level: Info, Format: Console, Stderr: &out,
		Fields: map[string]any{"component": "state", "node_id": "web1"}})
	fixed(l)
	l.Warn("something happened", "sls", "web")

	line := strings.TrimSpace(out.String())
	if !strings.HasPrefix(line, "warn: something happened") {
		t.Errorf("the message should lead: %q", line)
	}
	// The fixed fields are noise to a person watching a terminal.
	if strings.Contains(line, "component=") || strings.Contains(line, "ts=") {
		t.Errorf("console output should not repeat the fixed fields: %q", line)
	}
	if !strings.Contains(line, "sls=web") {
		t.Errorf("the record's own fields should be there: %q", line)
	}
}

func TestFileSinkAndItsFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "halite.log")
	var out strings.Builder
	l, err := New(Options{Level: Info, Stderr: &out, File: path})
	if err != nil {
		t.Fatal(err)
	}
	fixed(l)
	l.Info("to both")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "to both") || !strings.Contains(out.String(), "to both") {
		t.Errorf("the record should reach both sinks:\nfile: %s\nstderr: %s", data, out.String())
	}

	// A file that cannot be opened is an error. An operator who asked
	// for a file is relying on it, and falling back to stderr quietly is
	// how a record nobody reads becomes a record nobody has.
	if _, err := New(Options{File: filepath.Join(dir, "no-such-dir", "x.log")}); err == nil {
		t.Error("an unopenable log file should be reported")
	}
}

func TestSaltLevelNamesAreMapped(t *testing.T) {
	// SPEC 26.1 asks that Salt's three extra spellings be read rather
	// than refused, because an operator translating a configuration
	// should not have to look them up.
	for name, want := range map[string]Level{
		"error": Error, "critical": Error, "quiet": Error,
		"warning": Warn, "warn": Warn,
		"info": Info, "profile": Info, "": Info,
		"debug": Debug, "trace": Trace, "garbage": Trace,
	} {
		got, ok := ParseLevel(name)
		if !ok {
			t.Errorf("%q was not recognised", name)
		}
		if got != want {
			t.Errorf("%q = %v, want %v", name, got, want)
		}
	}
	if _, ok := ParseLevel("verbose"); ok {
		t.Error("an unknown level should be reported, not guessed at")
	}
}

// TestSecretsAreScrubbedAtTheSink. SPEC 26.1 puts the redactor at the
// sink so that a value cannot escape through a path that forgot about
// it — which matters because a decrypted pillar value travels through
// code that has no idea what it is holding.
func TestSecretsAreScrubbedAtTheSink(t *testing.T) {
	secrets := redact.New()
	secrets.Add("s3cret-pillar-value")

	var out strings.Builder
	l, _ := New(Options{Level: Info, Stderr: &out, Secrets: secrets})
	fixed(l)

	// In the message.
	l.Info(`the command "mysql -p s3cret-pillar-value" failed`)
	// And in a field, because a secret arrives in whichever one the
	// caller happened to put it in.
	l.Info("a state failed", "comment", "wrote s3cret-pillar-value to disk")

	if strings.Contains(out.String(), "s3cret-pillar-value") {
		t.Errorf("a secret reached the sink:\n%s", out.String())
	}
	if strings.Count(out.String(), redact.Placeholder) != 2 {
		t.Errorf("both occurrences should be replaced:\n%s", out.String())
	}
	// The diagnostic is still a diagnostic.
	if !strings.Contains(out.String(), "mysql -p") {
		t.Errorf("the message was mangled:\n%s", out.String())
	}
}
