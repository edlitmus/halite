package log

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// overlapWriter records whether two Writes were ever in progress at
// once. Each Write holds for a moment so that an overlap, if the logger
// allows one, is very likely to happen inside the test rather than
// merely be possible.
type overlapWriter struct {
	inFlight atomic.Int32
	overlaps atomic.Int32
	mu       sync.Mutex
	lines    []string
}

func (w *overlapWriter) Write(p []byte) (int, error) {
	if w.inFlight.Add(1) > 1 {
		w.overlaps.Add(1)
	}
	time.Sleep(200 * time.Microsecond)
	w.mu.Lock()
	w.lines = append(w.lines, string(p))
	w.mu.Unlock()
	w.inFlight.Add(-1)
	return len(p), nil
}

// A logger and the children With makes from it share one writer, so
// they have to share the lock that serialises it. Each had its own
// (issue 614), and a parent and a child writing at once wrote at once.
func TestWithChildrenDoNotWriteAtTheSameTimeAsTheirParent(t *testing.T) {
	w := &overlapWriter{}
	parent, err := New(Options{Level: Info, Format: JSON, Stderr: w})
	if err != nil {
		t.Fatal(err)
	}
	loggers := []*Logger{parent, parent.With("component", "a"), parent.With("component", "b").With("job", "1")}

	var wg sync.WaitGroup
	for _, l := range loggers {
		wg.Add(1)
		go func(l *Logger) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				l.Info("tick")
			}
		}(l)
	}
	wg.Wait()

	if n := w.overlaps.Load(); n != 0 {
		t.Errorf("%d writes overlapped another from a logger sharing the writer", n)
	}
	if len(w.lines) != 150 {
		t.Errorf("wrote %d records, want 150", len(w.lines))
	}
}

// A message with a newline in it keeps its continuation lines under the
// record, indented, rather than starting them at column one where they
// read as records of their own.
func TestConsoleIndentsTheRestOfAMultiLineMessage(t *testing.T) {
	var out strings.Builder
	l, _ := New(Options{Level: Info, Format: Console, Stderr: &out})
	fixed(l)
	l.Warn("command failed: exit 1\nerror: forged record\n", "sls", "web")

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want the record line and one continuation, got:\n%s", out.String())
	}
	if lines[0] != "warn: command failed: exit 1 sls=web" {
		t.Errorf("the record line is %q", lines[0])
	}
	if lines[1] != "    error: forged record" {
		t.Errorf("the continuation is %q, which starts a line of its own", lines[1])
	}
	for _, line := range lines[1:] {
		if !strings.HasPrefix(line, " ") {
			t.Errorf("a line of the record starts at column one: %q", line)
		}
	}
}
