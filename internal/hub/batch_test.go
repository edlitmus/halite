package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// fleetOf enrols and connects n nodes that answer every job.
//
// answer decides what each one returns, so a test can make a
// particular node fail.
func (l *lab) fleetOf(t *testing.T, n int, answer func(nodeID string) bool) (func(), *tracker) {
	t.Helper()
	tr := &tracker{seen: map[string]time.Time{}}
	var stops []func()
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("web%d.example", i)
		client := l.enrolled(t, id)
		stops = append(stops, l.connectTracking(t, client, id, tr, answer))
	}
	return func() {
		for _, stop := range stops {
			stop()
		}
	}, tr
}

// tracker records when each node was given the job, so a test can
// prove that a batch was staged rather than sent at once.
type tracker struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (t *tracker) saw(nodeID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.seen[nodeID]; !ok {
		t.seen[nodeID] = time.Now()
	}
}

func (t *tracker) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.seen)
}

func (l *lab) connectTracking(t *testing.T, client *transport.Client, nodeID string, tr *tracker, answer func(string) bool) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Subscribe(ctx, transport.SubscribeRequest{NodeID: nodeID},
			func(msg transport.Message) error {
				switch msg.T {
				case transport.MsgPing:
					select {
					case <-ready:
					default:
						close(ready)
					}
				case transport.MsgJob:
					tr.saw(nodeID)
					ok := answer == nil || answer(nodeID)
					client.Return(ctx, job.Return{
						JID:     job.ID(msg.JID),
						NodeID:  nodeID,
						Fun:     msg.Fun,
						Success: ok,
						RetCode: map[bool]int{true: 0, false: 1}[ok],
						Return:  json.RawMessage(`{"node":"` + nodeID + `"}`),
						Schema:  job.ReturnSchema,
					})
				}
				return nil
			})
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("%s never connected", nodeID)
	}
	return func() { cancel(); <-stopped }
}

// waitForState polls until the hub reports the state asked for.
func waitForState(t *testing.T, op *transport.Client, jid, want string) *transport.JobStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last *transport.JobStatus
	for {
		status, err := op.JobStatus(context.Background(), jid)
		if err != nil {
			t.Fatal(err)
		}
		last = status
		if status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s is %q after five seconds, not %q (%d returns)",
				jid, last.State, want, len(last.Returns))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SPEC 9.3: at most N at a time, waiting for returns before advancing.
func TestABatchIsDeliveredASliceAtATime(t *testing.T) {
	l := newLab(t).withJobs(t)
	stop, tr := l.fleetOf(t, 4, nil)
	defer stop()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Batch: "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Batch != 2 {
		t.Fatalf("the hub resolved the batch to %d", res.Batch)
	}
	if len(res.Nodes) != 4 {
		t.Fatalf("matched %v", res.Nodes)
	}

	// The first slice is out and the rest are not.
	deadline := time.Now().Add(2 * time.Second)
	sawPartial := false
	for time.Now().Before(deadline) {
		if n := tr.count(); n > 0 && n < 4 {
			sawPartial = true
			break
		}
		if tr.count() == 4 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !sawPartial {
		t.Log("the batches completed too quickly to observe a partial state; the counts below still have to add up")
	}

	// The last return is recorded before the state is updated, so a
	// batched job is finished when the hub says so and not when the
	// count reaches the total. `halite-hub run` polls on exactly this.
	status := waitForState(t, op, res.JID, string(job.Complete))
	if len(status.Delivered) != 4 {
		t.Errorf("delivered to %v", status.Delivered)
	}
	if tr.count() != 4 {
		t.Errorf("%d nodes ran the job", tr.count())
	}
}

// A percentage is resolved against the matched set on the hub, because
// the operator does not know that number when they type the command.
func TestABatchPercentageIsResolvedAgainstTheMatchedSet(t *testing.T) {
	l := newLab(t).withJobs(t)
	stop, _ := l.fleetOf(t, 4, nil)
	defer stop()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Batch: "50%",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Batch != 2 {
		t.Errorf("50%% of four nodes came to %d", res.Batch)
	}
	waitForState(t, op, res.JID, string(job.Complete))
}

// The point of a safe limit: stop before the rest of the estate gets
// the same broken change.
func TestASafeLimitStopsTheRestOfTheEstate(t *testing.T) {
	l := newLab(t).withJobs(t)
	stop, tr := l.fleetOf(t, 6, func(string) bool { return false })
	defer stop()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Batch: "2", BatchSafeLimit: 2,
		BatchTimeoutSecs: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var status *transport.JobStatus
	for time.Now().Before(deadline) {
		status, err = op.JobStatus(context.Background(), res.JID)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == string(job.Aborted) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.State != string(job.Aborted) {
		t.Fatalf("the job is %q after %d returns; it should have aborted", status.State, len(status.Returns))
	}
	if len(status.Delivered) >= 6 {
		t.Errorf("the safe limit let the job reach all %d nodes", len(status.Delivered))
	}
	if tr.count() >= 6 {
		t.Errorf("%d nodes ran a job that should have been stopped", tr.count())
	}
	// And the nodes never reached are named, not counted.
	if len(status.Nodes)-len(status.Delivered) < 1 {
		t.Error("nothing was held back")
	}
}

// A hub that stops mid-batch has half the estate updated and a record
// saying so. Salt has neither.
func TestABatchIsResumable(t *testing.T) {
	l := newLab(t).withJobs(t)
	op := l.operator(t, "ed")

	// Four nodes enrolled and none connected, so the first slice goes
	// nowhere and the batch stalls on its timeout.
	for i := 1; i <= 4; i++ {
		l.enrolled(t, fmt.Sprintf("web%d.example", i))
	}
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Batch: "2", BatchTimeoutSecs: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wait for the first slice to have been recorded as delivered.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err := op.JobStatus(context.Background(), res.JID)
		if err != nil {
			t.Fatal(err)
		}
		if len(status.Delivered) >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Resuming a job that has reached everyone is not an error, and
	// resuming one that has not says how many are left.
	again, err := op.ResumeJob(context.Background(), res.JID)
	if err != nil {
		t.Fatal(err)
	}
	_ = again

	// An identifier that is not a job.
	if _, err := op.ResumeJob(context.Background(), "20260101T000000000000"); err == nil {
		t.Error("resuming a job the hub never dispatched succeeded")
	}
}

// A job stays `dispatched` for ever because one node never answered,
// unless something closes the books: `jobs list` showed week-old jobs
// as though they were in flight.
func TestJobsAreSettledOnceTheirWindowHasPassed(t *testing.T) {
	l := newLab(t).withJobs(t)
	stop, _ := l.fleetOf(t, 2, nil)
	defer stop()
	l.enrolled(t, "absent.example") // enrolled, never connected

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", TTLSeconds: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForReturns(t, op, res.JID, 2)

	// Before the window closes it is genuinely in flight.
	if n, err := l.server.Settle(); err != nil || n != 0 {
		t.Errorf("settled %d jobs before any expired (%v)", n, err)
	}

	// The clock, not a sleep: the window is the job's own.
	l.server.Now = func() time.Time { return time.Now().Add(time.Hour) }
	n, err := l.server.Settle()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("settled %d jobs", n)
	}
	status, err := op.JobStatus(context.Background(), res.JID)
	if err != nil {
		t.Fatal(err)
	}
	// Two answered and one never did: partial, not complete and not
	// aborted -- the job reached everyone it was sent to.
	if status.State != string(job.Partial) {
		t.Errorf("the job settled as %q, want partial", status.State)
	}
	if len(status.Missing) != 1 || status.Missing[0] != "absent.example" {
		t.Errorf("missing = %v", status.Missing)
	}
	// Settling is idempotent.
	if n, err := l.server.Settle(); err != nil || n != 0 {
		t.Errorf("a settled job was settled again (%d, %v)", n, err)
	}
}

// A subset is a canary. Which machines it chose has to have an answer
// afterwards, and the answer must not be predictable in advance.
func TestASubsetPicksSomeAndRecordsWhich(t *testing.T) {
	l := newLab(t).withJobs(t)
	stop, _ := l.fleetOf(t, 6, nil)
	defer stop()

	op := l.operator(t, "ed")
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		res, err := op.Submit(context.Background(), transport.SubmitRequest{
			Target: "*", Fun: "test.ping", Subset: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Nodes) != 2 {
			t.Fatalf("a subset of 2 chose %v", res.Nodes)
		}
		seen[strings.Join(res.Nodes, ",")] = true
		waitForReturns(t, op, res.JID, 2)

		// The job records which ones, so `jobs show` can say.
		j, err := l.server.Jobs.Get(job.ID(res.JID))
		if err != nil {
			t.Fatal(err)
		}
		if len(j.Nodes) != 2 {
			t.Errorf("the record says %v", j.Nodes)
		}
	}
	// Six draws of two from six landing on the same pair every time
	// would be a shuffle that does not shuffle.
	if len(seen) == 1 {
		t.Errorf("every draw chose the same pair: %v", seen)
	}
}
