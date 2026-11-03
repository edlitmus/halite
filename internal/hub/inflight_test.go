package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// The contract Serve depends on: wait does not return while a handler
// is running, and returns as soon as the last one finishes.
//
// A hub that reports it has stopped while a handler is still writing a
// return into the job cache is the defect this exists to prevent, and
// it is not observable end to end without losing the race on purpose.
func TestInflightWaitsForEveryHandler(t *testing.T) {
	var f inflight

	// Nothing running: wait is not a delay. And it is final: a handler
	// that arrives afterwards is refused rather than counted, since
	// nothing would wait for it.
	var idle inflight
	done := make(chan struct{})
	go func() { idle.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait blocked with no handler running")
	}
	if idle.enter() {
		t.Error("a handler was let in after wait had returned")
	}

	if !f.enter() || !f.enter() {
		t.Fatal("a handler was refused before anything waited")
	}
	waited := make(chan struct{})
	go func() { f.wait(); close(waited) }()

	select {
	case <-waited:
		t.Fatal("wait returned with two handlers running")
	case <-time.After(20 * time.Millisecond):
	}

	f.leave()
	select {
	case <-waited:
		t.Fatal("wait returned with one handler still running")
	case <-time.After(20 * time.Millisecond):
	}

	f.leave()
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("wait did not return once every handler had finished")
	}
}

// Stopping the hub means the streams are closed by the time Serve
// returns, not shortly afterwards.
func TestServeReturnsOnlyOnceTheStreamsHaveClosed(t *testing.T) {
	l := newLab(t)
	client := l.enrolled(t, "web1.example")
	stop := l.connectSilent(t, client, "web1.example", `{"os":"FreeBSD"}`)
	defer stop()

	if len(l.server.fleet().Connected()) != 1 {
		t.Fatal("the node did not connect")
	}
	l.stop(t)
	if got := l.server.fleet().Connected(); len(got) != 0 {
		t.Errorf("Serve returned with %d stream(s) still attached", len(got))
	}
}

// A request net/http has already read, but whose handler has not yet
// been counted, must not run once Serve has said the hub stopped.
//
// This is the flake on CI run 41682208877: a node's return arrived just
// as a test ended, Serve returned with nothing counted, and the handler
// then wrote the return into a job cache the test was removing. The
// window is between net/http starting the handler and `enter`, which no
// amount of waiting on the count can close, so the hook holds a return
// in it until the hub has stopped and then lets it go.
func TestARequestThatArrivesAsTheHubStopsWritesNothing(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")

	held := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	l.server.requests.arrived = func(r *http.Request) {
		if r.URL.Path != transport.PathReturn {
			return
		}
		once.Do(func() { close(held) })
		<-release
	}

	// The node's side, answering the one job and saying what the hub
	// told it. connect throws that answer away.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	answered := make(chan error, 1)
	go web1.Subscribe(ctx, transport.SubscribeRequest{
		NodeID: "web1.example", Grains: json.RawMessage(`{"os":"FreeBSD"}`),
	}, func(msg transport.Message) error {
		switch msg.T {
		case transport.MsgPing:
			select {
			case <-ready:
			default:
				close(ready)
			}
		case transport.MsgJob:
			answered <- web1.Return(ctx, job.Return{
				JID: job.ID(msg.JID), NodeID: "web1.example", Fun: msg.Fun,
				Success: true, Return: json.RawMessage(`true`), Schema: job.ReturnSchema,
			})
		}
		return nil
	})
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("web1.example never connected")
	}

	res, err := l.operator(t, "ed").Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the return never reached the hub")
	}

	stopped := make(chan struct{})
	go func() { l.stop(t); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the hub did not stop with a request held before it was counted")
	}

	close(release)
	select {
	case err := <-answered:
		if err == nil {
			t.Error("the hub accepted a return after Serve had returned")
		} else if transport.Permanent(err) {
			// A node gives up on a refusal; it must retry this one.
			t.Errorf("a return refused for a stopping hub was refused permanently: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held return was never answered")
	}
	returns, err := l.server.Jobs.Returns(job.ID(res.JID))
	if err != nil {
		t.Fatal(err)
	}
	if len(returns) != 0 {
		t.Errorf("%d return(s) were written after the hub said it had stopped", len(returns))
	}
}
