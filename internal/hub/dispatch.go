package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/transport"
)

// Submission is what an operator asks for.
type Submission struct {
	Target     string
	TargetKind string
	Fun        string
	Arg        []string
	Kwarg      map[string]any
	Env        string
	Test       bool
	Offline    job.Offline
	TTL        time.Duration
	// Batch and Subset are SPEC 9.3's controls, applied on the hub.
	// BatchSpec is what the operator wrote -- a count or a percentage
	// -- and the size is resolved against the matched set here,
	// because the operator does not know that number when they type
	// the command.
	BatchSpec string
	Batch     job.Batch
	Subset    int
	// Submitter is the authenticated principal. The handler fills this
	// in from the certificate; a request body cannot set it.
	Submitter string
}

// errJobExpired is what `jobs resume` says about a job whose window
// has closed: the remaining nodes would refuse it anyway.
var errJobExpired = errors.New("this job has expired; the nodes it has not reached would refuse it")

// Dispatch is the job flow of SPEC 9.1: resolve the target, assign a
// jid, record the job with its expected respondents, and only then
// write it to each matched node's stream.
//
// The order matters. Recording first is what makes a missing return
// detectable: a hub that delivered and then recorded would have no
// account of a job it had already sent when it crashed between the two.
func (s *Server) Dispatch(sub Submission) (*job.Job, error) {
	if sub.Fun == "" {
		return nil, fmt.Errorf("a job needs a function to run")
	}
	kind, ok := target.KindFromFlag(sub.TargetKind)
	if !ok {
		return nil, fmt.Errorf("%q is not a target kind", sub.TargetKind)
	}
	matcher, err := target.Compile(kind, sub.Target, s.nodegroups())
	if err != nil {
		return nil, err
	}

	matched, err := s.resolve(matcher)
	if err != nil {
		return nil, err
	}

	connected := s.fleet().Connected()
	var absent []string
	for _, id := range matched {
		if _, up := connected[id]; !up {
			absent = append(absent, id)
		}
	}
	if sub.Offline == job.Require && len(absent) > 0 {
		return nil, fmt.Errorf("%d of %d matched nodes are not connected (%v) and the offline policy is %q",
			len(absent), len(matched), absent, job.Require)
	}

	if sub.Subset > 0 {
		matched, err = subsetOf(matched, sub.Subset)
		if err != nil {
			return nil, err
		}
	}

	batch := sub.Batch
	size, err := job.ParseBatchSize(sub.BatchSpec, len(matched))
	if err != nil {
		return nil, err
	}
	batch.Size = size

	nonce, err := job.Nonce()
	if err != nil {
		return nil, err
	}
	now := s.now()
	ttl := sub.TTL
	if ttl <= 0 {
		ttl = job.DefaultTTL
	}
	j := &job.Job{
		JID:        s.clock().Next(),
		Fun:        sub.Fun,
		Arg:        sub.Arg,
		Kwarg:      sub.Kwarg,
		Env:        sub.Env,
		Nonce:      nonce,
		Created:    now,
		Expires:    now.Add(ttl),
		Submitter:  sub.Submitter,
		Target:     sub.Target,
		TargetKind: kind.String(),
		Nodes:      matched,
		Offline:    sub.Offline,
		State:      job.Dispatched,
		Test:       sub.Test,
		Batch:      batch,
	}

	if s.Jobs != nil {
		if err := s.Jobs.Put(j); err != nil {
			return nil, err
		}
	}
	s.emit(tagJobNew(string(j.JID)), "", map[string]any{
		"jid": string(j.JID), "fun": j.Fun, "arg": j.Arg,
		"tgt": j.Target, "tgt_type": j.TargetKind,
		"nodes": j.Nodes, "submitter": j.Submitter, "test": j.Test,
	})

	msg := messageFor(j)

	// A batched job is delivered a slice at a time by a goroutine that
	// belongs to the hub, so closing the terminal does not abandon it
	// with half the estate updated. SPEC 9.3.
	if j.Batched() {
		j.State = job.Batching
		if s.Jobs != nil {
			if err := s.Jobs.Put(j); err != nil {
				return nil, err
			}
		}
		s.info("job dispatched in batches",
			"jid", string(j.JID), "fun", j.Fun, "target", j.Target,
			"matched", len(matched), "batch", j.Batch.Size, "submitter", j.Submitter)
		// Its own copy: the batch goroutine mutates Delivered, and the
		// handler that called Dispatch is still reading this one.
		go s.runBatches(s.batchContext(), cloneJob(j), msg)
		return j, nil
	}

	delivered := s.deliver(j, msg, matched)
	s.info("job dispatched",
		"jid", string(j.JID), "fun", j.Fun, "target", j.Target,
		"matched", len(matched), "delivered", delivered, "submitter", j.Submitter)
	if len(absent) > 0 {
		// Named, not counted: "three nodes were unresponsive" sends an
		// operator to the job cache to find out which.
		s.warn("some matched nodes are not connected",
			"jid", string(j.JID), "nodes", absent, "policy", string(sub.Offline))
	}
	return j, nil
}

// resolve turns a compiled target into the node set.
//
// Only accepted nodes are considered: a pending or rejected request is
// not part of the estate, and a revoked one is deliberately out of it.
func (s *Server) resolve(matcher *target.Matcher) ([]string, error) {
	records, err := s.Authority.Store.List()
	if err != nil {
		return nil, err
	}
	now := s.now()
	var matched []string
	for _, rec := range records {
		if rec.Status(now) != keystore.Accepted {
			continue
		}
		node, err := s.nodes().Matchable(rec.NodeID)
		if err != nil {
			// A node whose cached grains will not decode must not take
			// the whole job down with it, and must not be silently
			// dropped either.
			s.warn("skipping a node whose cached data is unreadable",
				"node_id", rec.NodeID, "error", err.Error())
			continue
		}
		if matcher.Match(node) {
			matched = append(matched, rec.NodeID)
		}
	}
	sort.Strings(matched)
	return matched, nil
}

// messageFor is the wire form of a job. One function, so that a batch
// resumed after a restart sends exactly what the first slice did.
func messageFor(j *job.Job) transport.Message {
	// The kwargs are copied rather than shared. Setting `test` on the
	// message used to write into the job's own map, so the record on
	// disk grew an argument the operator never passed -- and a resumed
	// batch would have sent a different message from the first slice.
	kwargs := make(map[string]any, len(j.Kwarg)+1)
	for k, v := range j.Kwarg {
		kwargs[k] = v
	}
	if j.Test {
		kwargs["test"] = true
	}
	if len(kwargs) == 0 {
		kwargs = nil
	}
	return transport.Message{
		T:       transport.MsgJob,
		JID:     string(j.JID),
		Fun:     j.Fun,
		Arg:     j.Arg,
		Kwarg:   kwargs,
		Env:     j.Env,
		Expires: j.Expires.UTC().Format(time.RFC3339Nano),
		Nonce:   j.Nonce,
	}
}

// batchContext is what a batch goroutine lives inside: the server's
// own, so that stopping the hub stops the batch rather than leaving a
// goroutine writing to a closed store.
func (s *Server) batchContext() context.Context {
	if s.Context != nil {
		return s.Context
	}
	return context.Background()
}

func (s *Server) nodegroups() target.Nodegroups {
	if s.Nodegroups == nil {
		return nil
	}
	return s.Nodegroups
}
