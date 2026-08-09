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
	"log"
	"net/http"
	"time"

	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/event"
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
}

// Server is the control plane.
type Server struct {
	cfg       Config
	ca        *ca.Store
	registry  *registry
	bus       *event.Bus
	returners *returner.Manager
	log       *log.Logger
}

// New builds a control plane. It does not listen until Run is called.
func New(cfg Config, logger *log.Logger) *Server {
	cfg.withDefaults()
	return &Server{
		cfg:       cfg,
		ca:        &ca.Store{Dir: cfg.PKIDir},
		registry:  newRegistry(cfg.OnlineAfter, cfg.JobTTL),
		bus:       event.NewBus(),
		returners: returner.NewManager(cfg.Returners, logger),
		log:       logger,
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

	mux.Handle(transport.PathHello, s.agentOnly(s.handleHello))
	mux.Handle(transport.PathJobs, s.agentOnly(s.handleJobs))
	mux.Handle(transport.PathResults, s.agentOnly(s.handleResults))
	mux.Handle(transport.PathPillar, s.agentOnly(s.handlePillar))
	mux.Handle(transport.PathStateTree, s.agentOnly(s.handleStateTree))

	mux.Handle(transport.PathEvents, s.eventsHandler())
	mux.Handle(transport.PathDispatch, s.adminOnly(s.handleDispatch))
	mux.Handle(transport.PathAgents, s.adminOnly(s.handleAgents))
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
		ErrorLog:    s.log,
	}

	// Returners drain in their own goroutine for the life of the server, so
	// a slow sink never delays an agent's return.
	returnersDone := make(chan struct{})
	defer close(returnersDone)
	go s.returners.Run(returnersDone)

	if len(s.cfg.ReactorRules) > 0 {
		reactorCtx, stopReactor := context.WithCancel(ctx)
		defer stopReactor()
		engine := reactor.New(s.cfg.ReactorRules, s.Dispatch, s.log)
		s.log.Printf("reactor: %d rule(s) loaded", engine.Rules())
		go engine.Run(reactorCtx, s.bus)
	}

	done := make(chan error, 1)
	go func() {
		s.log.Printf("control plane listening on %s (states %s, pillar %s)",
			s.cfg.Addr, s.cfg.StatesRoot, s.cfg.PillarRoot)
		// The control plane holds the whole fleet's pillar, so a tree every
		// local account can read matters more here than anywhere else.
		if warning := pillar.PermissionWarning(s.cfg.PillarRoot); warning != "" {
			s.log.Print(warning)
		}
		for _, r := range s.cfg.Returners {
			s.log.Printf("returning results to %s", r.Name())
		}
		if s.cfg.AutoAccept {
			s.log.Print("WARNING: -auto-accept is on; any host that can reach this port will be enrolled")
		}
		err := srv.ListenAndServeTLS("", "")
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
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
		if !allow(peer.Role) {
			s.log.Printf("denied %s %s for %q", r.Method, r.URL.Path, peer.ID)
			writeError(w, http.StatusForbidden, "this operation requires an operator certificate")
			return
		}
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
