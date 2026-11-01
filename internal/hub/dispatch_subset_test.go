package hub

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// `--subset` picks the nodes a job goes to, and the offline policy is about
// those nodes. Dispatch worked out which matched nodes were not connected
// *before* it chose the subset, so under `--offline queue` the nodes the
// subset left out were queued the job anyway (it ran on each as it next
// connected, on machines the operator had said not to touch), and under
// `--offline require` the job was refused for a node it was not going to
// reach. DIVERGENCE 5.509.
func TestAQueuedSubsetQueuesOnlyForTheChosenNodes(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	for _, id := range []string{"web1.example", "web2.example", "web3.example", "web4.example", "web5.example", "web6.example"} {
		l.enrolled(t, id)
	}
	op := l.operator(t, "ed")

	sub, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "web*", TargetKind: "glob", Fun: "test.ping", Offline: "queue", Subset: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	j, err := l.server.Jobs.Get(jobIDOf(sub.JID))
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Nodes) != 2 {
		t.Fatalf("the subset chose %d nodes, want 2: %v", len(j.Nodes), j.Nodes)
	}
	for _, q := range j.Queued {
		if !slices.Contains(j.Nodes, q) {
			t.Errorf("%s is queued the job and is not among the chosen nodes %v (queued %v)", q, j.Nodes, j.Queued)
		}
	}
}

func TestARequiredSubsetIsRefusedOnlyForTheChosenNodes(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	up := l.enrolled(t, "web1.example")
	l.enrolled(t, "web2.example")
	l.enrolled(t, "web3.example")
	tr := &tracker{seen: map[string]time.Time{}}
	defer l.connectTracking(t, up, "web1.example", tr, nil)()
	op := l.operator(t, "ed")

	// The connected node is the whole subset only by luck, so run it until
	// the draw is one where it is: the job must then go, and on the old code
	// it was refused for web2 or web3 whichever was drawn or not.
	for i := 0; i < 40; i++ {
		_, err := op.Submit(context.Background(), transport.SubmitRequest{
			Target: "web*", TargetKind: "glob", Fun: "test.ping", Offline: "require", Subset: 1,
		})
		if err != nil && !strings.Contains(err.Error(), "not connected") {
			t.Fatal(err)
		}
		if err == nil {
			return
		}
	}
	t.Fatal("a subset of the one connected node was never allowed in 40 draws: the refusal counts nodes outside the subset")
}
