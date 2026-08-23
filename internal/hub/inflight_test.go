package hub

import (
	"testing"
	"time"
)

// The contract Serve depends on: wait does not return while a handler
// is running, and returns as soon as the last one finishes.
//
// A hub that reports it has stopped while a handler is still writing a
// return into the job cache is the defect this exists to prevent, and
// it is not observable end to end without losing the race on purpose.
func TestInflightWaitsForEveryHandler(t *testing.T) {
	var f inflight

	// Nothing running: wait is not a delay.
	done := make(chan struct{})
	go func() { f.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("wait blocked with no handler running")
	}

	f.enter()
	f.enter()
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
