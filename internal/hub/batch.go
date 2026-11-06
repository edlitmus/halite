package hub

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// deliver writes a job to a set of nodes and records who it reached.
//
// The record is updated before the sends and after them: before, so a
// hub that dies mid-batch does not resend to nodes it may already have
// reached; after, so the count is right. Delivering twice is not a
// disaster -- the node's replay guard refuses the second -- but it
// wastes a batch slot and makes the run look like it did more than it
// did.
func (s *Server) deliver(j *job.Job, msg transport.Message, nodes []string) int {
	if s.Jobs != nil {
		// The caller's copy tracks the batch's progress; the record on
		// disk is changed through Update, because a return from one of
		// these very nodes can be recorded before this line runs and
		// its goroutine is editing the same record. See
		// job.Cache.Update.
		j.Delivered = append(j.Delivered, nodes...)
		if _, err := s.Jobs.Update(j.JID, func(cur *job.Job) error {
			cur.Delivered = append(cur.Delivered, nodes...)
			return nil
		}); err != nil {
			s.warn("could not record a delivery", "jid", string(j.JID), "error", err.Error())
		}
	}
	sent := 0
	for _, id := range nodes {
		if s.fleet().Send(id, msg) {
			sent++
		}
	}
	return sent
}

// runBatches delivers a batched job a slice at a time, waiting for each
// slice to return before advancing. SPEC 9.3.
//
// It runs on the hub, in its own goroutine, and reads its state from
// the job cache -- so an operator who closes the terminal has not
// stopped anything, and a hub that restarts can resume.
func (s *Server) runBatches(ctx context.Context, j *job.Job, msg transport.Message) {
	size := j.Batch.Size
	if size < 1 {
		size = 1
	}
	for {
		if ctx.Err() != nil {
			s.info("batch stopped with the hub", "jid", string(j.JID))
			return
		}
		remaining := j.Remaining()
		if len(remaining) == 0 {
			break
		}
		// **Never a slice to a job that has expired** (issue 540). The
		// node refuses it -- the replay guard checks the expiry and the
		// node sends back a failed return saying `refused: expired`
		// (TestANodeRefusesAJobThatHasExpired) -- so a slice delivered
		// now changes nothing on the node, but each refusal is a failed
		// return, which a safe limit then counts as though the change had
		// broken that machine. The batch used to do exactly that: with no
		// `--batch-timeout` it waited the job's whole lifetime per slice,
		// so every slice after the first silent one went to an expired
		// job. The job is closed here the way Settle closes any expired
		// job, by the same code, so the two cannot disagree about what an
		// expired batch is.
		if j.Expired(s.now()) {
			s.warn("batch stopped: the job expired before every node was reached",
				"jid", string(j.JID), "undelivered", len(remaining))
			if cur, err := s.Jobs.Get(j.JID); err != nil {
				s.warn("could not read an expired batch to close it", "jid", string(j.JID), "error", err.Error())
			} else if _, err := s.settleExpired(cur); err != nil {
				s.warn("could not close an expired batch", "jid", string(j.JID), "error", err.Error())
			}
			return
		}
		slice := remaining
		if len(slice) > size {
			slice = slice[:size]
		}

		sent := s.deliver(j, msg, slice)
		s.info("batch delivered",
			"jid", string(j.JID), "nodes", slice, "sent", sent,
			"done", len(j.Delivered), "of", len(j.Nodes))

		failed := s.awaitBatch(ctx, j, slice, sliceWait(j.Batch.Timeout, j.Expires, s.now()))

		// failed includes the delivered nodes that have not answered; see
		// awaitBatch.
		if j.Batch.SafeLimit > 0 && failed >= j.Batch.SafeLimit {
			// The point of the limit: stop before the rest of the
			// estate gets the same broken change.
			s.warn("batch aborted by its safe limit",
				"jid", string(j.JID), "failed", failed, "limit", j.Batch.SafeLimit,
				"undelivered", len(j.Remaining()))
			// Closed before the state is written, so anything that sees
			// `aborted` sees the gauge already settled. The nodes never
			// delivered to will never answer, and the ones in the last
			// slice have already outlasted the batch timeout; the job is
			// over and owes nothing. Settle skips an aborted job, so
			// without this its count stayed until the hub restarted.
			s.countJobEnded(j.JID)
			s.setState(j, job.Aborted)
			return
		}
		if len(j.Remaining()) > 0 && j.Batch.Wait > 0 {
			select {
			case <-time.After(j.Batch.Wait):
			case <-ctx.Done():
				return
			}
		}
	}
	s.info("batch complete", "jid", string(j.JID), "nodes", len(j.Nodes))
	// **Delivered to everyone, so no longer batching** (issue 540). A
	// node may still owe its return, and the job then stayed `batching`
	// until Settle closed it at expiry -- reading as though the hub had
	// slices left to send, when what it had was the same thing any
	// unbatched job has once it is sent: returns owed. `dispatched` is
	// that state, and it is what Settle, `jobs active` and the
	// orchestrator's wait already treat as in flight. Only from
	// `batching`, inside the record's update: a return that completed the
	// job, or a kill, got there first and is left alone.
	if _, err := s.Jobs.Update(j.JID, func(cur *job.Job) error {
		if cur.State != job.Batching {
			return errAlreadySettled
		}
		cur.State = job.Dispatched
		return nil
	}); err == nil {
		j.State = job.Dispatched
	} else if !errors.Is(err, errAlreadySettled) {
		s.warn("could not record a finished batch as dispatched", "jid", string(j.JID), "error", err.Error())
	}
	s.completeIfDone(j.JID)
}

// DefaultBatchTimeout is how long a slice waits for its returns when the
// operator gave no `--batch-timeout` (issue 540).
//
// It used to be the job's whole remaining lifetime, 15 minutes by default
// (job.DefaultTTL), taken once and then waited again for every slice. So one
// silent node held the rollout for the job's entire life, and every slice
// after it went to a job that had expired.
//
// Five minutes, and the number is chosen, not measured. It has to be well
// short of the default lifetime, or a single silent slice still uses the job
// up: at a third of it, a 15-minute job survives two slices that time out and
// can still deliver a third. It has to be long enough for an ordinary
// `state.apply` to return, or the batch counts a slow node as a silent one --
// which, since a silent node now counts against `--batch-safe-limit`, stops
// the batch. No highstate on the estate has been timed against it. An
// operator whose run takes longer sets `--batch-timeout`, and an operator
// whose run is a `test.ping` sets it shorter; this is the default for the
// one who set neither. Salt has no equivalent number to copy: its `--batch`
// is a client-side loop that waits on `timeout` and asks each node
// whether it is still running the job, which halite does not do.
const DefaultBatchTimeout = 5 * time.Minute

// sliceWait is how long one slice waits: the operator's `--batch-timeout`,
// or DefaultBatchTimeout, and never past the job's expiry.
//
// Capped at the expiry whichever it is, because nothing is gained by
// waiting longer: the next slice cannot be delivered to an expired job (see
// runBatches), so a wait past it only delays the batch reporting that it
// has stopped. Computed per slice rather than once, because the time left
// is what shrinks.
func sliceWait(configured time.Duration, expires, now time.Time) time.Duration {
	wait := configured
	if wait <= 0 {
		wait = DefaultBatchTimeout
	}
	if !expires.IsZero() {
		if left := expires.Sub(now); left < wait {
			wait = left
		}
	}
	if wait < 0 {
		wait = 0
	}
	return wait
}

// awaitBatch waits for one slice's returns and reports how many of the
// job's nodes count against its safe limit so far.
//
// Failures are counted across the whole job rather than the slice,
// because that is what a safe limit means: "stop when this many
// machines have broken", not "this many in a row".
//
// **A node that has not answered counts the same as one that failed**
// (issue 540, DIVERGENCE 5.496). It used to count only returns whose
// Success was false, so the change the limit exists to catch least was
// the one that kills its target: a change that hangs the node, reboots
// it, or cuts it off from the hub produces no return at all, and the
// batch moved on after the timeout and gave the same change to the
// next slice. SPEC 9.3 lists "unresponsive" beside "failed" for
// `--progress`, and it still does; for the limit they are one thing,
// because neither is a node the operator can say the change left
// working.
//
// "Has not answered" is judged when the slice's wait ends, over every
// node the batch has delivered to. Each of those has by then had at
// least one full slice wait: the ones in this slice have just had it,
// and the ones in earlier slices had theirs before this slice was sent.
// A node from an earlier slice that has since answered is counted by
// its answer, so a slow node that came back and succeeded is not held
// against the job. A node that was not connected when its slice was
// delivered is counted too: the hub cannot tell "never received it"
// from "received it and went quiet", and of the two mistakes, stopping
// a batch that would have been fine is the safer one. `jobs resume`
// undoes it once the node has answered; while it is still silent it
// counts again, as a failed return always has, because the count is
// over the whole job.
func (s *Server) awaitBatch(ctx context.Context, j *job.Job, slice []string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	want := make(map[string]bool, len(slice))
	for _, id := range slice {
		want[id] = true
	}
	for {
		returns, err := s.Jobs.Returns(j.JID)
		if err != nil {
			s.warn("reading returns for a batch", "jid", string(j.JID), "error", err.Error())
			return 0
		}
		failed, answered := 0, 0
		returned := make(map[string]bool, len(returns))
		for _, r := range returns {
			returned[r.NodeID] = true
			if !r.Success {
				failed++
			}
			if want[r.NodeID] {
				answered++
			}
		}
		if answered >= len(slice) || time.Now().After(deadline) {
			silent := 0
			for _, id := range j.Delivered {
				if !returned[id] {
					silent++
				}
			}
			if answered < len(slice) {
				s.warn("a batch slice timed out with returns outstanding",
					"jid", string(j.JID), "answered", answered, "of", len(slice),
					"silent", silent)
			}
			return failed + silent
		}
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			return failed
		}
	}
}

// Settle closes the books on jobs whose window has passed.
//
// Without it a job stays `dispatched` for ever because one node never
// answered, `jobs list` shows week-old jobs as though they were in
// flight, and "what is running right now" has no answer. A job past
// its expiry is finished by definition: the nodes it has not heard
// from would refuse it now.
func (s *Server) Settle() (int, error) {
	if s.Jobs == nil {
		return 0, nil
	}
	// Recent jobs only. Anything older has been settled already, or
	// predates this hub and is not worth a full scan every minute.
	jobs, err := s.Jobs.List(500)
	if err != nil {
		return 0, err
	}
	now := s.now()
	settled := 0
	for _, j := range jobs {
		if j.State != job.Dispatched && j.State != job.Batching {
			continue
		}
		if !j.Expired(now) {
			continue
		}
		closed, err := s.settleExpired(j)
		if err != nil {
			return settled, err
		}
		if closed {
			settled++
		}
	}
	// And anything still owed whose window has passed but which the
	// listing above did not reach: a job older than its five hundred, or
	// one whose record is no longer there. The record of such a job is left
	// as it is -- that is the listing's limit, not this one's -- but the
	// gauge must not keep its nodes for ever because of it. Anything the
	// loop above settled has already been closed, so nothing is counted
	// twice.
	for _, id := range s.owed.expired(now) {
		if owed, _ := s.countJobEnded(id); owed > 0 {
			s.m().jobsExpired.Inc()
		}
	}
	return settled, nil
}

// settleExpired closes one job whose window has passed: complete if every
// node answered, aborted if some were never delivered to, partial
// otherwise. Settle calls it for every expired job it finds, and a batch
// calls it for its own job when it reaches the expiry with nodes still to
// go, so the two close an expired batch the same way.
//
// It reports whether it closed the job. The state is changed only from
// dispatched or batching, inside the record's own update, because the
// two callers can reach one job at once -- Settle's sweep and the batch's
// own goroutine -- and the one that loses must not count the expiry a
// second time.
func (s *Server) settleExpired(j *job.Job) (bool, error) {
	missing, err := s.Jobs.Missing(j.JID)
	if err != nil {
		return false, err
	}
	switch {
	case len(missing) == 0:
		j.State = job.Complete
	case len(j.Remaining()) > 0:
		// It expired with nodes never delivered to, which is a
		// different failure from nodes that did not answer.
		j.State = job.Aborted
	default:
		j.State = job.Partial
	}
	state := j.State
	if _, err := s.Jobs.Update(j.JID, func(cur *job.Job) error {
		if cur.State != job.Dispatched && cur.State != job.Batching {
			return errAlreadySettled
		}
		cur.State = state
		return nil
	}); errors.Is(err, errAlreadySettled) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	// The books close on the gauge as well as on the record. An
	// expiry is counted from the record's own answer -- a node is
	// still missing -- rather than from what this process had on its
	// books, so a job dispatched before a restart and settled after
	// it is still counted once: a job is closed here only while it is
	// dispatched or batching, and has just been moved out of both.
	s.countJobEnded(j.JID)
	if len(missing) > 0 {
		s.m().jobsExpired.Inc()
	}
	s.info("job settled",
		"jid", string(j.JID), "state", string(j.State),
		"returned", len(j.Nodes)-len(missing), "of", len(j.Nodes))
	return true, nil
}

// errAlreadySettled abandons a change of state that another goroutine got to
// first: a settle, or a finished batch moving to dispatched.
var errAlreadySettled = errors.New("the job has already been settled")

// runnerJobWindow is how many of the most recent jobs AbandonRunnerJobs looks
// through. A runner call orphaned by a restart is from the run before it, so
// it is among the newest; the number is chosen, not measured.
const runnerJobWindow = 5000

// AbandonRunnerJobs closes the runner calls a previous run of the hub left
// dispatched, and says how many.
//
// A runner call is recorded `dispatched` before it runs and closed when it
// returns (`recordRunnerJob`, `recordRunnerReturn`), and it carries no expiry
// because it is not delivered to anyone and has no window to gather in.
// `Settle` closes only a job that has one. So a call in flight when the hub
// stopped -- a restart, a crash, a kill -- was listed as running for ever: the
// answer to "what is running right now" included a call that stopped with the
// process (DIVERGENCE 5.528).
//
// This is called once at startup, before the hub serves anything, which is
// the only moment it is certain that no runner call of this process is in
// flight: whatever is recorded dispatched against the hub itself belongs to a
// process that no longer exists. Called at any other time it would close the
// calls that are running. They are marked aborted, which is what they are:
// stopped by the hub going away with no return to report.
func (s *Server) AbandonRunnerJobs() (int, error) {
	if s.Jobs == nil {
		return 0, nil
	}
	jobs, err := s.Jobs.List(runnerJobWindow)
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, j := range jobs {
		if j.Target != RunnerTarget || j.State != job.Dispatched {
			continue
		}
		if _, err := s.Jobs.Update(j.JID, func(cur *job.Job) error {
			if cur.State == job.Dispatched {
				cur.State = job.Aborted
			}
			return nil
		}); err != nil {
			return closed, err
		}
		s.info("a runner call was in flight when the hub stopped",
			"jid", string(j.JID), "fun", j.Fun, "principal", j.Submitter)
		closed++
	}
	return closed, nil
}

// setState records a job's outcome.
func (s *Server) setState(j *job.Job, state job.State) {
	j.State = state
	if s.Jobs == nil {
		return
	}
	if _, err := s.Jobs.Update(j.JID, func(cur *job.Job) error {
		cur.State = state
		return nil
	}); err != nil {
		s.warn("could not record a job's state", "jid", string(j.JID), "error", err.Error())
	}
}

// Resume picks up a batched job the hub was part way through.
//
// A hub that restarts mid-batch has half the estate updated and a
// record saying so. Salt has neither. SPEC 9.3 names this command.
func (s *Server) Resume(ctx context.Context, id job.ID) (*job.Job, error) {
	j, err := s.Jobs.Get(id)
	if err != nil {
		return nil, err
	}
	if len(j.Remaining()) == 0 {
		return j, nil
	}
	if j.Expired(s.now()) {
		return nil, errJobExpired
	}
	s.info("resuming a batch",
		"jid", string(id), "delivered", len(j.Delivered), "remaining", len(j.Remaining()))
	j.State = job.Batching
	if _, err := s.Jobs.Update(id, func(cur *job.Job) error {
		cur.State = job.Batching
		return nil
	}); err != nil {
		return nil, err
	}
	// Back on the books: whoever it has not heard from is owed again.
	// After a safe-limit abort the job was closed, and after a restart
	// this process never had it, so without this the nodes the resumed
	// batch reaches would answer against nothing and a node that stayed
	// silent would never be counted. A node already owed is not owed
	// twice.
	if missing, err := s.Jobs.Missing(id); err == nil {
		s.m().jobsOutstanding.Add(float64(s.owed.owe(id, j.Expires, missing)))
	} else {
		s.warn("could not count a resumed job's unanswered nodes", "jid", string(id), "error", err.Error())
	}
	// The goroutine gets its own copy, because it mutates Delivered
	// while the caller is still reading what it returned. It is counted
	// as the hub's own work: a bare `go` here writes job records after
	// Serve has returned, so a hub that reports it has stopped has not.
	copied, msg := cloneJob(j), messageFor(j)
	s.goBackground(func() { s.runBatches(ctx, copied, msg) })
	return j, nil
}

// cloneJob is a deep enough copy that two goroutines can hold one job
// without sharing anything either of them writes.
func cloneJob(j *job.Job) *job.Job {
	c := *j
	c.Nodes = append([]string(nil), j.Nodes...)
	c.Delivered = append([]string(nil), j.Delivered...)
	c.Arg = append([]string(nil), j.Arg...)
	if j.Kwarg != nil {
		c.Kwarg = make(map[string]any, len(j.Kwarg))
		for k, v := range j.Kwarg {
			c.Kwarg[k] = v
		}
	}
	return &c
}

// subsetOf picks a random n of the matched set, for SPEC 9.3's
// `--subset`. The chosen set is recorded on the job, so "which ones did
// it pick" has an answer afterwards.
//
// crypto/rand, and not only because SPEC 25.3 requires it everywhere
// but the template seed: a subset is a canary, and a canary set anyone
// can predict is one that can be arranged to miss.
func subsetOf(nodes []string, n int) ([]string, error) {
	if n <= 0 || n >= len(nodes) {
		return nodes, nil
	}
	shuffled := append([]string(nil), nodes...)
	for i := len(shuffled) - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return nil, fmt.Errorf("choosing a subset: %w", err)
		}
		k := j.Int64()
		shuffled[i], shuffled[k] = shuffled[k], shuffled[i]
	}
	chosen := shuffled[:n]
	sort.Strings(chosen)
	return chosen, nil
}
