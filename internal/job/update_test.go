package job

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/chaos"
)

// Get, change, Put loses one of two concurrent changes, and this proves
// it rather than asserting it.
//
// The interleaving is forced rather than raced for, because the window
// is narrow enough that a hammering test passes on a fast machine and
// fails in CI — which is exactly how the defect this guards reached
// main. Two goroutines each read the record, each change a different
// field, each write the whole file back. The second write wins entirely,
// and the first change is gone.
//
// This test asserts the loss, so it stands as the reason `Update`
// exists. If a later change to `Put` made this pass, `Put` would have
// grown a guarantee it does not advertise and this file would be the
// place to find out.
func TestGetThenPutLosesOneOfTwoChanges(t *testing.T) {
	// The shape SPEC 31 does not name and this build has been wrong
	// about three times. internal/chaos says why it is in the registry.
	sc := chaos.Exercises(chaos.ConcurrentBookkeeping)
	t.Logf("%s: %s", sc.Key, sc.Behaviour)
	t.Logf("%s: not established: %s", sc.Key, sc.Limit)

	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example")
	j.Queued = []string{"web1.example"}
	if err := c.Put(j); err != nil {
		t.Fatal(err)
	}

	// Both read the record as it stands.
	clearing, err := c.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	completing, err := c.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}

	// One clears the spool: the node has been given the job.
	clearing.Dequeue("web1.example")
	if err := c.Put(clearing); err != nil {
		t.Fatal(err)
	}
	// The other marks it complete: the node's return has arrived.
	completing.State = Complete
	if err := c.Put(completing); err != nil {
		t.Fatal(err)
	}

	got, err := c.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Complete {
		t.Fatalf("the second write did not land at all: %+v", got)
	}
	if !got.IsQueuedFor("web1.example") {
		t.Fatal("Put no longer loses the first change. If that is deliberate, " +
			"this test is the place to say so — and job.Cache.Update's reason " +
			"for existing needs rewriting.")
	}
	// Which is the defect in one line: the node is still owed a job it
	// has already been given and has already answered, so its next
	// connection runs the job a second time.
}

// Update keeps both changes, on the same forced interleaving.
//
// `mutate` runs inside the lock and against the record as it is on disk,
// so the second writer sees the first writer's change rather than a copy
// taken before it.
func TestUpdateKeepsBothChanges(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example")
	j.Queued = []string{"web1.example"}
	if err := c.Put(j); err != nil {
		t.Fatal(err)
	}

	// The first writer holds the lock inside its mutator until the
	// second has definitely tried to take it, which is the interleaving
	// that loses a change when there is no lock.
	inside := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := c.Update(j.JID, func(cur *Job) error {
			close(inside)
			<-release
			cur.Dequeue("web1.example")
			return nil
		}); err != nil {
			t.Error(err)
		}
	}()

	<-inside
	wg.Add(1)
	var sawTheDequeue bool
	go func() {
		defer wg.Done()
		if _, err := c.Update(j.JID, func(cur *Job) error {
			sawTheDequeue = !cur.IsQueuedFor("web1.example")
			cur.State = Complete
			return nil
		}); err != nil {
			t.Error(err)
		}
	}()

	// Give the second writer time to block on the lock, then let the
	// first finish. A sleep is the honest way to say "it is waiting":
	// there is nothing to observe from outside, and being wrong about
	// it makes the test weaker rather than flaky.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	got, err := c.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Complete {
		t.Errorf("the second change was lost: %+v", got)
	}
	if got.IsQueuedFor("web1.example") {
		t.Errorf("the first change was lost: %+v", got)
	}
	if !sawTheDequeue {
		t.Error("the second writer read the record from before the first writer's " +
			"change; the mutator is not being given what is on disk")
	}
}

// Every writer's change lands, however many there are.
//
// The per-job lock is the thing being checked: a hub delivering a
// batched job appends to Delivered from one goroutine while returns
// arrive on others, and an append that reads a stale slice drops every
// entry added since it read.
func TestEveryWritersChangeLands(t *testing.T) {
	_ = chaos.Exercises(chaos.ConcurrentBookkeeping)
	c := newCache(t)
	j := dispatched(t, c, time.Now())

	const writers = 40
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		node := fmt.Sprintf("web%d.example", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Update(j.JID, func(cur *Job) error {
				cur.Delivered = append(cur.Delivered, node)
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	got, err := c.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Delivered) != writers {
		t.Fatalf("%d of %d deliveries landed: %v", len(got.Delivered), writers, got.Delivered)
	}
	seen := map[string]bool{}
	for _, node := range got.Delivered {
		if seen[node] {
			t.Errorf("%s landed twice", node)
		}
		seen[node] = true
	}
}

// Two jobs do not wait on each other, because a busy hub's bookkeeping
// should not queue behind whichever record is slowest to write.
func TestUpdateLocksPerJobAndNotPerStore(t *testing.T) {
	c := newCache(t)
	now := time.Now()
	first := dispatched(t, c, now, "web1.example")
	second := dispatched(t, c, now.Add(time.Second), "web2.example")
	if first.JID == second.JID {
		t.Fatal("the two jobs share a jid; this test is not testing what it says")
	}

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(holderDone)
		_, _ = c.Update(first.JID, func(*Job) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	var secondErr error
	go func() {
		defer close(done)
		_, secondErr = c.Update(second.JID, func(cur *Job) error {
			cur.State = Complete
			return nil
		})
	}()

	blocked := false
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		blocked = true
	}

	// Let the holder go and wait for both, before anything is asserted.
	// Both of these write a file inside the cache directory, and the
	// directory is a t.TempDir that is removed when this function
	// returns — so a goroutine still running here writes into a
	// directory being deleted. That is not theoretical: the first
	// version of this test did not wait for the holder, passed on every
	// machine here, and failed on CI's emulated FreeBSD runner with
	// "TempDir RemoveAll cleanup: directory not empty", which is the
	// same slow-machine window the defect this file is about lives in.
	close(release)
	<-holderDone
	<-done

	if blocked {
		t.Error("an update to one job waited on an update to another; the lock is per store")
	}
	if secondErr != nil {
		t.Error(secondErr)
	}
}

// A mutator that returns an error writes nothing, which is how a caller
// abandons an update it has decided against without a second read.
func TestAnAbandonedUpdateWritesNothing(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example")

	boom := fmt.Errorf("not this one")
	if _, err := c.Update(j.JID, func(cur *Job) error {
		cur.State = Complete
		return boom
	}); err != boom {
		t.Fatalf("the mutator's error was not returned: %v", err)
	}
	got, err := c.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Dispatched {
		t.Errorf("an abandoned update wrote anyway: state is %s", got.State)
	}
	// And the lock is released, or every later update to this job would
	// block for ever.
	if _, err := c.Update(j.JID, func(cur *Job) error {
		cur.State = Complete
		return nil
	}); err != nil {
		t.Fatalf("the job was still locked after an abandoned update: %v", err)
	}
}

// A jid the cache has no record of is an error rather than a record
// created out of nothing.
func TestUpdatingAJobThatIsNotThereIsAnError(t *testing.T) {
	c := newCache(t)
	called := false
	if _, err := c.Update(NewID(time.Now()), func(*Job) error {
		called = true
		return nil
	}); err == nil {
		t.Error("updating an absent job succeeded")
	}
	if called {
		t.Error("the mutator was called for a job that does not exist")
	}
}
