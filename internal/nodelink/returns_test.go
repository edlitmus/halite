package nodelink

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// poster answers each post with the next error in its script, then nil.
type poster struct {
	script []error
	calls  int
}

func (p *poster) Return(context.Context, any) error {
	p.calls++
	if p.calls <= len(p.script) {
		return p.script[p.calls-1]
	}
	return nil
}

func noDelay(t *testing.T) {
	old := retryDelay
	retryDelay = func(int) time.Duration { return 0 }
	t.Cleanup(func() { retryDelay = old })
}

var blinked = errors.New("connection reset by peer")

// A failure a retry may fix is retried, and each is said to the caller.
func TestAReturnThatFailsTwiceIsPostedOnTheThird(t *testing.T) {
	noDelay(t)
	p := &poster{script: []error{blinked, blinked}}
	var told int
	if err := PostReturn(context.Background(), p, "ret", func(error) { told++ }); err != nil {
		t.Fatalf("PostReturn = %v, want nil", err)
	}
	if p.calls != 3 || told != 2 {
		t.Errorf("posted %d times and told of %d failures, want 3 and 2", p.calls, told)
	}
}

// A refusal is the hub's answer, and is not argued with.
func TestARefusedReturnIsNotRetried(t *testing.T) {
	noDelay(t)
	refused := &transport.RefusedError{Path: transport.PathReturn, Status: 404, Code: transport.CodeRefused, Msg: "no such job"}
	p := &poster{script: []error{refused}}
	err := PostReturn(context.Background(), p, "ret", func(error) { t.Error("a refusal was reported as a failure to retry") })
	if !transport.Permanent(err) || p.calls != 1 {
		t.Errorf("PostReturn = %v after %d posts, want the refusal after 1", err, p.calls)
	}
}

// Five failures, and the return is given up, saying why.
func TestAReturnIsGivenUpAfterFiveFailures(t *testing.T) {
	noDelay(t)
	p := &poster{script: []error{blinked, blinked, blinked, blinked, blinked, blinked}}
	err := PostReturn(context.Background(), p, "ret", nil)
	if !errors.Is(err, ErrGaveUp) || !errors.Is(err, blinked) {
		t.Errorf("PostReturn = %v, want ErrGaveUp wrapping the last failure", err)
	}
	// Five, written out rather than ReturnTries, because the number is
	// the node's behaviour and a test against the constant passes
	// whatever the constant is.
	if p.calls != 5 {
		t.Errorf("posted %d times, want 5", p.calls)
	}
}

// The waits grow a second at a time, as the node's always have: 1 s, 2 s,
// 3 s, 4 s, 5 s.
func TestTheReturnRetryWaitsGrow(t *testing.T) {
	for i, want := range []time.Duration{1, 2, 3, 4, 5} {
		if got := retryDelay(i); got != want*time.Second {
			t.Errorf("wait after failure %d = %v, want %v", i+1, got, want*time.Second)
		}
	}
}

// A node stopping does not wait out a retry.
func TestAStoppedNodeStopsRetrying(t *testing.T) {
	old := retryDelay
	retryDelay = func(int) time.Duration { return time.Hour }
	t.Cleanup(func() { retryDelay = old })
	ctx, cancel := context.WithCancel(context.Background())
	p := &poster{script: []error{blinked}}
	done := make(chan error, 1)
	go func() { done <- PostReturn(ctx, p, "ret", nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("PostReturn = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PostReturn went on waiting after its context ended")
	}
}
