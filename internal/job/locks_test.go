package job

import (
	"sync"
	"testing"
	"time"
)

// A job's mutex goes when nobody holds it. It was kept for every job
// the process had updated, which on a hub is every job since it
// started.
func TestAJobsLockIsFreedWhenNobodyHoldsIt(t *testing.T) {
	c := newCache(t)
	const jobs = 50
	start := time.Now()
	for i := 0; i < jobs; i++ {
		j := dispatched(t, c, start.Add(time.Duration(i)*time.Second))
		var wg sync.WaitGroup
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.Update(j.JID, func(*Job) error { return nil }); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
	}
	c.mu.Lock()
	held := len(c.locks)
	c.mu.Unlock()
	if held != 0 {
		t.Errorf("%d job locks are still held after %d jobs finished updating", held, jobs)
	}
}

// Freeing an entry must not free it from under somebody queued on it.
// If the release removed the entry while B was still waiting, B would
// go on to hold the old mutex and C, arriving next, would make a new one
// and get in beside B: two writers on one record, which is what the
// lock is for. Forced in order rather than raced for, because the
// hammering tests above do not hit the window (they passed ten times
// over with exactly that defect put in).
func TestAJobsLockIsNotFreedFromUnderAWaiter(t *testing.T) {
	c := newCache(t)
	id := NewID(time.Now())

	releaseA := c.lock(id)
	bGot := make(chan func())
	go func() { bGot <- c.lock(id) }()
	for deadline := time.Now().Add(5 * time.Second); ; {
		c.mu.Lock()
		waiting := c.locks[id] != nil && c.locks[id].users == 2
		c.mu.Unlock()
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("B never queued")
		}
		time.Sleep(time.Millisecond)
	}
	releaseA()
	releaseB := <-bGot

	cGot := make(chan func(), 1)
	go func() { cGot <- c.lock(id) }()
	select {
	case release := <-cGot:
		release()
		releaseB()
		t.Fatal("C took the job's lock while B held it")
	case <-time.After(200 * time.Millisecond):
	}
	releaseB()
	(<-cGot)()

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.locks) != 0 {
		t.Errorf("%d entries left after every holder let go", len(c.locks))
	}
}
