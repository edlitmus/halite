package relay

import (
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// relayForTest is a relay with a job cache and nothing upstream.
func relayForTest(t *testing.T) *Relay {
	t.Helper()
	dir := t.TempDir()
	jobs, err := job.OpenCache(dir + "/jobs")
	if err != nil {
		t.Fatal(err)
	}
	spool, err := OpenSpool(dir+"/spool", 0)
	if err != nil {
		t.Fatal(err)
	}
	return &Relay{
		opts: Options{
			ID:     "relay1.example",
			Server: &hub.Server{Jobs: jobs},
			Now:    func() time.Time { return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) },
			Log:    func(string, string, ...any) {},
		},
		spool: spool,
	}
}

// A job the relay passes down has to be recorded here too.
//
// Without the record the return comes back to a hub that never
// dispatched the job: the relay refuses it as an unknown jid, the node
// logs that its return was refused, and the operator upstream waits out
// the timeout on a job that in fact ran and succeeded.
func TestAForwardedJobIsRecordedSoItsReturnIsAccepted(t *testing.T) {
	r := relayForTest(t)
	jid := "20260826T150532427456"

	if err := r.dispatch(transport.Message{
		T: transport.MsgJob, JID: jid, Fun: "test.ping", Node: "web1.example",
		Expires: time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC).Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	recorded, err := r.opts.Server.Jobs.Get(job.ID(jid))
	if err != nil {
		t.Fatalf("a forwarded job was not recorded: %v", err)
	}
	if recorded.Fun != "test.ping" {
		t.Errorf("the record is %+v", recorded)
	}
	if len(recorded.Nodes) != 1 || recorded.Nodes[0] != "web1.example" {
		t.Errorf("the record expects %v to answer", recorded.Nodes)
	}
	if !r.forwarded(job.ID(jid)) {
		t.Error("a job this relay forwarded is not recognised as forwarded")
	}
}

// A job submitted to the relay directly is the relay's own business.
//
// The upstream has no record of it, so forwarding its return earns a
// refusal for an unknown jid — which then sat at the head of the spool.
func TestAReturnForALocallySubmittedJobIsNotForwardedUpstream(t *testing.T) {
	r := relayForTest(t)
	jid := job.ID("20260826T150532427456")
	if err := r.opts.Server.Jobs.Put(&job.Job{
		JID: jid, Fun: "test.ping", Nodes: []string{"web1.example"},
		Submitter: "cert:CN=ed", Expires: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	if r.forwarded(jid) {
		t.Fatal("a locally submitted job is treated as one from upstream")
	}
	// Not connected, so a forwarded return would spool. Nothing should.
	r.Return(&job.Return{JID: jid, NodeID: "web1.example", Fun: "test.ping"})
	if r.spool.Count() != 0 {
		t.Errorf("a local job's return was spooled for the upstream: %d entries", r.spool.Count())
	}
}

// A jid the relay has no record of at all is not forwarded either.
func TestAReturnForAnUnknownJobIsNotForwardedUpstream(t *testing.T) {
	r := relayForTest(t)
	r.Return(&job.Return{
		JID: "20260826T999999999999", NodeID: "web1.example", Fun: "test.ping",
	})
	if r.spool.Count() != 0 {
		t.Errorf("an unknown job's return was spooled: %d entries", r.spool.Count())
	}
}

// A refusal is survived several times before the entry is dropped.
//
// The upstream checks that a relay filing a return owns the node it
// names, so until it has recorded the subordinates it refuses the whole
// spool as an impersonation attempt. Dropping on the first refusal
// discarded exactly the returns the spool exists to keep; never
// dropping blocks every return behind a genuinely poison one.
func TestASpooledReturnSurvivesARefusalButNotForever(t *testing.T) {
	r := relayForTest(t)
	name := "entry.json"

	for i := 1; i < maxSpoolAttempts; i++ {
		if n := r.attempt(name); n != i {
			t.Fatalf("attempt %d counted as %d", i, n)
		}
	}
	if n := r.attempt(name); n != maxSpoolAttempts {
		t.Fatalf("the last attempt counted as %d, not %d", n, maxSpoolAttempts)
	}

	// And an entry that gets through starts over, so a later outage
	// does not inherit the count from an earlier one.
	r.forget(name)
	if n := r.attempt(name); n != 1 {
		t.Errorf("a drained entry's count survived it: %d", n)
	}
}
