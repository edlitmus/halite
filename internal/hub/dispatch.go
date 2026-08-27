package hub

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/job"
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
	// OnBehalfOf is who asked the submitter to submit it, recorded for
	// the audit and never used to authorize.
	OnBehalfOf string
	// Correlation is the causality chain this job belongs to, carried
	// into the events it produces. A reaction sets it to the chain of
	// the event it reacted to, which is what makes a beacon that fires
	// a reaction that changes the file the beacon watches detectable
	// rather than merely slow. SPEC 16.3.
	Correlation string
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
		if sub.Offline == job.Queue {
			// SPEC 9.5: a queued job waits for a machine that is off,
			// so fifteen minutes is too short; "until someone
			// notices" is the hazard, so an hour is the default and
			// --ttl raises it deliberately.
			ttl = QueuedTTL
		}
	}
	j := &job.Job{
		JID:         s.clock().Next(),
		Fun:         sub.Fun,
		Arg:         sub.Arg,
		Kwarg:       sub.Kwarg,
		Env:         sub.Env,
		Nonce:       nonce,
		Created:     now,
		Expires:     now.Add(ttl),
		Submitter:   sub.Submitter,
		OnBehalfOf:  sub.OnBehalfOf,
		Correlation: sub.Correlation,
		Target:      sub.Target,
		TargetKind:  kind.String(),
		Nodes:       matched,
		Offline:     sub.Offline,
		State:       job.Dispatched,
		Test:        sub.Test,
		Batch:       batch,
	}

	if s.Jobs != nil {
		if err := s.Jobs.Put(j); err != nil {
			return nil, err
		}
	}
	s.emitCorrelated(tagJobNew(string(j.JID)), "", j.Correlation, map[string]any{
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
		s.countDispatch(j, len(matched))
		s.info("job dispatched in batches",
			"jid", string(j.JID), "fun", j.Fun, "target", j.Target,
			"matched", len(matched), "batch", j.Batch.Size, "submitter", j.Submitter)
		// Its own copy: the batch goroutine mutates Delivered, and the
		// handler that called Dispatch is still reading this one.
		copied, ctx := cloneJob(j), s.batchContext()
		s.goBackground(func() { s.runBatches(ctx, copied, msg) })
		return j, nil
	}

	// SPEC 9.5's `queue`: the nodes that were not connected are spooled
	// for their next appearance rather than reported unresponsive.
	if sub.Offline == job.Queue && len(absent) > 0 {
		j.Queued = append([]string(nil), absent...)
		if s.Jobs != nil {
			if err := s.Jobs.Put(j); err != nil {
				return nil, err
			}
		}
	}

	delivered := s.deliver(j, msg, matched)
	s.countDispatch(j, len(matched))
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
	ids, err := s.targetableNodes()
	if err != nil {
		return nil, err
	}
	var matched, skipped []string
	var why error
	for _, id := range ids {
		node, err := s.nodes().Matchable(id)
		if err != nil {
			// A node whose cached data will not read must not take the
			// whole job down with it, and must not be silently dropped
			// either.
			s.warn("skipping a node whose cached data is unreadable",
				"node_id", id, "error", err.Error())
			skipped = append(skipped, id)
			why = err
			continue
		}
		if matcher.Match(node) {
			matched = append(matched, id)
		}
	}
	if len(matched) == 0 && len(skipped) > 0 {
		// Every candidate was skipped, so the honest answer is not "no
		// node matched" — that reads as a wrong target and sends the
		// operator to fix one that was right. It happens for the whole
		// fleet at once when the hub cannot read its node cache at all,
		// which is what a cache directory left owned by root after a
		// hand-run as root looks like.
		sort.Strings(skipped)
		return nil, fmt.Errorf(
			"%d accepted node(s) could not be considered because the hub cannot read "+
				"what it has cached about them (%s): %w",
			len(skipped), strings.Join(skipped, ", "), why)
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
