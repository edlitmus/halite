package hub

import (
	"context"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// expiringJob submits a test.ping with a one-second window to one
// connected node that never answers by itself, and moves the hub's clock
// past the window. The node is what a long state run looks like from the
// hub: it has the job and is still working on it.
func expiringJob(t *testing.T) (*lab, *transport.Client, *transport.Client, string) {
	t.Helper()
	l := metricsLab(t, metricsPolicy).withEvents(t)
	quiet := l.enrolled(t, "quiet.example")
	stop := l.connectSilent(t, quiet, "quiet.example", `{}`)
	t.Cleanup(stop)
	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", TTLSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	l.advance(time.Hour)
	return l, op, quiet, res.JID
}

// jobState reads the state twice, the way the two kinds of reader do:
// GET /v1/jobs/<jid> for the API and anything driving the hub, and the
// record in the cache for `halite-hub jobs list` and `jobs show`.
func jobState(t *testing.T, l *lab, op *transport.Client, jid string) (api, record string, missing []string) {
	t.Helper()
	st, err := op.JobStatus(context.Background(), jid)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := l.server.Jobs.Get(job.ID(jid))
	if err != nil {
		t.Fatal(err)
	}
	return st.State, string(rec.State), st.Missing
}

// Issue 810. A job whose expiry passes while its node is still running it
// is settled partial and counted in halite_jobs_expired_total. The node
// then finishes and answers. The return is filed, and announced, because
// it is the only evidence of what ran; but the job stays partial. Before
// DIVERGENCE 5.715 completeIfDone moved it to complete, so `jobs list`
// and the API said partial and then complete about one run, and the
// counter had counted as expired a job that by then read as finished.
func TestAJobSettledPartialStaysPartialWhenItsNodeAnswersLate(t *testing.T) {
	l, op, quiet, jid := expiringJob(t)

	if n, err := l.server.Settle(); err != nil || n != 1 {
		t.Fatalf("settled %d jobs (%v)", n, err)
	}
	api, record, missing := jobState(t, l, op, jid)
	if api != string(job.Partial) || record != string(job.Partial) || len(missing) != 1 {
		t.Fatalf("after settling: api %q, record %q, missing %v; want partial, partial, one missing",
			api, record, missing)
	}
	wantSample(t, l, "halite_jobs_expired_total", 1, "after settling")

	lateReturn(t, quiet, jid, "quiet.example")
	status := waitForReturns(t, op, jid, 1)

	api, record, missing = jobState(t, l, op, jid)
	if api != string(job.Partial) || record != string(job.Partial) {
		t.Errorf("after the late return: api %q, record %q; want both %q, as already answered",
			api, record, job.Partial)
	}
	if len(missing) != 0 || len(status.Returns) != 1 {
		t.Errorf("the late return was not kept: missing %v, %d returns", missing, len(status.Returns))
	}
	wantSample(t, l, "halite_jobs_expired_total", 1, "after the late return")

	events, _, err := l.server.Events.Read(eventbus.Earliest, []string{tagJobRet(jid, "quiet.example")}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("the late return published %d return events, want 1", len(events))
	}

	// And the sweep that follows does not revisit it.
	if n, err := l.server.Settle(); err != nil || n != 0 {
		t.Errorf("settling again closed %d jobs (%v), want 0", n, err)
	}
	if _, record, _ = jobState(t, l, op, jid); record != string(job.Partial) {
		t.Errorf("after settling again the record reads %q", record)
	}
}

// The same return, landing inside the settlement: after Settle has read
// who is missing and before it writes the state. Settle's reading is then
// stale, the return path completes the job first, and Settle's own write
// must lose -- the job reads complete, which is true, and is not counted
// as expired, because nothing was missing when it closed. Whichever writer
// closes the job first decides its state, and the other changes nothing.
func TestAReturnLandingDuringSettlementCompletesTheJobAndIsNotCountedExpired(t *testing.T) {
	l, op, quiet, jid := expiringJob(t)

	fired := false
	l.server.settleAfterMissing = func() {
		if fired {
			return
		}
		fired = true
		lateReturn(t, quiet, jid, "quiet.example")
	}
	n, err := l.server.Settle()
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the settlement never reached the window the return is meant to land in")
	}
	if n != 0 {
		t.Errorf("Settle reports closing %d jobs; the return path closed it first", n)
	}
	api, record, missing := jobState(t, l, op, jid)
	if api != string(job.Complete) || record != string(job.Complete) || len(missing) != 0 {
		t.Errorf("api %q, record %q, missing %v; want complete, complete, none", api, record, missing)
	}
	wantSample(t, l, "halite_jobs_expired_total", 0, "after a return that beat the settlement")
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after a return that beat the settlement")
}
