package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// sampleOf reads one unlabelled series out of the hub's own registry.
//
// From the registry's exposition rather than from the gauge's field, so
// what is asserted is what a scraper would be handed: the defect these
// tests hold down was a number that was right in nobody's head and
// wrong on every dashboard.
func sampleOf(t *testing.T, l *lab, family string) float64 {
	t.Helper()
	var buf bytes.Buffer
	if err := l.server.Metrics.Write(&buf); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		rest, ok := strings.CutPrefix(line, family+" ")
		if !ok {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			t.Fatalf("%s has the value %q: %v", family, rest, err)
		}
		return v
	}
	t.Fatalf("%s is not in the exposition:\n%s", family, buf.String())
	return 0
}

func wantSample(t *testing.T, l *lab, family string, want float64, when string) {
	t.Helper()
	if got := sampleOf(t, l, family); got != want {
		t.Errorf("%s: %s is %g, want %g", when, family, got, want)
	}
}

// lateReturn files a return the way a node that answered after the job
// had ended would.
func lateReturn(t *testing.T, client *transport.Client, jid, nodeID string) {
	t.Helper()
	if err := client.Return(context.Background(), job.Return{
		JID: job.ID(jid), NodeID: nodeID, Fun: "test.ping", Success: true,
		Return: json.RawMessage(`true`), Schema: job.ReturnSchema,
	}); err != nil {
		t.Fatalf("the late return from %s was refused: %v", nodeID, err)
	}
}

// A job that reaches its time to live with nodes that never answered is
// finished, and finished owes nothing.
//
// Before this, `halite_jobs_expired_total` was declared and never
// incremented by anything, and `halite_jobs_missing_returns` was lowered
// only by a return -- so the two nodes here, one connected and silent
// and one never connected at all, stayed counted until the hub
// restarted, and the documented `HaliteJobsUnanswered` alert fired for
// ever on a job that had been over for an hour.
func TestAnExpiredJobLeavesNothingOwed(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	quiet := l.enrolled(t, "quiet.example")
	stop := l.connectSilent(t, quiet, "quiet.example", `{}`)
	defer stop()
	l.enrolled(t, "absent.example") // enrolled, never connected: never delivered

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", TTLSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_missing_returns", 2, "in flight")
	wantSample(t, l, "halite_jobs_expired_total", 0, "in flight")

	l.advance(time.Hour)
	if n, err := l.server.Settle(); err != nil || n != 1 {
		t.Fatalf("settled %d jobs (%v)", n, err)
	}
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after expiry")
	wantSample(t, l, "halite_jobs_expired_total", 1, "after expiry")

	// The node that was silent answers after all. The return is still
	// filed -- it is the only evidence of what ran -- but the job has
	// already been closed, so it must not be subtracted a second time:
	// a gauge of nodes that cannot be negative must not read -1.
	lateReturn(t, quiet, res.JID, "quiet.example")
	waitForReturns(t, op, res.JID, 1)
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after a late return")

	// Settling is idempotent, and so is counting it.
	if _, err := l.server.Settle(); err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_expired_total", 1, "after settling again")
}

// A killed job is over by an operator's decision, and the alert for
// unanswered jobs must not keep firing for one somebody stopped on
// purpose. Nor is it an expiry: the counter of those is for windows
// that closed on their own.
func TestAKilledJobLeavesNothingOwed(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	quiet := l.enrolled(t, "quiet.example")
	stop := l.connectSilent(t, quiet, "quiet.example", `{}`)
	defer stop()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_missing_returns", 1, "in flight")

	if _, err := op.KillJob(context.Background(), res.JID); err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after the kill")

	// A node that was already running it can still answer.
	lateReturn(t, quiet, res.JID, "quiet.example")
	waitForReturns(t, op, res.JID, 1)
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after a late return")

	// Killing expires the job, and settling it later must count no
	// expiry for it: the kill already closed it.
	l.advance(time.Hour)
	if _, err := l.server.Settle(); err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_expired_total", 0, "after settling a killed job")

	// Killing it twice does not subtract twice.
	if _, err := op.KillJob(context.Background(), res.JID); err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after a second kill")
}

// A batch stopped at its safe limit has nodes it will never deliver to,
// and they were counted on dispatch with everyone else.
func TestABatchStoppedAtItsSafeLimitLeavesNothingOwed(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	stop, _ := l.fleetOf(t, 6, func(string) bool { return false })
	defer stop()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Batch: "2", BatchSafeLimit: 2,
		BatchTimeoutSecs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := waitForState(t, op, res.JID, string(job.Aborted))
	if len(status.Delivered) >= 6 {
		t.Fatalf("the safe limit let the job reach all %d nodes", len(status.Delivered))
	}
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after the abort")
	wantSample(t, l, "halite_jobs_expired_total", 0, "after the abort")
}

// A return for a job this process never dispatched -- one from before a
// restart, which is the commonest case -- was subtracted from a gauge
// that had never had it added, so a hub that restarted with work in
// flight read a negative number of unanswered nodes.
func TestAReturnForAJobThisHubDidNotCountMovesNothing(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	client := l.enrolled(t, "web1.example")

	jid := l.server.clock().Next()
	if err := l.server.Jobs.Put(&job.Job{
		JID: jid, Fun: "test.ping", Nonce: "n", State: job.Dispatched,
		Created: l.server.now(), Expires: l.server.now().Add(time.Hour),
		Nodes: []string{"web1.example"}, Delivered: []string{"web1.example"},
	}); err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_missing_returns", 0, "before the return")
	lateReturn(t, client, string(jid), "web1.example")
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after a return for an uncounted job")
}

// The ordinary case still works: every node answers, and the gauge is
// back where it started without anything having to settle it.
func TestAnAnsweredJobOwesNothing(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	stop, _ := l.fleetOf(t, 3, nil)
	defer stop()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, op, res.JID, string(job.Complete))
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after every return")
	wantSample(t, l, "halite_jobs_expired_total", 0, "after every return")
}

// A batch resumed by a hub that did not dispatch it -- the restart
// `jobs resume` exists for -- owes whoever it has not heard from, so a
// node that stays silent through the resumed slices is counted, and
// settling it takes them off again.
func TestAResumedBatchOwesWhoItHasNotHeardFrom(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	nodes := []string{"web1.example", "web2.example", "web3.example", "web4.example"}
	for _, id := range nodes {
		l.enrolled(t, id) // enrolled, never connected
	}
	jid := l.server.clock().Next()
	if err := l.server.Jobs.Put(&job.Job{
		JID: jid, Fun: "test.ping", Nonce: "n", State: job.Batching,
		Created: l.server.now(), Expires: l.server.now().Add(time.Hour),
		Nodes: nodes, Delivered: nodes[:2],
		Batch: job.Batch{Size: 2, Timeout: time.Second},
	}); err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_missing_returns", 0, "before the resume")

	if _, err := l.server.Resume(l.server.batchContext(), jid); err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_missing_returns", 4, "after the resume")

	l.advance(2 * time.Hour)
	if _, err := l.server.Settle(); err != nil {
		t.Fatal(err)
	}
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after it expired")
	wantSample(t, l, "halite_jobs_expired_total", 1, "after it expired")
}

// Settle reads only its five hundred most recent records, and a job it
// cannot reach -- older than that, or one whose record is gone -- must
// still come off the gauge once its window has passed, and count as an
// expiry exactly once.
func TestAnExpiredJobSettleCannotListStillComesOff(t *testing.T) {
	l := metricsLab(t, metricsPolicy)
	gone := &job.Job{
		JID: l.server.clock().Next(), Fun: "test.ping",
		Expires: l.server.now().Add(time.Minute),
	}
	// Counted as dispatched, with no record for the listing to find.
	l.server.countDispatch(gone, []string{"web1.example", "web2.example"})
	wantSample(t, l, "halite_jobs_missing_returns", 2, "in flight")

	l.advance(time.Hour)
	for range 2 {
		if _, err := l.server.Settle(); err != nil {
			t.Fatal(err)
		}
	}
	wantSample(t, l, "halite_jobs_missing_returns", 0, "after the sweep")
	wantSample(t, l, "halite_jobs_expired_total", 1, "after sweeping twice")
}
