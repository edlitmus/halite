package hub

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
)

// A reactor that has stopped has no reaction still running.
//
// `halite-hub serve` started the reactor as a bare goroutine and its
// workers as more, and Run returned the moment its own loop saw the
// context end, whatever the workers were doing. A runner reaction in
// flight then finished after the hub had logged "the hub stopped" and
// returned: its `ret` event and its job return were written by a process
// that had said it was done, and in a test the directory under them was
// already being removed. DIVERGENCE 5.567.
//
// The runner here holds until the test lets it go, and the test lets it
// go only once it has seen whether Run waited. A Run that does not wait
// returns at once and the hold is released after; a Run that waits is
// released after the short window, and returns only when the reaction
// has finished.
func TestAReactionInFlightWhenTheHubStopsFinishesBeforeTheReactorReturns(t *testing.T) {
	l, r, _ := reactorLab(t, map[string]string{
		"hold.sls": "hold_it:\n  runner.test.hold: []\n",
	}, `
reactor:
  - tag: 'halite/beacon/**'
    sls:
      - $DIR/hold.sls
    principal: 'cert:CN=ed'
`)
	l.server.Policy = labPolicy(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	l.server.runners().Add(RunnerModule{
		Reach: ReachHub,
		Sig:   runnerSig("test", "hold", "Hold until released.", "19.2"),
		Fn: func(*RunnerContext) (any, error) {
			close(entered)
			// Not the context: this is a runner that does not stop the
			// moment it is asked to, which is any runner part way through
			// writing something.
			<-release
			return "held", nil
		},
	})

	offsetFile := filepath.Join(t.TempDir(), "reactor.offset")
	r.OffsetFile = offsetFile
	before := r.concrete(eventbus.Latest)
	r.writeOffset(before)
	var handled atomic.Bool
	r.Handled = func(string, []ReactionResult) { handled.Store(true) }

	l.server.emit("halite/beacon/web1.example/inotify/etc/thing", "web1.example",
		map[string]any{"path": "/etc/thing"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	var finishedAtStop bool
	go func() {
		defer close(stopped)
		_ = r.Run(ctx)
		finishedAtStop = handled.Load()
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner reaction never started")
	}

	// The hub stops, as `serve` stops it: Serve has returned, and the
	// context the reactor runs under has ended.
	l.stop(t)
	cancel()
	select {
	case <-stopped:
	case <-time.After(500 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the reactor did not return once its reaction had finished")
	}

	if !finishedAtStop {
		// Show the write rather than infer it: the reaction goes on to
		// record its return into the job cache of a hub that has stopped.
		deadline := time.Now().Add(5 * time.Second)
		for !handled.Load() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		jobs, _ := l.server.Jobs.List(0)
		var written int
		for _, j := range jobs {
			rets, _ := l.server.Jobs.Returns(j.JID)
			written += len(rets)
		}
		t.Errorf("the reactor returned with a reaction still running; "+
			"it then wrote %d job return(s) after the hub had stopped", written)
	}

	// Stopped part way, the reaction is not counted as done: the next
	// start reads its event again (DIVERGENCE 5.495).
	if got := r.readOffset(); got != before {
		t.Errorf("the reactor's offset moved past an event whose reaction was cut short "+
			"by the stop: %q, want %q", got, before)
	}
}
