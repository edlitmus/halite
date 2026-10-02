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
	"github.com/edlitmus/halite/internal/tracing"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
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
	// JID, Expires and Signature are SPEC 25.6's detached signing, and
	// are set together or not at all.
	//
	// A signer has to know the identifier and the expiry before it signs,
	// because the signature covers both, so on a signed submission they
	// come from the caller and this hub checks them rather than choosing
	// them. It cannot check the signature itself: it holds no signer key,
	// which is the whole point, so a forged signature reaches the node
	// and the node refuses it.
	JID       job.ID
	Expires   time.Time
	Signature string
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

	matched, err := s.resolve(matcher, sub.Env)
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
	jid, expires, err := s.identify(sub, now, ttl)
	if err != nil {
		return nil, err
	}
	j := &job.Job{
		JID:         jid,
		Fun:         sub.Fun,
		Arg:         sub.Arg,
		Kwarg:       sub.Kwarg,
		Env:         sub.Env,
		Nonce:       nonce,
		Created:     now,
		Expires:     expires,
		Signature:   sub.Signature,
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

	// SPEC 26.3's span per job, started once the job exists and before
	// anything is delivered.
	//
	// It ends when Dispatch returns, not when the job does. A job is
	// asynchronous by design -- the hub writes it to each node's stream
	// and the returns arrive minutes later -- so a span held open until
	// the last return would be a span held open across a node that never
	// answers. What this measures is the dispatch: resolving the target,
	// recording the job, and writing it to every stream, which is the
	// part the hub is responsible for and the part that can be slow for
	// a reason an operator can act on.
	//
	// Each node then continues the trace from the `traceparent` on the
	// message, so the node-side spans are children of this one and
	// outlive it. A parent that finishes before its children is
	// ordinary in a trace and is what an asynchronous fan-out looks
	// like.
	span := s.Tracer.StartSpan(tracing.SpanContext{}, "dispatch "+j.Fun, tracing.KindServer)
	defer span.End()
	span.SetAttr("halite.jid", string(j.JID))
	span.SetAttr("halite.fun", j.Fun)
	span.SetAttr("halite.target", j.Target)
	span.SetAttr("halite.target_kind", j.TargetKind)
	span.SetAttr("halite.nodes", len(matched))
	if j.Submitter != "" {
		span.SetAttr("halite.submitter", j.Submitter)
	}
	if j.Test {
		span.SetAttr("halite.test", true)
	}
	// Through SpanContextOf rather than the field: a nil span is what
	// tracing being off looks like, and reaching into it is the one way
	// past the nil-safety every method has.
	if sc, ok := tracing.SpanContextOf(span); ok {
		j.TraceParent = tracing.FormatTraceParent(sc)
	}

	if s.Jobs != nil {
		if err := s.Jobs.Put(j); err != nil {
			span.Fail(err)
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
//
// env is the environment the job names, which is also the pillar
// environment a `-I` or `-J` term is evaluated in: a node adopts a job's
// environment for its pillar as well as its files (adoptJobEnvironment
// in halite-node), so that is the pillar it would hold while running
// the job. Empty is `base`, as it is for `pillar.show_pillar`.
func (s *Server) resolve(matcher *target.Matcher, env string) ([]string, error) {
	ids, err := s.targetableNodes()
	if err != nil {
		return nil, err
	}
	loader, err := s.targetPillar(matcher, env)
	if err != nil {
		return nil, err
	}
	var matched, skipped, undecided []string
	var why, undecidedWhy error
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
		var failed error
		if loader != nil {
			node.LoadPillar = loader(id, node, &failed)
		}
		hit := matcher.Match(node)
		if failed != nil {
			// The match was evaluated against an empty pillar standing
			// in for one that would not compile, so `hit` answers a
			// question nobody asked, in either direction. targetPillar
			// says why this refuses the dispatch rather than skipping
			// the node as the unreadable cache entry above is skipped.
			s.warn("a pillar target cannot be decided for a node whose pillar will not compile",
				"node_id", id, "error", failed.Error())
			undecided = append(undecided, id)
			undecidedWhy = failed
			continue
		}
		if hit {
			matched = append(matched, id)
		}
	}
	if len(undecided) > 0 {
		sort.Strings(undecided)
		return nil, fmt.Errorf(
			"target %q reads pillar, and the hub could not compile the pillar of %d "+
				"candidate node(s) (%s), so whether it matches them is unknown; fix their "+
				"pillar, or exclude them ahead of the pillar term (`not L@%s and ...`): %w",
			matcher.Expr(), len(undecided), strings.Join(undecided, ", "),
			strings.Join(undecided, ","), undecidedWhy)
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

// targetPillar prepares what a `-I` or `-J` term reads: each candidate's
// pillar, compiled on the hub on first use. It returns nil for a target
// with no pillar term, which is nearly every target and costs nothing.
//
// SPEC 8.1 says pillar targeting is against "compiled pillar, hub
// side". Until this existed, Matchable handed the matcher an empty map
// and nothing filled it in, so `-I role:web` matched no node at all and
// `not I@role:db` matched every node -- the database hosts included.
//
// The pillar is compiled, not looked up, because the hub keeps no copy
// of what it last sent a node: SPEC 12.8's cache is not built, and the
// node side asks afresh for every run so that a changed value is used
// by the next one. Compiling here gives the answer the node would get,
// from the same tree and with the grains the node last reported -- so a
// pillar target is as stale as a grain target on the same node (SPEC
// 8.3), and no staler. The cost is one compilation per candidate whose
// answer actually depends on pillar (see target.Node.LoadPillar), each
// a full render including external pillar sources: a fleet-wide `-I` on
// a tree that calls out to a secrets manager calls it once per node.
// That is the price of the answer being right, and it is the price a
// fleet-wide highstate already pays.
//
// The grains go through the compiler's `pillar_trusted_grains` filter
// exactly as for the node's own request. That allowlist governs which
// grains a *pillar top file* may target on (SPEC 12.4); it is not
// consulted for the operator's expression, which may read any pillar
// key, as Salt's `-I` does. The operator is not the party 12.4
// distrusts, and the pillar being read was compiled under 12.4 already.
//
// A candidate whose pillar will not compile, or who has never connected
// so that there are no grains to compile it from, cannot be decided,
// and the whole dispatch is refused naming it. The two obvious
// alternatives are both wrong:
//
//   - Treat it as an empty pillar, which is what the matcher does
//     unaided. Then it falls inside every `not I@...`, and the job goes
//     to exactly the host the target was written to keep it from.
//   - Skip it with a warning, as resolve does for an unreadable cache
//     entry. Then a node that belonged in the job is out of it, and the
//     only record is a line in the hub's log that the operator at the
//     terminal never sees. Pillar is how an estate usually says what a
//     machine *is*, so that is a database host quietly missing its
//     change, reported as a successful job.
//
// Refusing is loud and recoverable: the operator learns which node and
// why, and can fix its pillar or exclude it ahead of the pillar term,
// which short-circuit evaluation then never compiles for. Because the
// loader is lazy, a node decided without reading pillar holds nothing
// up.
//
// A hub with no `pillar_roots` has no pillar to target, and refuses the
// expression rather than matching nothing: the empty match was the
// defect.
func (s *Server) targetPillar(matcher *target.Matcher, env string) (
	func(id string, node target.Node, failed *error) func() *value.Map, error) {
	reads := false
	for _, term := range matcher.Terms() {
		if term.Kind == target.Pillar || term.Kind == target.PillarRegex {
			reads = true
			break
		}
	}
	if !reads {
		return nil, nil
	}
	if s.Pillar == nil || s.Pillar.Roots == nil {
		return nil, fmt.Errorf("target %q reads pillar, and this hub compiles no pillar "+
			"to target against; set pillar_roots", matcher.Expr())
	}
	if env == "" {
		env = "base"
	}
	return func(id string, node target.Node, failed *error) func() *value.Map {
		var (
			done   bool
			loaded *value.Map
		)
		return func() *value.Map {
			if done {
				return loaded
			}
			done = true
			// Matchable answers a node it holds nothing about with empty
			// grains, which is right for an ID match and wrong for a
			// compilation: a pillar compiled from no grains is not the
			// pillar the node would receive.
			if _, err := s.nodes().Get(id); err != nil {
				if errors.Is(err, ErrUnknownNode) || errors.Is(err, errNoNodeCache) {
					*failed = fmt.Errorf("%s has not connected, so the hub holds no grains "+
						"to compile its pillar from", id)
				} else {
					*failed = err
				}
				return nil
			}
			compiled, err := s.compilePillar(id, env, node.Grains)
			if err != nil {
				*failed = fmt.Errorf("compiling the pillar of %s: %w", id, err)
				return nil
			}
			loaded = compiled.Pillar
			return loaded
		}
	}, nil
}

// messageFor is the wire form of a job. One function, so that a batch
// resumed after a restart sends exactly what the first slice did.
func messageFor(j *job.Job) transport.Message {
	// job.WireKwargs rather than an assembly here, because SPEC 25.6's
	// signature covers the arguments a node receives: if this function
	// and the one the operator signs with disagreed about a single key,
	// every signed `--test` job would be refused as unsigned.
	kwargs := job.WireKwargs(j)
	return transport.Message{
		T:       transport.MsgJob,
		JID:     string(j.JID),
		Fun:     j.Fun,
		Arg:     j.Arg,
		Kwarg:   kwargs,
		Env:     j.Env,
		Expires: j.Expires.UTC().Format(time.RFC3339Nano),
		Nonce:   j.Nonce,
		// The trace this job belongs to, so the node's spans continue
		// it. Empty on a hub that is not tracing, which is a job message
		// byte-for-byte identical to the one this build sent before
		// tracing existed.
		TraceParent: j.TraceParent,
		// Who asked, for the node's own record of what it ran. SPEC
		// 25.7 requires the principal and the node had no way to know
		// it. Empty on a job with no authenticated submitter, which is
		// one the hub raised itself.
		Submitter:  j.Submitter,
		OnBehalfOf: j.OnBehalfOf,
		// What the operator asked for, which the signature covers and
		// which a node checks itself against. SPEC 25.6.
		Target:     j.Target,
		TargetKind: j.TargetKind,
		Signature:  j.Signature,
	}
}

// identify settles a job's identifier and expiry.
//
// Unsigned, they are the hub's: the next identifier from its clock and
// now plus the time to live, as they have always been. Signed, they are
// the caller's, because the signature covers both and a signer cannot
// sign an identifier the hub has not issued yet without a second round
// trip -- one in which the hub chooses what is about to be signed.
//
// What the hub checks instead is everything about them that does not need
// a key: the identifier is well formed, it is not one this hub already
// has a job for, and the expiry is in the future. The first two are what
// stop a caller replaying a signed submission at the hub; the node's own
// guard of SPEC 6.3 stops it being replayed at the node.
func (s *Server) identify(sub Submission, now time.Time, ttl time.Duration) (job.ID, time.Time, error) {
	if sub.Signature == "" {
		if sub.JID != "" || !sub.Expires.IsZero() {
			return "", time.Time{}, errors.New(
				"a job identifier and an expiry may only be given with a signature")
		}
		return s.clock().Next(), now.Add(ttl), nil
	}
	if !sub.JID.Valid() {
		return "", time.Time{}, fmt.Errorf(
			"a signed job must carry its own identifier and %q is not one", sub.JID)
	}
	if sub.Expires.IsZero() {
		return "", time.Time{}, errors.New("a signed job must carry an absolute expiry")
	}
	if !sub.Expires.After(now) {
		return "", time.Time{}, fmt.Errorf(
			"this job expired at %s and it is now %s",
			sub.Expires.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	if s.Jobs != nil {
		if _, err := s.Jobs.Get(sub.JID); err == nil {
			return "", time.Time{}, fmt.Errorf("this hub already has a job %s", sub.JID)
		} else if !errors.Is(err, job.ErrNoJob) {
			return "", time.Time{}, fmt.Errorf("checking whether %s has been submitted before: %w", sub.JID, err)
		}
	}
	return sub.JID, sub.Expires, nil
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
