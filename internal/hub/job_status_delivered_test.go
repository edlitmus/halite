package hub

import (
	"context"
	"slices"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// `delivered` is an attempt, not an arrival, and the documentation now says
// so. It said "the nodes that have received it", and a job for a node that
// was down listed that node under it. This holds the behaviour the wording
// describes: the node is counted and is also missing, and it is the return,
// not `delivered`, that says who received the job. DIVERGENCE 5.524.
func TestDeliveredCountsANodeThatWasNotConnected(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.enrolled(t, "db1.example")
	op := l.operator(t, "ed")
	sub, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "db1.example", TargetKind: "L", Fun: "test.ping", Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := op.JobStatus(context.Background(), sub.JID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(st.Delivered, "db1.example") {
		t.Errorf("delivered is %v; a delivery attempt to the absent node is what it records", st.Delivered)
	}
	if !slices.Contains(st.Missing, "db1.example") {
		t.Errorf("missing is %v; the node has not answered and is not connected", st.Missing)
	}
	if len(st.Returns) != 0 {
		t.Errorf("returns are %v for a node that never connected", st.Returns)
	}
}
