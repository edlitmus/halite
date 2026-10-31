package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// What a safe limit counts, recorded as it is.
//
// `--batch-safe-limit 1` stops a batch once one node has *failed*, and a
// node that does not answer is not a failure: awaitBatch counts returns whose
// Success is false and nothing else. A change that hangs the first node it
// reaches, or cuts it off, therefore does not trip the limit; after the
// batch timeout the batch warns "advanced with returns outstanding" and
// delivers the same change to the next slice. SPEC 9.3 lists "unresponsive"
// beside "failed" as separate states, so counting only the second is the
// letter of it; whether a silent node should count toward the limit is the
// operator's decision and is open (DIVERGENCE 5.496). This test says what
// happens today, so a change in either direction is a decision and not an
// accident.
func TestASilentNodeIsNotCountedAgainstTheSafeLimit(t *testing.T) {
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
	// One slice a second; the silent node is first, so the other two are only
	// reached if the batch moves past it.
	deadline := time.Now().Add(15 * time.Second)
	for tr.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	// The tracker sees the two nodes that answer; the silent one takes the job
	// and says nothing.
	if tr.count() != 2 {
		t.Fatalf("%d of the two answering nodes were given the job; a silent node stopped the batch, "+
			"which is not what is recorded in 5.496", tr.count())
	}
	status, err := op.JobStatus(context.Background(), res.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Delivered) != 3 {
		t.Errorf("delivered to %v, want all three", status.Delivered)
	}
	if len(status.Missing) != 1 || status.Missing[0] != "web1.example" {
		t.Errorf("missing = %v, want only the silent node", status.Missing)
	}
}
