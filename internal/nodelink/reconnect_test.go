package nodelink

import (
	"testing"
	"time"
)

// `hub_tries` counts attempts since the stream last opened, not every attempt
// the process has made. DIVERGENCE 5.546.
func TestHubTriesCountsFailuresInARowNotAttemptsEver(t *testing.T) {
	r := NewReconnect(3)
	r.jitter = func() float64 { return 0.5 }

	// A node that holds its stream, loses it, and does so ten times over a
	// year, is never near giving up.
	for i := 0; i < 10; i++ {
		r.Attempting()
		r.Opened()
		if _, giveUp := r.Ended(false); giveUp {
			t.Fatalf("gave up after a stream that opened, on disconnection %d", i+1)
		}
	}
	// Three attempts in a row that never open do.
	var giveUp bool
	for i := 0; i < 3; i++ {
		r.Attempting()
		_, giveUp = r.Ended(false)
	}
	if !giveUp {
		t.Error("did not give up after 3 failures in a row")
	}

	// Zero retries for ever.
	forever := NewReconnect(0)
	for i := 0; i < 100; i++ {
		forever.Attempting()
		if _, stop := forever.Ended(false); stop {
			t.Fatal("hub_tries 0 gave up")
		}
	}
}

// The wait grows through a run of failures, is back at the start after a
// stream opened, and is spread so that a fleet does not return together.
func TestTheReconnectWaitResetsWhenTheStreamOpensAndIsJittered(t *testing.T) {
	r := NewReconnect(0)
	r.jitter = func() float64 { return 0.5 } // the middle: exactly the backoff
	var waits []time.Duration
	for i := 0; i < 8; i++ {
		r.Attempting()
		w, _ := r.Ended(false)
		waits = append(waits, w)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range waits {
		if w != want[i]*time.Second {
			t.Errorf("wait %d = %v, want %v", i, w, want[i]*time.Second)
		}
	}
	r.Attempting()
	r.Opened()
	if w, _ := r.Ended(false); w != time.Second {
		t.Errorf("the wait after a stream that opened is %v, want 1s", w)
	}
	if w, _ := r.Ended(true); w != time.Second {
		t.Errorf("the wait after a clean end is %v, want 1s", w)
	}

	lo, hi := NewReconnect(0), NewReconnect(0)
	lo.jitter, hi.jitter = func() float64 { return 0 }, func() float64 { return 0.999999 }
	a, _ := lo.Ended(false)
	b, _ := hi.Ended(false)
	if a != 800*time.Millisecond || b < 1190*time.Millisecond || b > 1200*time.Millisecond {
		t.Errorf("jitter spans %v to %v, want about 800ms to 1.2s", a, b)
	}
}

// The jitter comes from crypto/rand (SPEC 25.3) and stays in [0, 1).
func TestTheReconnectJitterSourceStaysInRange(t *testing.T) {
	for i := 0; i < 1000; i++ {
		if f := cryptoFloat(); f < 0 || f >= 1 {
			t.Fatalf("cryptoFloat() = %v, want [0, 1)", f)
		}
	}
}
