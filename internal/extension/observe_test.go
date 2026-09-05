package extension

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/bridge"
)

// call is one observation.
type call struct {
	name, result string
	took         time.Duration
}

type observer struct {
	mu    sync.Mutex
	calls []call
}

func (o *observer) record(name, result string, took time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, call{name, result, took})
}

func (o *observer) results() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]string, 0, len(o.calls))
	for _, c := range o.calls {
		out = append(out, c.result)
	}
	return out
}

// SPEC 26.2 names three families over extension calls and this build
// had none of them: a bridged extension timing out was a job failure
// with no counter behind it.
func TestAnExtensionCallIsObserved(t *testing.T) {
	cache := t.TempDir()
	key := installReal(t, cache)

	store := &Store{Dir: cache, Options: LoadOptions{
		TrustKeys: []TrustKey{key}, RequireSignature: true,
	}}
	installed, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	usable, problems := store.Usable(installed)
	if len(problems) != 0 {
		t.Fatalf("problems loading: %v", problems)
	}

	seen := &observer{}
	rt := &Runtime{
		WorkDirFor: func(string) string { return t.TempDir() },
		Timeout:    20 * time.Second,
		PoolSize:   2,
		Log:        func(name, level, message string) { t.Logf("%s %s: %s", name, level, message) },
		Observe:    seen.record,
	}
	defer rt.Close()
	if err := rt.Add(usable["echo"]); err != nil {
		t.Fatal(err)
	}
	loaded, ok := rt.Get("echo")
	if !ok {
		t.Fatal("echo is not loaded")
	}

	if _, err := loaded.Call(context.Background(), "say", nil,
		map[string]any{"message": "counted"}, nil); err != nil {
		t.Fatal(err)
	}
	// A function the extension does not have is a failed invocation,
	// not an absent one.
	if _, err := loaded.Call(context.Background(), "nosuch", nil, nil, nil); err == nil {
		t.Fatal("a function the extension does not have answered")
	}

	got := seen.results()
	if len(got) != 2 {
		t.Fatalf("%d calls were observed, want 2: %v", len(got), got)
	}
	if got[0] != "succeeded" || got[1] != "failed" {
		t.Errorf("the calls were observed as %v, want [succeeded failed]", got)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	for _, c := range seen.calls {
		if c.name != "echo" {
			t.Errorf("a call was observed against %q", c.name)
		}
		// Non-negative rather than positive. Go's monotonic clock on
		// Windows has a granularity of about half a millisecond, and a
		// call that reuses an already-warm pooled process returns
		// inside that, so `time.Since` reads exactly zero. That is a
		// correct reading of a sub-tick call and a histogram bucketing
		// it at zero is right; a floor applied at the point of
		// measurement would make the counter lie to satisfy a test. A
		// negative duration is the thing that cannot happen, and is
		// what this is for.
		if c.took < 0 {
			t.Errorf("a call was observed as taking %v", c.took)
		}
	}
}

// A timeout is counted apart from a failure, because SPEC 26.2 asks for
// `halite_ext_timeouts_total` separately: an extension that is slow and
// one that is broken need different answers, and both arrive here as a
// non-nil error.
func TestATimeoutIsToldFromAFailure(t *testing.T) {
	seen := &observer{}
	rt := &Runtime{Observe: seen.record}
	loaded := &Loaded{rt: rt, name: "inventory"}

	loaded.observe(time.Now(), nil)
	loaded.observe(time.Now(), errors.New("the extension reported a failure"))
	// Wrapped, which is how it arrives: the bridge adds how long it
	// waited, and a check on the message rather than on the error would
	// stop working the day somebody reworded it.
	loaded.observe(time.Now(), fmt.Errorf("say: %w: it did not answer within 1m0s", bridge.ErrTimeout))

	want := []string{"succeeded", "failed", "timed_out"}
	got := seen.results()
	if len(got) != len(want) {
		t.Fatalf("observed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("observation %d is %q, want %q", i, got[i], want[i])
		}
	}
}

// A runtime with no observer is the normal case: a one-shot command
// line keeps no metrics, and every call site is unconditional.
func TestAnExtensionWithNoObserverIsSafe(t *testing.T) {
	loaded := &Loaded{rt: &Runtime{}, name: "inventory"}
	loaded.observe(time.Now(), nil)
	(&Loaded{name: "orphan"}).observe(time.Now(), nil)
}
