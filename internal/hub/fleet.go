package hub

import (
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// Fleet is the set of nodes with a live subscribe stream.
//
// One stream per node: a second connection from the same identity
// replaces the first, because a node that reconnected after a network
// partition would otherwise leave a stream nobody is reading behind,
// and jobs would be delivered into it.
type Fleet struct {
	mu      sync.Mutex
	streams map[string]*stream
	// via maps a subordinate node to the relay whose stream reaches
	// it. SPEC 5.3: a relay presents itself upstream as a single
	// client, so a job for a node behind one is written to the relay's
	// stream and the relay dispatches it onward.
	via map[string]string
}

// stream is one node's outbound queue.
type stream struct {
	nodeID string
	out    chan transport.Message
	// done is closed when the stream is replaced or shut down, so a
	// handler blocked on a write wakes up.
	done chan struct{}
	once sync.Once
	// since is when the node connected, for `keys list` and the
	// operator's question "is it up".
	since time.Time
	// pong is when the node last answered a ping. A node whose stream
	// is open but which has stopped answering is a half-open
	// connection, which looks identical to a healthy one from the
	// hub's side until something asks.
	pongMu sync.Mutex
	pong   time.Time
}

func (st *stream) sawPong(at time.Time) {
	st.pongMu.Lock()
	defer st.pongMu.Unlock()
	st.pong = at
}

func (st *stream) lastPong() time.Time {
	st.pongMu.Lock()
	defer st.pongMu.Unlock()
	return st.pong
}

func (st *stream) close() {
	st.once.Do(func() { close(st.done) })
}

// queueDepth is how far a node may fall behind before the hub stops
// pretending it is keeping up. A slow node must not hold the hub's
// memory, and a dropped job is visible in the return; a hub that grows
// without bound is not visible until it is fatal.
const queueDepth = 64

func newFleet() *Fleet {
	return &Fleet{streams: map[string]*stream{}, via: map[string]string{}}
}

// LiveFleet is this hub's fleet, created if nothing has needed it yet.
//
// The Fleet field is created lazily on the first connection, so a
// caller that reads it before any node has arrived — a relay, which
// reports its subordinates upstream at connection time — reads nil.
func (s *Server) LiveFleet() *Fleet { return s.fleet() }

// NewFleet answers with an empty one.
//
// Exported so a caller that needs the fleet before the hub has served
// anything — a relay, which reports its subordinates upstream at
// connection time — can create it and hand the same one to the server.
func NewFleet() *Fleet { return newFleet() }

func (f *Fleet) attach(nodeID string, now time.Time) *stream {
	st := &stream{
		nodeID: nodeID,
		out:    make(chan transport.Message, queueDepth),
		done:   make(chan struct{}),
		since:  now,
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if old, ok := f.streams[nodeID]; ok {
		old.close()
	}
	f.streams[nodeID] = st
	return st
}

// detach removes a stream, unless it has already been replaced by a
// newer one from the same node.
func (f *Fleet) detach(st *stream) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cur, ok := f.streams[st.nodeID]; ok && cur == st {
		delete(f.streams, st.nodeID)
	}
	st.close()
}

// Relay records that a set of nodes is reachable through one
// connection.
//
// Replaced wholesale for that relay rather than merged, because a
// subordinate that has gone must stop being dispatched to: an update
// that only added would keep sending jobs to a node the relay can no
// longer reach, and report it unresponsive rather than absent.
func (f *Fleet) Relay(relayID string, subordinates []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for node, via := range f.via {
		if via == relayID {
			delete(f.via, node)
		}
	}
	for _, node := range subordinates {
		// A relay may not claim a node that is connected directly.
		// Otherwise a relay could take over delivery for any node in
		// the estate by naming it, which is a job going somewhere the
		// operator did not intend.
		if _, direct := f.streams[node]; direct {
			continue
		}
		f.via[node] = relayID
	}
}

// RelayFor answers with the relay a node is reached through, if any.
func (f *Fleet) RelayFor(nodeID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	via, ok := f.via[nodeID]
	return via, ok
}

// dropRelay forgets the subordinates of a relay that has gone.
func (f *Fleet) dropRelay(relayID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for node, via := range f.via {
		if via == relayID {
			delete(f.via, node)
		}
	}
}

// Send offers a message to one node. It reports false if the node is
// not connected or is too far behind to take it.
func (f *Fleet) Send(nodeID string, msg transport.Message) bool {
	f.mu.Lock()
	st, ok := f.streams[nodeID]
	if !ok {
		// Not connected directly. A relay may still reach it, and the
		// message goes down the relay's stream naming the node it is
		// for.
		if via, relayed := f.via[nodeID]; relayed {
			st, ok = f.streams[via]
			msg.Node = nodeID
		}
	}
	f.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case st.out <- msg:
		return true
	case <-st.done:
		return false
	default:
		return false
	}
}

// Broadcast offers a message to every connected node and reports how
// many took it.
func (f *Fleet) Broadcast(msg transport.Message) int {
	f.mu.Lock()
	ids := make([]string, 0, len(f.streams))
	for id := range f.streams {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	n := 0
	for _, id := range ids {
		if f.Send(id, msg) {
			n++
		}
	}
	return n
}

// Disconnect ends a node's stream, after trying to tell it why.
func (f *Fleet) Disconnect(nodeID, reason string) bool {
	return f.end(nodeID, transport.Message{T: transport.MsgRevoke, Reason: reason, Final: true})
}

// Reload ends a node's stream and asks it to come back.
//
// Renewal uses this: the node is holding a certificate the hub has just
// superseded, and the stream it opened with that certificate would
// otherwise outlive it. Ending the stream makes the node reconnect,
// which makes it read the certificate it has just been issued.
func (f *Fleet) Reload(nodeID, reason string) bool {
	return f.end(nodeID, transport.Message{T: transport.MsgReload, Reason: reason, Final: true})
}

func (f *Fleet) end(nodeID string, msg transport.Message) bool {
	f.mu.Lock()
	st, ok := f.streams[nodeID]
	f.mu.Unlock()
	if !ok {
		return false
	}
	// Best effort: a node that is not reading gets closed on anyway,
	// and if it was revoked its next handshake is refused regardless.
	select {
	case st.out <- msg:
		// Give the writer a moment to put the line on the wire before
		// the stream is torn down under it.
		time.Sleep(10 * time.Millisecond)
	default:
	}
	st.close()
	return true
}

// sawPong records that a node answered a ping.
func (f *Fleet) sawPong(nodeID string, at time.Time) {
	f.mu.Lock()
	st, ok := f.streams[nodeID]
	f.mu.Unlock()
	if ok {
		st.sawPong(at)
	}
}

// LastPong is when a node last answered, zero if it never has.
func (f *Fleet) LastPong(nodeID string) time.Time {
	f.mu.Lock()
	st, ok := f.streams[nodeID]
	f.mu.Unlock()
	if !ok {
		return time.Time{}
	}
	return st.lastPong()
}

// Connected lists the nodes with a live stream, and since when.
// Relayed is every node reachable through a relay, with the relay.
func (f *Fleet) Relayed() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.via))
	for node, via := range f.via {
		out[node] = via
	}
	return out
}

// Connected is every node a job can reach: directly connected ones, and
// the subordinates of connected relays.
//
// A relayed node is connected in the sense that matters — a job for it
// will be delivered — and reporting it otherwise would make
// `manage.up` on a relayed estate answer with the relays alone.
func (f *Fleet) Connected() map[string]time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]time.Time, len(f.streams)+len(f.via))
	for id, st := range f.streams {
		out[id] = st.since
	}
	for node, via := range f.via {
		if st, ok := f.streams[via]; ok {
			// Since the relay connected: the hub does not know when
			// the subordinate did, and inventing a time would be worse
			// than the relay's own.
			out[node] = st.since
		}
	}
	return out
}
