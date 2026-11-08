package hub

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

// DefaultMaxNodeConnections is SPEC 6.5's "maximum node connections per
// hub", used unless `max_node_connections` says otherwise.
//
// What is counted is subscribe streams: the one long-lived connection
// each node holds, which is what the hub keeps a goroutine, a queue of
// queueDepth messages and a file descriptor for, for as long as the
// node is up. A relay is one, whatever it has behind it -- that is what
// a relay is for (SPEC 5.3), and why the handbook's 100,000 is "with
// relays".
//
// Not every TCP connection that presents a node certificate. A node
// opens other requests beside its stream -- a return, a pillar, a file
// -- and they are short and bounded by their own limits. Refusing one of
// those because the hub is full would lose a job's result to protect a
// resource the result never held, and a cap counted per TCP connection
// would count a node twice while it posted a return. The stream is the
// thing that grows with the estate and stays.
//
// Nor is the refusal made at the TLS handshake, which is where a cap
// counted at accept would have to make it. Go's server answers a failed
// VerifyConnection with a `bad certificate` alert and nothing else, and
// a node reads that alert as its own revocation (transport.Rejected):
// it forgets its cached pillar. A hub that was merely full would have
// told every node over the cap that it had been thrown out.
const DefaultMaxNodeConnections = 25000

func (s *Server) maxNodeConnections() int {
	if s.MaxNodeConnections > 0 {
		return s.MaxNodeConnections
	}
	return DefaultMaxNodeConnections
}

// full reports whether attaching nodeID would take the fleet past max.
//
// subscribe asks this before it reads the request body, so that a node
// over the cap is refused before the hub reads up to MaxGrainsPayload
// from it. The answer is advisory: two nodes can both pass it with one
// slot left. attachLimited asks again under the lock that attaches, and
// that is what decides which of them gets the slot.
// A node that already holds a stream is never refused: its new stream
// replaces the old one and the count does not move, and refusing it
// would leave it with a stream the hub is about to stop reading.
func (f *Fleet) full(nodeID string, max int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fullLocked(nodeID, max)
}

func (f *Fleet) fullLocked(nodeID string, max int) bool {
	if _, ok := f.streams[nodeID]; ok {
		return false
	}
	return len(f.streams) >= max
}

// connLimitLog spaces out the warning a full hub logs. A hub at its cap
// is refused by every node over it on every reconnect -- at 5,000 nodes
// over, each backing off to its ceiling, that is still hundreds of
// refusals a minute -- and a line for each would bury the log the
// operator opens to find out why. The counter has every one.
type connLimitLog struct {
	mu      sync.Mutex
	last    time.Time
	skipped int
}

const connLimitLogEvery = time.Minute

// refuseOverCap answers a subscribe the cap does not allow.
//
// 503, because the hub is up and this is about the hub, not the node:
// nothing about the request was wrong, and the same request will
// succeed once a node leaves. A node reads a 503 on its stream as an
// outage (transport.Unreachable), keeps running from what it last
// received, and reconnects on its own backoff. A 4xx would be a
// RefusedError, which is the hub deciding against this node; a 401 or
// 403 makes it forget its pillar.
//
// No Retry-After: the node's subscribe does not read one, and a header
// nothing acts on reads like a promise.
func (s *Server) refuseOverCap(w http.ResponseWriter, nodeID string, max int) {
	s.m().nodeConnectRefused.Inc()
	s.connLimitLog.mu.Lock()
	now := s.now()
	var skipped int
	logIt := s.connLimitLog.last.IsZero() || now.Sub(s.connLimitLog.last) >= connLimitLogEvery
	if logIt {
		skipped = s.connLimitLog.skipped
		s.connLimitLog.last, s.connLimitLog.skipped = now, 0
	} else {
		s.connLimitLog.skipped++
	}
	s.connLimitLog.mu.Unlock()
	if logIt {
		s.warn("refused a node connection: the hub holds max_node_connections streams",
			"node_id", nodeID, "max_node_connections", max,
			"refused_since_last_warning", skipped)
	}
	transport.WriteError(w, http.StatusServiceUnavailable, transport.CodeHubFull,
		fmt.Errorf("this hub already holds %d node connections, its max_node_connections; "+
			"try again later, or connect through a relay", max))
}
