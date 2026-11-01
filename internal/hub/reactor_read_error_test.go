package hub

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
)

// A read error that says nothing about the position leaves it alone.
//
// Every error from the bus other than a lagged position was answered like a
// corrupt offset file: jump to the end of the bus and read again at once. A
// segment that could not be read for a moment therefore skipped every event
// between the offset and now, which are reactions that never fire for events
// that will not come again, and a persistent failure was retried with no
// pause and no look at the context. DIVERGENCE 5.519.
func TestAnUnreadableBusDoesNotMoveTheReactorsOffset(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a file this process cannot open, which root and Windows can")
	}
	l, r, _ := reactorLab(t, map[string]string{
		"audit.sls": "record_it:\n  runner.event.send:\n    - args:\n        tag: halite/audit/saw_it\n",
	}, `
reactor:
  - tag: 'halite/node/*/start'
    sls:
      - $DIR/audit.sls
    principal: 'cert:CN=ed'
`)
	l.server.Policy = labPolicy(t)
	// A bus in a directory this test knows, so its segments can be found.
	busDir := t.TempDir()
	bus, err := eventbus.Open(busDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bus.Close() })
	l.server.Events = bus
	offsetFile := filepath.Join(t.TempDir(), "reactor.offset")
	r.OffsetFile = offsetFile
	r.writeOffset(eventbus.Earliest)
	l.server.emit("halite/node/web1.example/start", "web1.example", nil)
	start := r.readOffset()

	files, _ := filepath.Glob(filepath.Join(busDir, "*"))
	if len(files) == 0 {
		t.Fatal("found no event segments to make unreadable")
	}
	for _, f := range files {
		if err := os.Chmod(f, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(f, 0o600)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	stopped := make(chan struct{})
	go func() { _ = r.Run(ctx); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		// The old loop never looked at the context on this path.
		t.Fatal("the reactor did not stop when its context ended")
	}
	if got := strings.TrimSpace(r.readOffset()); got != start {
		t.Errorf("a read error moved the offset from %q to %q", start, got)
	}

	// Readable again, a new reactor on the same offset reacts to the event
	// it could not read: nothing was skipped.
	for _, f := range files {
		os.Chmod(f, 0o600)
	}
	again := &Reactor{Server: l.server, Entries: l.server.Reactors, Workers: 2,
		Now: l.server.now, OffsetFile: offsetFile}
	runReactor(t, again)
	waitFor(t, 10*time.Second, "the reaction to the event the first reactor could not read", func() bool {
		events, _, err := bus.Read(eventbus.Earliest, []string{"halite/audit/saw_it"}, 10)
		return err == nil && len(events) > 0
	})
}
