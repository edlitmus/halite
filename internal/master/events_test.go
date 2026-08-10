package master

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// tail opens an event stream and returns a channel of decoded events. The
// stream closes when the returned cancel runs.
func tail(t *testing.T, client *transport.Client, query string) (<-chan transport.Event, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	body, err := client.Stream(ctx, transport.PathEvents+query)
	if err != nil {
		cancel()
		t.Fatalf("open event stream: %v", err)
	}

	events := make(chan transport.Event, 64)
	go func() {
		defer close(events)
		defer body.Close()
		scanner := bufio.NewScanner(body)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var ev transport.Event
			if json.Unmarshal([]byte(line), &ev) == nil {
				events <- ev
			}
		}
	}()
	return events, func() {
		cancel()
		// Drain so the goroutine can finish against a closed body.
		for range events {
		}
	}
}

func waitForTag(t *testing.T, events <-chan transport.Event, pattern string) transport.Event {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, open := <-events:
			if !open {
				t.Fatalf("stream closed before %q arrived", pattern)
			}
			if matchesTag(pattern, ev.Tag) {
				return ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q", pattern)
		}
	}
}

// matchesTag is the test's own matcher, kept trivial on purpose: the bus
// matcher has its own tests, and reusing it here would hide a bug in it.
func matchesTag(pattern, tag string) bool {
	prefix, ok := strings.CutSuffix(pattern, "**")
	if !ok {
		return pattern == tag
	}
	return strings.HasPrefix(tag, prefix)
}

func TestEventStreamCarriesTheJobLifecycle(t *testing.T) {
	f := newFleet(t, Config{PollTimeout: time.Second})
	admin := f.adminClient(t, "ed")
	events, done := tail(t, admin, "")
	defer done()

	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1", "os_family": "FreeBSD"})

	helloEvent := waitForTag(t, events, "halite/agent/web1/hello")
	if helloEvent.Source != "web1" {
		t.Errorf("hello source = %q, want web1", helloEvent.Source)
	}

	var resp transport.DispatchResponse
	if err := admin.Post(context.Background(), transport.PathDispatch,
		transport.DispatchRequest{Target: "*", Kind: transport.KindGrains}, &resp); err != nil {
		t.Fatal(err)
	}
	dispatch := waitForTag(t, events, "halite/job/"+resp.JobID+"/dispatch")
	if dispatch.Source != "ed" {
		t.Errorf("dispatch source = %q, want the operator", dispatch.Source)
	}
	if dispatch.Data["kind"] != transport.KindGrains {
		t.Errorf("dispatch data = %v", dispatch.Data)
	}

	if err := web.Post(context.Background(), transport.PathResults,
		transport.JobResult{JobID: resp.JobID, Ok: true, Succeeded: 1}, nil); err != nil {
		t.Fatal(err)
	}
	ret := waitForTag(t, events, "halite/job/"+resp.JobID+"/ret/web1")
	if ret.Data["result"] != true {
		t.Errorf("return data = %v", ret.Data)
	}
	if _, present := ret.Data["error"]; present {
		t.Errorf("a successful return must not carry an error field: %v", ret.Data)
	}
}

func TestEventStreamFiltersByTag(t *testing.T) {
	f := newFleet(t, Config{})
	admin := f.adminClient(t, "ed")
	events, done := tail(t, admin, "?tag=halite/job/**")
	defer done()

	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})

	var resp transport.DispatchResponse
	if err := admin.Post(context.Background(), transport.PathDispatch,
		transport.DispatchRequest{Target: "*", Kind: transport.KindGrains}, &resp); err != nil {
		t.Fatal(err)
	}

	// The hello came first; a filtered stream must skip it and deliver the
	// dispatch as its first event.
	select {
	case ev := <-events:
		if !strings.HasPrefix(ev.Tag, "halite/job/") {
			t.Fatalf("got %q, want only job events", ev.Tag)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no job event arrived")
	}
}

func TestEventStreamReplaysHistoryOnRequest(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})
	admin := f.adminClient(t, "ed")

	// The hello happened before this stream existed.
	events, done := tail(t, admin, "?history=0")
	defer done()

	replayed := waitForTag(t, events, "halite/agent/web1/hello")
	if replayed.Source != "web1" {
		t.Errorf("replayed event = %+v", replayed)
	}
}

func TestEventStreamStartsFromNowByDefault(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})
	admin := f.adminClient(t, "ed")

	events, done := tail(t, admin, "")
	defer done()

	select {
	case ev := <-events:
		t.Errorf("a default tail replayed %q; it should start from now", ev.Tag)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestAgentsCannotStreamEvents(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")

	_, err := web.Stream(context.Background(), transport.PathEvents)
	if err == nil {
		t.Fatal("an agent certificate must not be able to read the fleet's events")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("got %v, want 403", err)
	}
}

func TestAgentsCanRaiseTheirOwnEvents(t *testing.T) {
	f := newFleet(t, Config{})
	admin := f.adminClient(t, "ed")
	events, done := tail(t, admin, "")
	defer done()

	web := f.enrolledClient(t, "web1")
	err := web.Post(context.Background(), transport.PathEvents, transport.Event{
		Tag:    "halite/beacon/web1/disk-full",
		Source: "master", // a lie, and it must not be believed
		Data:   map[string]any{"mount": "/var"},
	}, nil)
	if err != nil {
		t.Fatalf("agent event: %v", err)
	}

	ev := waitForTag(t, events, "halite/beacon/web1/disk-full")
	if ev.Source != "web1" {
		t.Errorf("source = %q; it must come from the certificate, not the body", ev.Source)
	}
	if ev.Data["mount"] != "/var" {
		t.Errorf("data = %v", ev.Data)
	}
}

func TestUntaggedAgentEventIsRejected(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")

	err := web.Post(context.Background(), transport.PathEvents,
		transport.Event{Data: map[string]any{"x": 1}}, nil)
	if err == nil {
		t.Fatal("an event without a tag must be rejected")
	}
}

func TestEventStreamEndsWhenTheClientGoesAway(t *testing.T) {
	f := newFleet(t, Config{})
	admin := f.adminClient(t, "ed")

	ctx, cancel := context.WithCancel(context.Background())
	body, err := client(t, admin, ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	// Reading a cancelled stream must end rather than hang.
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, body)
		body.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not end after the client cancelled")
	}
}

func client(t *testing.T, c *transport.Client, ctx context.Context) (io.ReadCloser, error) {
	t.Helper()
	return c.Stream(ctx, transport.PathEvents)
}

func TestReactorCausedWorkIsMarkedInBothItsEvents(t *testing.T) {
	// A rule matching job events is the classic runaway: the job it
	// dispatches raises a dispatch event *and* a return event, and either
	// one feeding back is enough to loop. Both must carry the mark.
	f := newFleet(t, Config{})
	admin := f.adminClient(t, "ed")
	events, done := tail(t, admin, "")
	defer done()

	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})

	resp, err := f.server.Dispatch(
		transport.DispatchRequest{Target: "*", Kind: transport.KindGrains}, reactorSource)
	if err != nil {
		t.Fatal(err)
	}

	dispatch := waitForTag(t, events, "halite/job/"+resp.JobID+"/dispatch")
	if marked, _ := dispatch.Data["reactor"].(bool); !marked {
		t.Errorf("dispatch event is not marked as the reactor's: %v", dispatch.Data)
	}

	if err := web.Post(context.Background(), transport.PathResults,
		transport.JobResult{JobID: resp.JobID, Ok: true}, nil); err != nil {
		t.Fatal(err)
	}
	ret := waitForTag(t, events, "halite/job/"+resp.JobID+"/ret/web1")
	if marked, _ := ret.Data["reactor"].(bool); !marked {
		t.Errorf("return event is not marked as the reactor's: %v", ret.Data)
	}
}

func TestOperatorWorkIsNotMarkedAsTheReactors(t *testing.T) {
	f := newFleet(t, Config{})
	admin := f.adminClient(t, "ed")
	events, done := tail(t, admin, "")
	defer done()

	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})

	var resp transport.DispatchResponse
	if err := admin.Post(context.Background(), transport.PathDispatch,
		transport.DispatchRequest{Target: "*", Kind: transport.KindGrains}, &resp); err != nil {
		t.Fatal(err)
	}
	dispatch := waitForTag(t, events, "halite/job/"+resp.JobID+"/dispatch")
	if _, marked := dispatch.Data["reactor"]; marked {
		t.Error("an operator's dispatch must not be marked as the reactor's work")
	}
}

func TestAgentCannotRaiseAnotherHostsBeaconTag(t *testing.T) {
	// The source is stamped from the certificate, but reactor rules match
	// on the tag — so a forged tag would fire a rule written for db1.
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")

	refused := []string{
		"halite/beacon/db1/service",    // another agent's beacon
		"halite/job/123/ret/web1",      // the control plane's namespace
		"halite/agent/web1/hello",      // ditto
		"halite/beacon/web1",           // too short
		"halite/beacon/web1/svc/extra", // too long
		"halite/beacon/web1/",          // no name
		"anything/else",                // outside the scheme
	}
	for _, tag := range refused {
		err := web.Post(context.Background(), transport.PathEvents,
			transport.Event{Tag: tag, Data: map[string]any{"x": 1}}, nil)
		if err == nil {
			t.Errorf("%q was accepted from web1", tag)
			continue
		}
		if !strings.Contains(err.Error(), "403") {
			t.Errorf("%q: got %v, want 403", tag, err)
		}
	}

	// Its own beacon tag still works.
	if err := web.Post(context.Background(), transport.PathEvents,
		transport.Event{Tag: "halite/beacon/web1/disk"}, nil); err != nil {
		t.Errorf("web1 could not raise its own beacon: %v", err)
	}
}
