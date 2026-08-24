package hub

import (
	"context"
	"crypto/rand"
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
		j.Delivered = append(j.Delivered, nodes...)
		if err := s.Jobs.Put(j); err != nil {
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
	timeout := j.Batch.Timeout
	if timeout <= 0 {
		timeout = time.Until(j.Expires)
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
		slice := remaining
		if len(slice) > size {
			slice = slice[:size]
		}

		sent := s.deliver(j, msg, slice)
		s.info("batch delivered",
			"jid", string(j.JID), "nodes", slice, "sent", sent,
			"done", len(j.Delivered), "of", len(j.Nodes))

		failed := s.awaitBatch(ctx, j, slice, timeout)

		if j.Batch.SafeLimit > 0 && failed >= j.Batch.SafeLimit {
			// The point of the limit: stop before the rest of the
			// estate gets the same broken change.
			s.warn("batch aborted by its safe limit",
				"jid", string(j.JID), "failed", failed, "limit", j.Batch.SafeLimit,
				"undelivered", len(j.Remaining()))
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
	s.completeIfDone(j.JID)
}

// awaitBatch waits for one slice's returns and reports how many of the
// job's returns so far have failed.
//
// Failures are counted across the whole job rather than the slice,
// because that is what a safe limit means: "stop when this many
// machines have broken", not "this many in a row".
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
		for _, r := range returns {
			if !r.Success {
				failed++
			}
			if want[r.NodeID] {
				answered++
			}
		}
		if answered >= len(slice) || time.Now().After(deadline) {
			if answered < len(slice) {
				s.warn("a batch advanced with returns outstanding",
					"jid", string(j.JID), "answered", answered, "of", len(slice))
			}
			return failed
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
		missing, err := s.Jobs.Missing(j.JID)
		if err != nil {
			return settled, err
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
		if err := s.Jobs.Put(j); err != nil {
			return settled, err
		}
		settled++
		s.info("job settled",
			"jid", string(j.JID), "state", string(j.State),
			"returned", len(j.Nodes)-len(missing), "of", len(j.Nodes))
	}
	return settled, nil
}

// setState records a job's outcome.
func (s *Server) setState(j *job.Job, state job.State) {
	j.State = state
	if s.Jobs == nil {
		return
	}
	if err := s.Jobs.Put(j); err != nil {
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
	if err := s.Jobs.Put(j); err != nil {
		return nil, err
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
