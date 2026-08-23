package hub

import (
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
	// Submitter is the authenticated principal. The handler fills this
	// in from the certificate; a request body cannot set it.
	Submitter string
}

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
	}

	if s.Jobs != nil {
		if err := s.Jobs.Put(j); err != nil {
			return nil, err
		}
	}

	msg := transport.Message{
		T:       transport.MsgJob,
		JID:     string(j.JID),
		Fun:     j.Fun,
		Arg:     j.Arg,
		Kwarg:   j.Kwarg,
		Env:     j.Env,
		Expires: j.Expires.UTC().Format(time.RFC3339Nano),
		Nonce:   j.Nonce,
	}
	if j.Test {
		if msg.Kwarg == nil {
			msg.Kwarg = map[string]any{}
		}
		msg.Kwarg["test"] = true
	}

	delivered := 0
	for _, id := range matched {
		if s.fleet().Send(id, msg) {
			delivered++
		}
	}
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

func (s *Server) nodegroups() target.Nodegroups {
	if s.Nodegroups == nil {
		return nil
	}
	return s.Nodegroups
}
