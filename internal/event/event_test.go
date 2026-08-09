package event

import (
	"fmt"
	"testing"
	"time"
)

func TestTagMatch(t *testing.T) {
	cases := []struct {
		pattern, tag string
		want         bool
	}{
		{"halite/job/123/ret/web1", "halite/job/123/ret/web1", true},
		{"halite/job/*/ret/*", "halite/job/123/ret/web1", true},
		{"halite/job/*/ret/*", "halite/job/123/dispatch", false},
		// A single star stays inside its segment.
		{"halite/job/*", "halite/job/123/ret/web1", false},
		{"halite/job/*", "halite/job/123", true},
		// A double star crosses segments, including none at all.
		{"halite/job/**", "halite/job/123/ret/web1", true},
		{"halite/job/**", "halite/job", true},
		{"**", "anything/at/all", true},
		{"halite/**/ret/*", "halite/job/123/ret/web1", true},
		// Partial globs within a segment.
		{"halite/agent/web*/hello", "halite/agent/web1/hello", true},
		{"halite/agent/web*/hello", "halite/agent/db1/hello", false},
		{"halite/agent/*1/hello", "halite/agent/web1/hello", true},
		// Length must still line up.
		{"halite/job", "halite/job/123", false},
		{"halite/job/123", "halite/job", false},
	}
	for _, c := range cases {
		if got := TagMatch(c.pattern, c.tag); got != c.want {
			t.Errorf("TagMatch(%q, %q) = %v, want %v", c.pattern, c.tag, got, c.want)
		}
	}
}

func TestSubscriberReceivesMatchingEventsOnly(t *testing.T) {
	bus := NewBus()
	jobs, cancel := bus.Subscribe("halite/job/**")
	defer cancel()

	bus.Emit("halite/agent/web1/hello", "web1", nil)
	bus.Emit("halite/job/1/dispatch", SourceMaster, map[string]any{"kind": "state.highstate"})

	select {
	case ev := <-jobs:
		if ev.Tag != "halite/job/1/dispatch" {
			t.Errorf("got %q, want the job event", ev.Tag)
		}
		if ev.Data["kind"] != "state.highstate" {
			t.Errorf("data lost: %v", ev.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("no event delivered")
	}

	select {
	case ev := <-jobs:
		t.Errorf("unexpected second event %q", ev.Tag)
	default:
	}
}

func TestPublishFillsInIdentityFields(t *testing.T) {
	bus := NewBus()
	all, cancel := bus.Subscribe("")
	defer cancel()

	bus.Publish(Event{Tag: "halite/test"})
	ev := <-all

	if ev.ID == "" {
		t.Error("no id assigned")
	}
	if ev.Time.IsZero() {
		t.Error("no timestamp assigned")
	}
	if ev.Source != SourceMaster {
		t.Errorf("source = %q, want %q", ev.Source, SourceMaster)
	}
}

func TestSlowSubscriberDropsRatherThanBlocks(t *testing.T) {
	bus := NewBus()
	bus.buffer = 4
	_, cancel := bus.Subscribe("**") // never read from
	defer cancel()

	// Far more than the buffer holds. If Publish blocked, this would hang
	// and the test would time out rather than fail.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			bus.Emit("halite/test", SourceMaster, nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a subscriber that stopped reading")
	}

	if dropped := bus.Dropped(); dropped != 96 {
		t.Errorf("dropped = %d, want 96 (100 published, 4 buffered)", dropped)
	}
}

func TestUnsubscribeClosesTheChannelAndStopsDelivery(t *testing.T) {
	bus := NewBus()
	events, cancel := bus.Subscribe("**")

	bus.Emit("halite/test", SourceMaster, nil)
	<-events
	cancel()

	bus.Emit("halite/test", SourceMaster, nil)
	if _, open := <-events; open {
		t.Error("channel delivered after cancel")
	}
	// A second cancel must not panic on a closed channel.
	cancel()
}

func TestHistoryReplaysMatchingEventsOldestFirst(t *testing.T) {
	bus := NewBus()
	for i := 0; i < 5; i++ {
		bus.Emit(fmt.Sprintf("halite/job/%d/dispatch", i), SourceMaster, nil)
		bus.Emit("halite/agent/web1/hello", "web1", nil)
	}

	jobs := bus.History("halite/job/**", 0)
	if len(jobs) != 5 {
		t.Fatalf("got %d job events, want 5", len(jobs))
	}
	if jobs[0].Tag != "halite/job/0/dispatch" || jobs[4].Tag != "halite/job/4/dispatch" {
		t.Errorf("wrong order: %s ... %s", jobs[0].Tag, jobs[4].Tag)
	}

	if limited := bus.History("halite/job/**", 2); len(limited) != 2 ||
		limited[0].Tag != "halite/job/3/dispatch" {
		t.Errorf("limit should keep the most recent: %v", limited)
	}
}

func TestHistoryIsBounded(t *testing.T) {
	bus := NewBus()
	bus.maxHistory = 10
	for i := 0; i < 50; i++ {
		bus.Emit("halite/test", SourceMaster, nil)
	}
	if kept := len(bus.History("**", 0)); kept != 10 {
		t.Errorf("history holds %d events, want the cap of 10", kept)
	}
}

func TestEventIDsAreOrdered(t *testing.T) {
	bus := NewBus()
	for i := 0; i < 20; i++ {
		bus.Emit("halite/test", SourceMaster, nil)
	}
	history := bus.History("**", 0)
	for i := 1; i < len(history); i++ {
		if history[i].ID <= history[i-1].ID {
			t.Fatalf("ids not increasing: %q then %q", history[i-1].ID, history[i].ID)
		}
	}
}

func TestConcurrentPublishAndSubscribe(t *testing.T) {
	bus := NewBus()
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				bus.Emit("halite/test", SourceMaster, nil)
			}
		}
	}()

	// Churning subscriptions against a live publisher is where a bus with a
	// racy lock falls over; -race turns that into a failure.
	for i := 0; i < 50; i++ {
		events, cancel := bus.Subscribe("**")
		select {
		case <-events:
		case <-time.After(time.Second):
		}
		cancel()
	}
	close(stop)
}
