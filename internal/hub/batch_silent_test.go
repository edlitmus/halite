package hub

import (
	"context"
	"sync"
	"testing"
	"time"

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
