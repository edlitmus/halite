package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// A silent node counts against the safe limit (issue 540, DIVERGENCE 5.496).
//
// `--batch-safe-limit` exists to stop before the rest of the estate gets the
// same broken change, and the broken change it caught least was the one
// that kills its target: a node that takes the job and never answers made no
// failed return, so after the batch timeout the batch delivered the same
// change to the next slice. This test held that behaviour until the owner
// decided it the other way; it now holds the decision. The silent node is
// first, so the other two are reached only if the batch moves past it.
func TestASilentNodeCountsAgainstTheSafeLimit(t *testing.T) {
	l := newLab(t).withJobs(t)
	tr := &tracker{seen: map[string]time.Time{}}
	var stops []func()
	web1 := l.enrolled(t, "web1.example")
	stops = append(stops, l.connectSilent(t, web1, "web1.example", `{}`))
	for _, id := range []string{"web2.example", "web3.example"} {
		stops = append(stops, l.connectTracking(t, l.enrolled(t, id), id, tr, nil))
	}
	var once sync.Once
	defer once.Do(func() {
		for _, stop := range stops {
			stop()
		}
	})

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Batch: "1", BatchSafeLimit: 1, BatchTimeoutSecs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := waitForState(t, op, res.JID, "aborted")
	if len(status.Delivered) != 1 || status.Delivered[0] != "web1.example" {
		t.Errorf("delivered to %v, want only the silent node", status.Delivered)
	}
	// Two seconds past the abort, which is two more slice timeouts: if the
	// batch were still going, a second node would have been given the job.
	time.Sleep(2 * time.Second)
	if n := tr.count(); n != 0 {
		t.Fatalf("%d of the answering nodes were given the job after the first node "+
			"stayed silent; a silent node did not count against the safe limit", n)
	}
}

// With no `--batch-timeout`, a slice waits DefaultBatchTimeout and not the
// job's whole lifetime, and no wait runs past the expiry (issue 540).
//
// The old default was time.Until(j.Expires): with the default 15-minute
// lifetime, one silent node held the rollout for 15 minutes.
func TestTheDefaultSliceWaitIsBoundedAndStopsAtTheExpiry(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		configured time.Duration
		expires    time.Time
		want       time.Duration
	}{
		{"no timeout, a default-length job", 0, now.Add(job.DefaultTTL), DefaultBatchTimeout},
		{"no timeout, a short job", 0, now.Add(time.Minute), time.Minute},
		{"an operator's timeout", 30 * time.Second, now.Add(job.DefaultTTL), 30 * time.Second},
		{"an operator's timeout past the expiry", time.Hour, now.Add(job.DefaultTTL), job.DefaultTTL},
		{"already expired", 0, now.Add(-time.Second), 0},
	}
	for _, c := range cases {
		if got := sliceWait(c.configured, c.expires, now); got != c.want {
			t.Errorf("%s: waits %v, want %v", c.name, got, c.want)
		}
	}
	if DefaultBatchTimeout >= job.DefaultTTL {
		t.Errorf("the default slice wait %v is not shorter than a job's default lifetime %v; "+
			"one silent slice would use the whole job up", DefaultBatchTimeout, job.DefaultTTL)
	}
}

// A batch delivers no slice once its job has expired (issue 540).
//
// The node would refuse it (TestANodeRefusesAJobThatHasExpired in
// cmd/halite-node), so it would change nothing, and each refusal is a
// failed return. The hub's clock is moved past the expiry while the first
// slice is waiting on a silent node; the next two nodes must not be given
// the job, and the job is closed as Settle closes an expired job with nodes
// never reached: aborted.
func TestABatchDeliversNoSliceAfterItsJobExpires(t *testing.T) {
	l := newLab(t).withJobs(t)
	tr := &tracker{seen: map[string]time.Time{}}
	var stops []func()
	web1 := l.enrolled(t, "web1.example")
	stops = append(stops, l.connectSilent(t, web1, "web1.example", `{}`))
	for _, id := range []string{"web2.example", "web3.example"} {
		stops = append(stops, l.connectTracking(t, l.enrolled(t, id), id, tr, nil))
	}
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Batch: "1", BatchTimeoutSecs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The first slice is out before the clock moves, so what is tested is
	// the step between slices and not whether the first was sent.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := op.JobStatus(context.Background(), res.JID)
		if err != nil {
			t.Fatal(err)
		}
		if len(status.Delivered) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first slice was never delivered: %v", status.Delivered)
		}
		time.Sleep(20 * time.Millisecond)
	}
	l.advance(job.DefaultTTL + time.Minute)

	status := waitForState(t, op, res.JID, string(job.Aborted))
	time.Sleep(2 * time.Second)
	if n := tr.count(); n != 0 {
		t.Fatalf("%d nodes were given a job that had expired", n)
	}
	if len(status.Delivered) != 1 {
		t.Errorf("delivered to %v, want only the first slice", status.Delivered)
	}
}
