package atomicfile

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// No more than MaxConcurrentWrites writes are inside WritePrepared at
// once, however many are asked for -- and as many as that are, so the
// bound has not quietly become a lock.
//
// The other half of TestWritesHoldingThreadsAreBounded, without the child
// and on every platform: what it counts is writers inside the bound, and a
// writer waiting in the hook is one, whatever the disk does. The hook holds
// every writer that reaches it until the bound is full and a moment more,
// so that an unbounded WritePrepared lets all of them in. A hook that slept
// for a fixed time instead let an unbounded one reach 41 of 192 on a
// laptop, which passed with the bound removed.
func TestConcurrentWritesAreBounded(t *testing.T) {
	var in, most atomic.Int64
	release := make(chan struct{})
	beforeAttributes = func(string) {
		now := in.Add(1)
		for {
			seen := most.Load()
			if now <= seen || most.CompareAndSwap(seen, now) {
				break
			}
		}
		<-release
		in.Add(-1)
	}
	defer func() { beforeAttributes = nil }()

	dir := t.TempDir()
	writers := 3 * MaxConcurrentWrites
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			errs <- Write(filepath.Join(dir, fmt.Sprintf("f%03d", i)), []byte("x"), 0o600)
		})
	}
	deadline := time.Now().Add(30 * time.Second)
	for most.Load() < MaxConcurrentWrites && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Room for the writers past the bound to arrive, if nothing stops them.
	time.Sleep(300 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%d writers, at most %d inside at once", writers, most.Load())
	if got := most.Load(); got > MaxConcurrentWrites {
		t.Errorf("%d writes were in progress at once, past the bound of %d", got, MaxConcurrentWrites)
	}
	if got := most.Load(); got < MaxConcurrentWrites {
		t.Errorf("at most %d writes were in progress at once, short of the bound of %d", got, MaxConcurrentWrites)
	}
}
