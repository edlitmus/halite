package hub

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
)

// An idle followed stream holds one channel on the bus, not one more for
// every heartbeat. Each Wait registers a channel only an append frees,
// and the stream asked for a new one each time round, so a stream left
// open on a quiet hub added one every 25 seconds.
func TestAnIdleStreamDoesNotPileUpWaiters(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	op := l.operator(t, "ed")
	l.server.streamHeartbeat = 2 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = op.FollowEvents(ctx, nil, eventbus.Latest, true, 100,
			func(json.RawMessage) error { return nil })
	}()
	// A few hundred heartbeats' worth of silence.
	time.Sleep(500 * time.Millisecond)
	waiting := l.server.Events.Waiting()
	cancel()
	<-done
	if waiting > 1 {
		t.Errorf("one idle stream holds %d channels on the bus", waiting)
	}
	if waiting == 0 {
		t.Error("the stream holds no channel, so it was not following")
	}
}

// A record appended between the stream's read and its wait still wakes
// it. The wait was registered after the read, so a record that landed in
// between sat until the heartbeat, though the comment said otherwise.
func TestARecordAppendedAfterTheReadIsNotMissed(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	op := l.operator(t, "ed")
	// Long enough that a heartbeat cannot rescue a missed wake inside
	// the test's own deadline.
	l.server.streamHeartbeat = 30 * time.Second
	var once sync.Once
	l.server.streamAfterRead = func() {
		once.Do(func() {
			if _, err := l.server.Events.Append(&eventbus.Event{Tag: "halite/test/between"}); err != nil {
				t.Error(err)
			}
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := make(chan string, 1)
	go func() {
		_ = op.FollowEvents(ctx, nil, eventbus.Latest, true, 100,
			func(raw json.RawMessage) error {
				var e eventbus.Event
				if err := json.Unmarshal(raw, &e); err == nil {
					select {
					case got <- e.Tag:
					default:
					}
				}
				return nil
			})
	}()
	select {
	case tag := <-got:
		if tag != "halite/test/between" {
			t.Errorf("the stream delivered %q", tag)
		}
	case <-ctx.Done():
		t.Fatal("a record appended between the read and the wait was not delivered")
	}
}
