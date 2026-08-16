// Package master is halite's control plane: an mTLS HTTP/2 server that
// enrolls agents, hands them the state tree and their pillar, dispatches
// jobs by target, and collects results.
//
// The server holds its state in memory. A restart forgets which agents
// were online and what jobs ran; agents reconnect on their next poll and
// carry on. Durable job history is a returner concern (P3), not something
// the control plane should grow a database for.
package master

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/event"
	"github.com/edlitmus/halite/internal/logging"
	"github.com/edlitmus/halite/internal/orch"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/reactor"
	"github.com/edlitmus/halite/internal/returner"
	"github.com/edlitmus/halite/internal/transport"
)

// Config is what the control plane needs to run.
type Config struct {
	Addr       string // listen address, host:port
	PKIDir     string
	StatesRoot string
	PillarRoot string

	// AutoAccept signs enrollment requests without an operator decision.
	// It exists for labs and disposable test fleets; on a real network it
	// means anyone who can reach the port becomes a fleet member.
	AutoAccept bool

	// MaxPendingEnrollments caps how many enrollment requests may wait for
	// an operator at once. Enrollment is unauthenticated, so the cap is
	// what stands between the open port and a full disk. Zero means the
	// CA's default (ca.DefaultMaxPending).
	MaxPendingEnrollments int

	// EnrollRate is how many enrollment requests one source address may
	// make per minute. It bounds the time an unauthenticated caller can
	// spend on the control plane's behalf, where the pending cap bounds
	// the disk. Zero means DefaultEnrollRate.
	EnrollRate int

	// PollTimeout is how long an agent's job poll is held open before it is
	// answered with an empty list.
	PollTimeout time.Duration

	// OnlineAfter is how stale an agent's last contact may be before it
	// stops being a dispatch target.
	OnlineAfter time.Duration

	// JobTTL bounds how long queued work stays runnable. An agent that was
	// down when a job was dispatched picks up nothing older than this.
	JobTTL time.Duration

	// Returners are durable sinks for finished job results.
	Returners []returner.Returner

	// ReactorRules turn events into jobs.
	ReactorRules []reactor.Rule

	// OrchRoot holds orchestration SLS files.
	OrchRoot string
	// OrchTimeout bounds a whole orchestration; OrchStepTimeout bounds one
	// step's wait for its agents.
	OrchTimeout     time.Duration
	OrchStepTimeout time.Duration
}

// reactorSource is the identity reacted work is dispatched under. It is
// not a certificate: the reactor runs inside the control plane.
const reactorSource = "reactor"

func (c *Config) withDefaults() {
	if c.Addr == "" {
		c.Addr = fmt.Sprintf(":%d", transport.DefaultPort)
	}
	if c.PollTimeout == 0 {
		c.PollTimeout = 30 * time.Second
	}
	if c.OnlineAfter == 0 {
		// Two poll cycles plus slack: an agent mid-poll is still online.
		c.OnlineAfter = 3 * c.PollTimeout
	}
	if c.JobTTL == 0 {
		c.JobTTL = 5 * time.Minute
	}
	if c.OrchRoot == "" {
		c.OrchRoot = defaultOrchRoot(c.StatesRoot)
	}
	if c.OrchTimeout == 0 {
		c.OrchTimeout = DefaultOrchTimeout
	}
	if c.OrchStepTimeout == 0 {
		c.OrchStepTimeout = orch.DefaultStepTimeout
	}
}

// Server is the control plane.
type Server struct {
	cfg       Config
	ca        *ca.Store
	registry  *registry
	bus       *event.Bus
	returners *returner.Manager
	log       *logging.Logger

	// enrollLimit paces the one route that answers before authentication.
	enrollLimit *rateLimiter

	// refusals throttles the log when a revoked agent keeps trying.
	refusals struct {
		mu sync.Mutex
		at map[string]time.Time
	}
}

// New builds a control plane. It does not listen until Run is called.
func New(cfg Config, logger *logging.Logger) *Server {
	cfg.withDefaults()
	return &Server{
		cfg:         cfg,
		ca:          &ca.Store{Dir: cfg.PKIDir},
		registry:    newRegistry(cfg.OnlineAfter, cfg.JobTTL),
		bus:         event.NewBus(),
		returners:   returner.NewManager(cfg.Returners, logger),
		log:         logger,
		enrollLimit: newRateLimiter(cfg.EnrollRate),
	}
}

// Bus exposes the event bus so returners and the reactor can subscribe
// before the server starts listening.
func (s *Server) Bus() *event.Bus { return s.bus }

// Handler builds the routing table. Only enrollment is reachable without a
// client certificate; everything else is wrapped in a role check.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(transport.PathEnroll, s.handleEnroll)

	mux.Handle(transport.PathRenew, s.agentOnly(s.handleRenew))
	mux.Handle(transport.PathHello, s.agentOnly(s.handleHello))
	mux.Handle(transport.PathJobs, s.agentOnly(s.handleJobs))
	mux.Handle(transport.PathResults, s.agentOnly(s.handleResults))
	mux.Handle(transport.PathPillar, s.agentOnly(s.handlePillar))
	mux.Handle(transport.PathStateTree, s.agentOnly(s.handleStateTree))

	mux.Handle(transport.PathEvents, s.eventsHandler())
	mux.Handle(transport.PathMine, s.mineHandler())
	mux.Handle(transport.PathDispatch, s.adminOnly(s.handleDispatch))
	mux.Handle(transport.PathAgents, s.adminOnly(s.handleAgents))
	mux.Handle(transport.PathOrchestrate, s.adminOnly(s.handleOrchestrate))
	mux.Handle(transport.PathOrchInfo, s.adminOnly(s.handleOrchInfo))
	mux.Handle(transport.PathJobInfo, s.adminOnly(s.handleJobInfo))
	return mux
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	tlsCfg, err := transport.ServerTLS(
		s.ca.Dir+"/master.crt", s.ca.Dir+"/master.key", s.ca.Dir+"/ca.crt")
	if err != nil {
		return fmt.Errorf("%w (run 'halite key init' and 'halite key server <hostname>')", err)
	}
	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.Handler(),
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 15 * time.Second,
		// Job polls are long-lived, so no write timeout; the poll bounds
		// itself and idle connections are reaped below.
		IdleTimeout: 2 * s.cfg.PollTimeout,
		// Warn, because ClientAuth is VerifyClientCertIfGiven: a caller
		// with no certificate finishes the handshake and is turned away
		// by the handlers, so what reaches here is a caller that offered
		// one and failed — a foreign or expired certificate being tried.
		ErrorLog: s.log.StdLogger(logging.Warn),
	}

	// Returners drain in their own goroutines for the life of the server, so
	// a slow sink never delays an agent's return. stopReturners closes the
	// intake and waits for the flush; it must run only after the HTTP server
	// has stopped, so no handler is left submitting into a closed manager.
	returnersDone := make(chan struct{})
	returnersFinished := make(chan struct{})
	go func() {
		s.returners.Run(returnersDone)
		close(returnersFinished)
	}()
	stopReturners := func() {
		close(returnersDone)
		<-returnersFinished
	}

	if len(s.cfg.ReactorRules) > 0 {
		reactorCtx, stopReactor := context.WithCancel(ctx)
		defer stopReactor()
		engine := reactor.New(s.cfg.ReactorRules, s.Dispatch, s.log)
		s.log.Infof("reactor: %d rule(s) loaded", engine.Rules())
		go engine.Run(reactorCtx, s.bus)
	}

	done := make(chan error, 1)
	go func() {
		s.log.Infof("control plane listening on %s (states %s, pillar %s)",
			s.cfg.Addr, s.cfg.StatesRoot, s.cfg.PillarRoot)
		// The control plane holds the whole fleet's pillar, so a tree every
		// local account can read matters more here than anywhere else.
		if warning := pillar.PermissionWarning(s.cfg.PillarRoot); warning != "" {
			s.log.Warnf("%s", warning)
		}
		for _, r := range s.cfg.Returners {
			s.log.Infof("returning results to %s", r.Name())
		}
		if s.cfg.AutoAccept {
			s.log.Warnf("-auto-accept is on; any host that can reach this port will be enrolled")
		}
		err := srv.ListenAndServeTLS("", "")
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()

	select {
	case err := <-done:
		stopReturners()
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		// Every handler has returned, so nothing new can be submitted; flush
		// what was accepted before letting the process exit.
		stopReturners()
		return err
	}
}

// agentOnly admits any enrolled identity: agents, and operators too, since
// an operator certificate is strictly more privileged.
func (s *Server) agentOnly(h func(http.ResponseWriter, *http.Request, transport.Peer)) http.Handler {
	return s.authorized(h, func(ca.Role) bool { return true })
}

// adminOnly admits operator certificates. Dispatching work is the one
// thing an agent must never be able to do: a compromised agent that could
// dispatch would own the fleet.
func (s *Server) adminOnly(h func(http.ResponseWriter, *http.Request, transport.Peer)) http.Handler {
	return s.authorized(h, func(role ca.Role) bool { return role == ca.RoleAdmin })
}

func (s *Server) authorized(
	h func(http.ResponseWriter, *http.Request, transport.Peer),
	allow func(ca.Role) bool,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer, ok := transport.PeerOf(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "a client certificate issued by this fleet's CA is required")
			return
		}
		if s.ca.IsRevoked(peer.ID) {
			// Read from the store per request, so `halite key revoke`
			// takes effect on a running control plane. The certificate is
			// still cryptographically valid — this is the door it opens
			// being shut, not the key being destroyed.
			s.noteRefusal(peer.ID, r.URL.Path)
			writeError(w, http.StatusForbidden, "the certificate for %q has been revoked", peer.ID)
			return
		}
		if !allow(peer.Role) {
			s.log.Warnf("denied %s %s for %q", r.Method, r.URL.Path, peer.ID)
			writeError(w, http.StatusForbidden, "this operation requires an operator certificate")
			return
		}
		// One line per authenticated request is far too much for a
		// running fleet and exactly what is wanted when an agent is not
		// getting what it asks for.
		s.log.Debugf("%s %s from %q", r.Method, r.URL.Path, peer.ID)
		r.Body = http.MaxBytesReader(w, r.Body, transport.MaxBodyBytes)
		h(w, r, peer)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already out; there is nothing left to say.
		return
	}
}

func writeError(w http.ResponseWriter, status int, format string, a ...any) {
	writeJSON(w, status, transport.ErrorResponse{Error: fmt.Sprintf(format, a...)})
}

// decode reads a JSON body into v.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("malformed request body: %w", err)
	}
	return nil
}

// refusalInterval is how often one revoked identity is worth a line in
// the log. A revoked agent keeps retrying — it has no way to know why it
// is being turned away — and an operator needs to see that it is out
// there without losing everything else in the log to it.
const refusalInterval = 5 * time.Minute

// noteOnce runs report at most once per refusalInterval for a key. Both
// callers are things a rejected caller repeats until somebody notices, so
// the log has to say them without being buried in them.
func (s *Server) noteOnce(key string, report func()) {
	s.refusals.mu.Lock()
	last, seen := s.refusals.at[key]
	if seen && time.Since(last) < refusalInterval {
		s.refusals.mu.Unlock()
		return
	}
	if s.refusals.at == nil {
		s.refusals.at = map[string]time.Time{}
	}
	s.refusals.at[key] = time.Now()
	s.refusals.mu.Unlock()
	report()
}

// noteRefusal logs and announces that a revoked identity tried to work.
func (s *Server) noteRefusal(id, path string) {
	s.noteOnce("revoked:"+id, func() {
		s.log.Warnf("refused %s for %q: revoked", path, id)
		s.bus.Emit(fmt.Sprintf(event.TagKeyRefused, id), event.SourceMaster,
			map[string]any{"id": id, "path": path})
	})
}

// noteFlood logs and announces that a source address is enrolling faster
// than it is allowed to.
func (s *Server) noteFlood(source string) {
	s.noteOnce("flood:"+source, func() {
		s.log.Warnf("throttling enrollment from %s: more than %d a minute",
			source, s.enrollLimit.perMinute)
		s.bus.Emit(event.TagEnrollThrottled, event.SourceMaster,
			map[string]any{"source": source, "per_minute": s.enrollLimit.perMinute})
	})
}
