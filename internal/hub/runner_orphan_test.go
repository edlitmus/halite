package hub

import (
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
)

// A runner call in flight when the hub stopped is not listed as running for
// ever. DIVERGENCE 5.528.
func TestARunnerCallLeftDispatchedByAPreviousRunIsClosedAtStartup(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	jid := l.server.clock().Next()
	l.server.recordRunnerJob(jid, RunnerCall{Principal: "cert:CN=ed", Fun: "state.orchestrate"}, time.Now())
	// A node job in the same state is not the hub's to close: its nodes may
	// still answer and its window is what ends it.
	nodeJob := l.server.clock().Next()
	if err := l.server.Jobs.Put(&job.Job{
		JID: nodeJob, Fun: "test.ping", Target: "web1.example", Nodes: []string{"web1.example"},
		State: job.Dispatched, Created: time.Now(), Submitter: "cert:CN=ed",
	}); err != nil {
		t.Fatal(err)
	}

	n, err := l.server.AbandonRunnerJobs()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d calls closed, want 1", n)
	}
	got, _ := l.server.Jobs.Get(jid)
	if got.State != job.Aborted {
		t.Errorf("the orphaned runner call is %q", got.State)
	}
	other, _ := l.server.Jobs.Get(nodeJob)
	if other.State != job.Dispatched {
		t.Errorf("a node job was closed too: %q", other.State)
	}
	if again, _ := l.server.AbandonRunnerJobs(); again != 0 {
		t.Errorf("a second sweep closed %d more", again)
	}
}
