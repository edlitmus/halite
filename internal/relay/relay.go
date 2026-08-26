// Package relay proxies between hubs.
//
// SPEC 5.3 replaces Salt's syndic, which is the least reliable
// component in a large estate. The four differences it names are the
// whole design:
//
//   - a durable spool, so an upstream outage does not lose returns;
//   - event forwarding that is explicit and filterable by tag glob,
//     rather than all-or-nothing;
//   - pillar forwarded upstream by default, so pillar has one source
//     of truth;
//   - a depth limit, because unbounded nesting is how syndic estates
//     become undebuggable.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/hub"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/transport"
)

// Options configure a relay.
type Options struct {
	// Server is the local hub the subordinate nodes connect to.
	Server *hub.Server
	// Upstream is the client this relay presents itself with.
	Upstream *transport.Client
	// ID is this relay's own node identity, which is what the upstream
	// sees as the single connected client.
	ID string
	// Depth is how many relays this one is already behind, taken from
	// its own upstream connection. Zero for a relay attached directly
	// to a hub.
	Depth int
	// SpoolDir holds returns the upstream could not take.
	SpoolDir string
	// SpoolMax bounds it.
	SpoolMax int64
	// EventTags are the globs whose events are forwarded upstream.
	// Empty forwards nothing, which is the safe default: SPEC 5.3
	// makes forwarding explicit precisely because the syndic's
	// all-or-nothing is what floods a hub.
	EventTags []string
	// Retry is how long to wait before reconnecting upstream.
	Retry time.Duration
	// Now is the clock, for the tests.
	Now func() time.Time
	// Log receives what the relay wants an operator to know.
	Log func(level, msg string, kv ...any)
}

func (o *Options) log(level, msg string, kv ...any) {
	if o.Log != nil {
		o.Log(level, msg, kv...)
	}
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Options) retry() time.Duration {
	if o.Retry <= 0 {
		return 10 * time.Second
	}
	return o.Retry
}

// Relay is a running relay.
type Relay struct {
	opts  Options
	spool *Spool

	mu sync.Mutex
	// reported is the subordinate set the upstream was last told
	// about, so a change is sent and an unchanged fleet is not.
	reported map[string]bool
	// connected records that the upstream stream is open, which is
	// what decides whether a return is posted or spooled.
	connected bool
	// attempts counts refusals per spooled entry, so a return the
	// upstream will never accept is dropped rather than blocking every
	// return behind it for ever.
	attempts map[string]int

	// Nil until Register is called, and every counter here is nil-safe,
	// so a relay running without metrics needs no branches.
	returnsUp *metrics.Counter
	eventsUp  *metrics.Counter
}

// maxSpoolAttempts is how many refusals a spooled return survives.
const maxSpoolAttempts = 5

// attempt records one refusal and answers with the running count.
func (r *Relay) attempt(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempts == nil {
		r.attempts = map[string]int{}
	}
	r.attempts[name]++
	return r.attempts[name]
}

// forget drops the count for an entry that has left the spool.
func (r *Relay) forget(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.attempts, name)
}

// New checks the configuration and answers with a relay.
func New(opts Options) (*Relay, error) {
	if opts.Server == nil {
		return nil, fmt.Errorf("a relay needs a local hub")
	}
	if opts.Upstream == nil {
		return nil, fmt.Errorf("a relay needs an upstream; set --upstream")
	}
	if opts.ID == "" {
		return nil, fmt.Errorf("a relay needs its own node identity")
	}
	if opts.Depth >= transport.MaxRelayDepth {
		return nil, fmt.Errorf("this relay would be %d deep and SPEC 5.3 caps it at %d",
			opts.Depth+1, transport.MaxRelayDepth)
	}
	spool, err := OpenSpool(opts.SpoolDir, opts.SpoolMax)
	if err != nil {
		return nil, err
	}
	return &Relay{opts: opts, spool: spool, reported: map[string]bool{}}, nil
}

// Run holds the upstream connection open, reconnecting.
func (r *Relay) Run(ctx context.Context) error {
	for {
		if err := r.session(ctx); err != nil && ctx.Err() == nil {
			r.opts.log("warn", "the upstream connection ended", "error", err.Error())
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(r.opts.retry()):
		}
	}
}

// session is one upstream connection.
func (r *Relay) session(ctx context.Context) error {
	subordinates := r.subordinates()
	r.opts.log("info", "connecting upstream",
		"relay", r.opts.ID, "subordinates", len(subordinates), "depth", r.opts.Depth+1)

	r.setConnected(true)
	defer r.setConnected(false)

	// Anything spooled while the upstream was away goes first, so the
	// returns arrive in the order they happened — but only once the
	// upstream has been told who this relay proxies for.
	//
	// Draining concurrently with the subscription lost every spooled
	// return on reconnect: the upstream checks that a relay filing a
	// return owns the node it names, and until it has recorded the
	// subordinates it owns none of them, so it refused the whole spool
	// as an impersonation attempt.
	go func() {
		if err := r.announce(ctx); err != nil {
			r.opts.log("warn", "the upstream could not be told who this relay proxies for; "+
				"the spool waits for the next attempt", "error", err.Error())
			return
		}
		r.drain(ctx)
	}()
	// And the fleet is watched, so a subordinate that connects while
	// this session is open is reported rather than waiting for the
	// next reconnection.
	go r.watchFleet(ctx)

	return r.opts.Upstream.Subscribe(ctx, transport.SubscribeRequest{
		NodeID: r.opts.ID, Relay: true, Depth: r.opts.Depth,
		Subordinates: subordinates,
	}, r.handle)
}

// handle acts on one message from the upstream.
func (r *Relay) handle(msg transport.Message) error {
	switch msg.T {
	case transport.MsgPing:
		return nil
	case transport.MsgJob:
		return r.dispatch(msg)
	case transport.MsgEvent, transport.MsgRevoke, transport.MsgReload,
		transport.MsgQuiesce, transport.MsgDrain, transport.MsgKill:
		// Passed through to the named subordinate, or to all of them
		// when the upstream named none.
		return r.forwardDown(msg)
	}
	return nil
}

// dispatch runs an upstream job against this relay's own fleet.
func (r *Relay) dispatch(msg transport.Message) error {
	if msg.Node == "" {
		return nil
	}
	// The relay does not re-target: the upstream already decided which
	// node this is for, and matching again here against a fleet the
	// upstream cannot see would let the two disagree about who ran it.
	r.record(msg, []string{msg.Node})
	delivered := r.fleet().Send(msg.Node, msg)
	if !delivered {
		// The subordinate is gone. Answered immediately rather than
		// left to time out, because the upstream's respondent list has
		// this node on it and a job that waits five minutes for a
		// machine the relay knows is absent is five minutes wasted.
		r.Return(&job.Return{
			JID: job.ID(msg.JID), NodeID: msg.Node, Fun: msg.Fun,
			Success: false, RetCode: 1,
			Return: json.RawMessage(fmt.Sprintf("%q",
				"this node is not connected to the relay that proxies for it")),
			Schema:    job.ReturnSchema,
			StartTime: r.opts.now().UTC().Format(time.RFC3339Nano),
		})
	}
	return nil
}

// forwardDown sends a control message to a subordinate.
func (r *Relay) forwardDown(msg transport.Message) error {
	if msg.Node != "" {
		r.record(msg, []string{msg.Node})
		r.fleet().Send(msg.Node, msg)
		return nil
	}
	r.record(msg, r.names())
	r.fleet().Broadcast(msg)
	return nil
}

// record files a forwarded job in the relay's own cache before it goes
// down.
//
// Without this the return comes back to a hub that never dispatched the
// job: the relay refuses it as an unknown jid, the node logs that its
// return was refused, and the operator upstream waits out the timeout
// on a job that in fact ran and succeeded. The relay has to be a hub
// for its own subordinates, which means holding the job record even
// though it did not create it.
func (r *Relay) record(msg transport.Message, nodes []string) {
	if msg.T != transport.MsgJob || msg.JID == "" || r.opts.Server.Jobs == nil {
		return
	}
	expires, err := time.Parse(time.RFC3339, msg.Expires)
	if err != nil {
		// The node enforces the deadline from the message it receives,
		// so a stamp the relay cannot read costs nothing but the
		// relay's own view of when the record may be pruned.
		expires = r.opts.now().Add(time.Hour)
	}
	forwarded := &job.Job{
		JID: job.ID(msg.JID), Fun: msg.Fun, Arg: msg.Arg, Kwarg: msg.Kwarg,
		Env: msg.Env, Nonce: msg.Nonce, Expires: expires, Created: r.opts.now(),
		Submitter: r.submitter(), Nodes: nodes,
	}
	if err := r.opts.Server.Jobs.Put(forwarded); err != nil {
		r.opts.log("warn", "a forwarded job could not be recorded; its returns will be refused",
			"jid", msg.JID, "error", err.Error())
	}
}

// names is every subordinate currently connected.
func (r *Relay) names() []string {
	connected := r.fleet().Connected()
	out := make([]string, 0, len(connected))
	for id := range connected {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Return posts a subordinate's return upstream, spooling it when the
// upstream cannot take it.
//
// SPEC 5.3's first improvement over the syndic, and the one an estate
// notices: an upstream outage does not lose returns. A relay that
// dropped them would make every job during an outage look like a fleet
// that did not answer.
func (r *Relay) Return(ret *job.Return) {
	if !r.forwarded(ret.JID) {
		return
	}
	encoded, err := json.Marshal(ret)
	if err != nil {
		r.opts.log("warn", "a return could not be encoded",
			"jid", string(ret.JID), "node_id", ret.NodeID, "error", err.Error())
		return
	}
	if r.isConnected() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := r.opts.Upstream.Return(ctx, ret)
		cancel()
		if err == nil {
			r.returnsUp.With("sent").Inc()
			return
		}
		r.opts.log("warn", "a return could not be forwarded upstream; spooling it",
			"jid", string(ret.JID), "node_id", ret.NodeID, "error", err.Error())
	}
	if err := r.spool.Put(encoded, r.opts.now()); err != nil {
		r.returnsUp.With("dropped").Inc()
		r.opts.log("warn", "a return could not be spooled",
			"jid", string(ret.JID), "node_id", ret.NodeID, "error", err.Error())
		return
	}
	r.returnsUp.With("spooled").Inc()
}

// drain sends what is spooled, oldest first.
func (r *Relay) drain(ctx context.Context) {
	for {
		entry, ok, err := r.spool.Next()
		if err != nil || !ok {
			return
		}
		var ret job.Return
		if err := json.Unmarshal(entry.Body, &ret); err != nil {
			// Not a return. Removed rather than retried for ever: it
			// will never become one.
			r.opts.log("warn", "a spooled entry is not a return; discarding it", "file", entry.Name)
			_ = r.spool.Remove(entry)
			continue
		}
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = r.opts.Upstream.Return(sendCtx, &ret)
		cancel()
		var refused *transport.RefusedError
		if errors.As(err, &refused) {
			// A refusal is dropped only after several attempts, never
			// on the first. Leaving it at the head of the spool for
			// ever would block every later return behind it, but
			// dropping it at once discards a return the upstream
			// refused only because it had not yet caught up with who
			// this relay proxies for.
			n := r.attempt(entry.Name)
			if n < maxSpoolAttempts {
				r.opts.log("warn", "the upstream refused a spooled return; keeping it",
					"jid", string(ret.JID), "node_id", ret.NodeID,
					"attempt", n, "error", refused.Error())
				return
			}
			r.returnsUp.With("discarded").Inc()
			r.opts.log("warn", "the upstream refused a spooled return too often; discarding it",
				"jid", string(ret.JID), "node_id", ret.NodeID,
				"attempts", n, "error", refused.Error())
			r.forget(entry.Name)
			_ = r.spool.Remove(entry)
			continue
		}
		if err != nil {
			// Stopped at the first failure, so the order is kept and
			// the upstream is not hammered while it is down.
			r.opts.log("warn", "a spooled return could not be sent; keeping it",
				"jid", string(ret.JID), "remaining", r.spool.Count(), "error", err.Error())
			return
		}
		r.returnsUp.With("drained").Inc()
		r.opts.log("info", "drained a spooled return",
			"jid", string(ret.JID), "node_id", ret.NodeID, "remaining", r.spool.Count())
		r.forget(entry.Name)
		_ = r.spool.Remove(entry)
	}
}

// ForwardEvent sends one local event upstream if its tag matches.
//
// SPEC 5.3's second improvement: explicit and filterable, rather than
// the syndic's all-or-nothing. An estate with a busy segment behind a
// relay should be able to forward the job returns and leave the beacon
// chatter local.
func (r *Relay) ForwardEvent(e *eventbus.Event) {
	if !r.wants(e.Tag) {
		return
	}
	if !r.isConnected() {
		// Not spooled. A return is a job's answer and its loss is a
		// job that looks unanswered; an event is a record, and
		// spooling every event of an outage would fill the disk with
		// the thing that was already lost. The gap is visible in the
		// upstream's own bus, which has offsets.
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := r.opts.Upstream.SendEvent(ctx, transport.EventRequest{
		Tag: e.Tag, Data: encodeEventData(e), Correlation: e.Correlation,
	}); err != nil {
		r.opts.log("warn", "an event could not be forwarded upstream",
			"tag", e.Tag, "error", err.Error())
		return
	}
	r.eventsUp.Inc()
}

func encodeEventData(e *eventbus.Event) json.RawMessage {
	if len(e.Data) == 0 {
		return nil
	}
	encoded, err := json.Marshal(e.Data)
	if err != nil {
		return nil
	}
	return encoded
}

// wants reports whether an event tag is forwarded.
func (r *Relay) wants(tag string) bool {
	for _, glob := range r.opts.EventTags {
		if eventbus.MatchTag(glob, tag) {
			return true
		}
	}
	return false
}

// subordinates is the current fleet, as the upstream should see it.
func (r *Relay) subordinates() []transport.Subordinate {
	connected := r.fleet().Connected()
	out := make([]transport.Subordinate, 0, len(connected))
	seen := map[string]bool{}
	for id := range connected {
		if id == r.opts.ID || seen[id] {
			continue
		}
		seen[id] = true
		sub := transport.Subordinate{NodeID: id}
		if data, err := r.opts.Server.NodeData(id); err == nil && data != nil {
			sub.Grains, sub.Version = data.Grains, data.Version
		}
		out = append(out, sub)
	}
	r.mu.Lock()
	r.reported = seen
	r.mu.Unlock()
	return out
}

// watchFleet tells the upstream when the subordinate set changes.
func (r *Relay) watchFleet(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !r.isConnected() {
				return
			}
			if !r.fleetChanged() {
				continue
			}
			subordinates := r.subordinates()
			sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := r.opts.Upstream.RelayUpdate(sendCtx, transport.RelayUpdate{
				Subordinates: subordinates,
			})
			cancel()
			if err != nil {
				r.opts.log("warn", "the upstream could not be told the fleet changed",
					"error", err.Error())
				continue
			}
			r.opts.log("info", "told the upstream the fleet changed",
				"subordinates", len(subordinates))
		}
	}
}

// fleetChanged reports whether the connected set differs from what the
// upstream was last told.
func (r *Relay) fleetChanged() bool {
	connected := r.fleet().Connected()
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for id := range connected {
		if id == r.opts.ID {
			continue
		}
		count++
		if !r.reported[id] {
			return true
		}
	}
	return count != len(r.reported)
}

func (r *Relay) setConnected(v bool) {
	r.mu.Lock()
	r.connected = v
	r.mu.Unlock()
}

func (r *Relay) isConnected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected
}

// Spooled reports how many returns are waiting, for
// `sys.list_extensions`-style introspection and for a metric.
func (r *Relay) Spooled() int { return r.spool.Count() }

// fleet is the relay's own subordinates, through the accessor rather
// than the field: the hub creates it lazily on the first connection,
// and the relay reads it before any node has arrived.
func (r *Relay) fleet() *hub.Fleet { return r.opts.Server.LiveFleet() }

// forwarded reports whether a jid is one the upstream dispatched and
// this relay passed down.
//
// A job submitted to the relay directly is the relay's own business:
// the upstream has no record of it, so a return for it comes back
// refused as an unknown jid — and before this check that refusal sat at
// the head of the spool for ever. The marker is the submitter that
// record wrote, so no separate index has to be kept correct.
func (r *Relay) forwarded(jid job.ID) bool {
	if r.opts.Server.Jobs == nil {
		return false
	}
	j, err := r.opts.Server.Jobs.Get(jid)
	if err != nil {
		return false
	}
	return j.Submitter == r.submitter()
}

// submitter is the marker record writes on a job that came from
// upstream.
func (r *Relay) submitter() string { return "relay:" + r.opts.ID }

// announce tells the upstream which nodes this relay proxies for.
func (r *Relay) announce(ctx context.Context) error {
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return r.opts.Upstream.RelayUpdate(sendCtx, transport.RelayUpdate{
		Subordinates: r.subordinates(),
	})
}
