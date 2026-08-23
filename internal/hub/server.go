// Package hub is the control plane of SPEC section 6: the endpoints a
// node talks to, over the mutual-TLS transport.
//
// The rule the whole package follows is that a node's identity comes
// from the certificate the TLS layer verified and never from the
// request body. A body says what a node wants; the certificate says who
// is asking.
package hub

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/version"
)

// Server holds what the endpoints need.
type Server struct {
	Authority *keystore.Authority
	Log       *log.Logger
	// Now is the clock, for the tests.
	Now func() time.Time
	// PingInterval is how often an idle subscribe stream says
	// something, so that a node can tell a quiet hub from a dead one.
	PingInterval time.Duration

	// Fleet is the set of nodes with a live stream. It is created on
	// first use; a caller may set one to share it.
	Fleet     *Fleet
	fleetOnce sync.Once

	// Jobs is the job cache of SPEC 9.4. Without one the hub still
	// dispatches, and nothing can be looked up afterwards, so `serve`
	// always provides it.
	Jobs *job.Cache
	// Nodes is the node data cache of SPEC 8.3, which is what targeting
	// on a grain reads.
	Nodes     *NodeCache
	nodesOnce sync.Once
	// Nodegroups is the configured nodegroup table.
	Nodegroups target.Nodegroups

	jobClock job.Clock
}

// clock assigns job identifiers that do not collide.
func (s *Server) clock() *job.Clock {
	if s.jobClock.Now == nil && s.Now != nil {
		s.jobClock.Now = s.Now
	}
	return &s.jobClock
}

// nodes is the node data cache, or an empty one held in a temporary
// directory-free form: a hub with no cache still targets on node ID.
func (s *Server) nodes() *NodeCache {
	s.nodesOnce.Do(func() {
		if s.Nodes == nil {
			s.Nodes = &NodeCache{}
		}
	})
	return s.Nodes
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) pingInterval() time.Duration {
	if s.PingInterval > 0 {
		return s.PingInterval
	}
	return 30 * time.Second
}

func (s *Server) info(msg string, kv ...any) {
	if s.Log != nil {
		s.Log.Info(msg, kv...)
	}
}

func (s *Server) warn(msg string, kv ...any) {
	if s.Log != nil {
		s.Log.Warn(msg, kv...)
	}
}

// Handler routes SPEC 6.2's endpoints.
//
// Everything except health requires a client certificate, and enroll is
// the one exception in the other direction: it is reached without one
// because that is what it is for.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+transport.PathHealth, s.health)
	mux.HandleFunc("POST "+transport.PathEnroll, s.enroll)
	mux.HandleFunc("POST "+transport.PathEnrollRenew, s.authenticated(s.renew))
	mux.HandleFunc("POST "+transport.PathSubscribe, s.authenticated(s.subscribe))
	mux.HandleFunc("POST "+transport.PathReturn, s.authenticated(s.returned))
	mux.HandleFunc("POST "+transport.PathJobs, s.operator(s.submit))
	mux.HandleFunc("GET "+transport.PathJob+"{jid}", s.operator(s.jobStatus))
	// An unrouted path under /v1/ is a version skew or a scan, and
	// either way the answer is the same shape as every other failure.
	mux.HandleFunc("/", s.notFound)
	return mux
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	transport.WriteError(w, http.StatusNotFound, transport.CodeMalformed,
		fmt.Errorf("%s %s is not an endpoint this hub serves", r.Method, r.URL.Path))
}

// health is the only endpoint reachable without a client certificate,
// and it returns a fixed string with no state, per SPEC 6.2. It says
// the version because a load balancer's health check is the one request
// that is guaranteed to be made, and "which build is behind this
// address" is otherwise a question that needs a login.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "halite-hub %s ok\n", version.String())
}

// authenticated refuses a request that arrives without a verified
// client certificate, or with a revoked one.
//
// The revocation check is here as well as in the handshake because a
// connection outlives the handshake that opened it. A revoked node in
// the lab reconnected, reused its established HTTP/2 connection, and
// was served: the transport never saw a second ClientHello to refuse.
func (s *Server) authenticated(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peer, err := transport.PeerCert(r)
		if err != nil {
			transport.WriteError(w, http.StatusUnauthorized, transport.CodeRefused, err)
			return
		}
		nodeID, err := pki.NodeIDFromCert(peer)
		if err != nil {
			transport.WriteError(w, http.StatusUnauthorized, transport.CodeRefused, err)
			return
		}
		if revoker := s.Authority.Revoker; revoker != nil {
			if reason, revoked := revoker.Revoked(pki.SerialString(peer)); revoked {
				s.warn("a revoked node is still connected", "node_id", nodeID, "reason", reason)
				transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
					fmt.Errorf("this node's enrollment is revoked: %s", reason))
				return
			}
		}
		next(w, r, nodeID)
	}
}

// operator refuses a request that does not arrive on an operator
// certificate.
//
// A node's certificate authenticates a node and nothing else: SPEC 9.1
// has the hub authorize a submission against RBAC, and the first half
// of that is knowing that the peer is an operator at all. A node that
// could submit a job would be one compromised host driving the fleet.
func (s *Server) operator(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peer, err := transport.PeerCert(r)
		if err != nil {
			transport.WriteError(w, http.StatusUnauthorized, transport.CodeRefused, err)
			return
		}
		name, err := pki.OperatorFromCert(peer)
		if err != nil {
			transport.WriteError(w, http.StatusForbidden, transport.CodeRefused, err)
			return
		}
		if revoker := s.Authority.Revoker; revoker != nil {
			if reason, revoked := revoker.Revoked(pki.SerialString(peer)); revoked {
				transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
					fmt.Errorf("this operator certificate is revoked: %s", reason))
				return
			}
		}
		next(w, r, pki.Principal(name))
	}
}

// enroll is SPEC 7.3. The request arrives without a client certificate,
// because the whole purpose of it is to get one.
func (s *Server) enroll(w http.ResponseWriter, r *http.Request) {
	var req transport.EnrollRequest
	if err := transport.ReadJSON(w, r, transport.MaxRequestBody, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	res, err := s.Authority.Enroll(keystore.Request{
		CSR:        []byte(req.CSR),
		Token:      req.Token,
		RemoteAddr: r.RemoteAddr,
	})

	switch {
	case errors.Is(err, keystore.ErrPending):
		s.info("enrollment request is pending",
			"node_id", res.NodeID, "fingerprint", res.Fingerprint, "from", r.RemoteAddr)
		transport.WriteJSON(w, http.StatusAccepted, transport.EnrollResponse{
			NodeID:      res.NodeID,
			State:       string(keystore.Pending),
			Fingerprint: res.Fingerprint,
			Message:     "waiting for an operator to accept this request",
		})
	case errors.Is(err, keystore.ErrRefused):
		// The detail goes in the hub's log, where an operator can see
		// it. The node is told that it was refused.
		s.warn("enrollment refused", "from", r.RemoteAddr, "error", err.Error())
		transport.WriteError(w, http.StatusForbidden, transport.CodeRefused,
			errors.New("enrollment refused; the hub's log says why"))
	case err != nil:
		s.warn("enrollment failed", "from", r.RemoteAddr, "error", err.Error())
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
	default:
		s.info("enrollment issued",
			"node_id", res.NodeID, "fingerprint", res.Fingerprint, "from", r.RemoteAddr)
		transport.WriteJSON(w, http.StatusOK, transport.EnrollResponse{
			NodeID:      res.NodeID,
			State:       string(keystore.Accepted),
			Cert:        string(res.Cert),
			CA:          string(res.CABundle),
			Fingerprint: res.Fingerprint,
		})
	}
}

// renew is SPEC 7.4's automatic renewal: no operator, no token, because
// the node has already authenticated as itself on this very connection.
func (s *Server) renew(w http.ResponseWriter, r *http.Request, nodeID string) {
	var req transport.EnrollRequest
	if err := transport.ReadJSON(w, r, transport.MaxRequestBody, &req); err != nil {
		transport.WriteError(w, http.StatusBadRequest, transport.CodeMalformed, err)
		return
	}
	peer, err := transport.PeerCert(r)
	if err != nil {
		transport.WriteError(w, http.StatusUnauthorized, transport.CodeRefused, err)
		return
	}
	res, err := s.Authority.Renew(peer, []byte(req.CSR))
	if err != nil {
		s.warn("renewal refused", "node_id", nodeID, "error", err.Error())
		status := http.StatusForbidden
		if !errors.Is(err, keystore.ErrRefused) {
			status = http.StatusBadRequest
		}
		transport.WriteError(w, status, transport.CodeRefused, err)
		return
	}
	s.info("certificate renewed", "node_id", res.NodeID, "fingerprint", res.Fingerprint)
	// The stream this node is on was opened with the certificate that
	// has just been superseded. Ending it makes the node come back
	// holding the new one; leaving it open leaves a live stream
	// authenticated by a serial the hub has just revoked.
	s.fleet().Reload(res.NodeID, "certificate renewed")
	transport.WriteJSON(w, http.StatusOK, transport.EnrollResponse{
		NodeID:      res.NodeID,
		State:       string(keystore.Accepted),
		Cert:        string(res.Cert),
		CA:          string(res.CABundle),
		Fingerprint: res.Fingerprint,
	})
}
