package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestALevelFiltersEverythingBelowIt(t *testing.T) {
	cases := map[Level][]string{
		Error: {"ERROR"},
		Warn:  {"ERROR", "WARN"},
		Info:  {"ERROR", "WARN", "INFO"},
		Debug: {"ERROR", "WARN", "INFO", "DEBUG"},
	}
	for level, want := range cases {
		var written strings.Builder
		logger := New(&written, "", level)
		logger.Errorf("an error")
		logger.Warnf("a warning")
		logger.Infof("a note")
		logger.Debugf("a detail")

		for _, token := range []string{"ERROR", "WARN", "INFO", "DEBUG"} {
			present := strings.Contains(written.String(), token)
			expected := false
			for _, w := range want {
				if w == token {
					expected = true
				}
			}
			if present != expected {
				t.Errorf("at %s: %s present=%v, want %v (log: %q)",
					level, token, present, expected, written.String())
			}
		}
	}
}

func TestParseLevelAcceptsAnyCaseAndRejectsTheRest(t *testing.T) {
	for _, name := range []string{"error", "WARN", "Info", "dEbUg"} {
		if _, err := ParseLevel(name); err != nil {
			t.Errorf("ParseLevel(%q): %v", name, err)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Error("ParseLevel accepted a level that does not exist")
	}
}

// A daemon that logs at the default level must still say the things the
// fleet has always printed, so upgrading does not silently quiet a host.
func TestTheDefaultLevelKeepsInfo(t *testing.T) {
	var written strings.Builder
	New(&written, "", DefaultLevel).Infof("connected")
	if !strings.Contains(written.String(), "connected") {
		t.Errorf("the default level dropped an info line: %q", written.String())
	}
}

func TestOpenAppendsRatherThanTruncating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "halite.log")

	first, err := Open(path, "halite-master ", Info)
	if err != nil {
		t.Fatal(err)
	}
	first.Infof("the first run")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(path, "halite-master ", Info)
	if err != nil {
		t.Fatal(err)
	}
	second.Infof("the second run")
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	body := readFile(t, path)
	for _, want := range []string{"the first run", "the second run", "halite-master "} {
		if !strings.Contains(body, want) {
			t.Errorf("log is missing %q: %q", want, body)
		}
	}
}

// This is the newsyslog(8) and logrotate handshake: the file is renamed
// out from under the daemon, which must start writing to the name again
// rather than to the file that moved.
func TestReopenWritesToTheNameAgainAfterARotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "halite.log")
	logger, err := Open(path, "", Info)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	logger.Infof("before the rotation")
	if err := os.Rename(path, path+".0"); err != nil {
		t.Fatal(err)
	}
	if err := logger.Reopen(); err != nil {
		t.Fatal(err)
	}
	logger.Infof("after the rotation")

	if body := readFile(t, path); !strings.Contains(body, "after the rotation") {
		t.Errorf("the reopened log did not get the new line: %q", body)
	} else if strings.Contains(body, "before the rotation") {
		t.Errorf("the reopened log kept the rotated content: %q", body)
	}
	if body := readFile(t, path+".0"); !strings.Contains(body, "before the rotation") {
		t.Errorf("the rotated file lost what was written before it moved: %q", body)
	}
}

// A Logger on stderr owns nothing to reopen, and SIGHUP arrives all the
// same — it must not be an error.
func TestReopenIsHarmlessWithoutAFile(t *testing.T) {
	if err := New(&strings.Builder{}, "", Info).Reopen(); err != nil {
		t.Errorf("Reopen on a borrowed destination: %v", err)
	}
}

func TestStdLoggerFilesLinesAtItsLevel(t *testing.T) {
	var written strings.Builder
	logger := New(&written, "", Warn)
	logger.StdLogger(Warn).Print("http: TLS handshake error")
	logger.StdLogger(Debug).Print("this one is below the level")

	if !strings.Contains(written.String(), "WARN") ||
		!strings.Contains(written.String(), "TLS handshake error") {
		t.Errorf("the warning did not reach the log: %q", written.String())
	}
	if strings.Contains(written.String(), "below the level") {
		t.Errorf("a debug line was written at level warn: %q", written.String())
	}
}

// The daemons log from every handler and from each background runner at
// once, so an interleaved line is a real failure mode.
func TestConcurrentWritesStayWholeLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "halite.log")
	logger, err := Open(path, "halite-master ", Debug)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			logger.Infof("job %d dispatched", n)
		}(i)
	}
	wg.Wait()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimRight(readFile(t, path), "\n"), "\n")
	if len(lines) != 50 {
		t.Fatalf("want 50 lines, got %d", len(lines))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "halite-master ") || !strings.Contains(line, "INFO  job ") {
			t.Errorf("a line came out mangled: %q", line)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
