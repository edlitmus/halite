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
}

func (st *stream) close() {
	st.once.Do(func() { close(st.done) })
}

// queueDepth is how far a node may fall behind before the hub stops
// pretending it is keeping up. A slow node must not hold the hub's
// memory, and a dropped job is visible in the return; a hub that grows
// without bound is not visible until it is fatal.
const queueDepth = 64

func newFleet() *Fleet { return &Fleet{streams: map[string]*stream{}} }

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

// Send offers a message to one node. It reports false if the node is
// not connected or is too far behind to take it.
func (f *Fleet) Send(nodeID string, msg transport.Message) bool {
	f.mu.Lock()
	st, ok := f.streams[nodeID]
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

// Connected lists the nodes with a live stream, and since when.
func (f *Fleet) Connected() map[string]time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]time.Time, len(f.streams))
	for id, st := range f.streams {
		out[id] = st.since
	}
	return out
}
