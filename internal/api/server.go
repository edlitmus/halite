// Package api is the HTTP API of SPEC section 22.
//
// It is a client of the hub, not a component of it. In Salt the API
// process loads the control plane's own configuration and calls into
// its internals, so a flaw in the API is a flaw in the control plane.
// Here it holds an operator certificate like any other caller, and its
// worst case is bounded by the RBAC policy that certificate is bound
// to. <!-- lexicon:allow -->
package api

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/account"
	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/ldap"
	"github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/oidc"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/version"
)

// Server is the API service.
type Server struct {
	// Accounts is the local account file of SPEC 23.2, or nil for a
	// service with no local accounts.
	Accounts *account.File
	// Tokens is where issued tokens live.
	Tokens *apitoken.Store
	// Policy is the RBAC of SPEC 23.5. A nil policy authorizes nothing.
	Policy *policy.Policy
	// Hub is how this service reaches the control plane, holding its
	// own operator certificate.
	Hub *transport.Client
	// Hooks is the webhook ingress of SPEC 22.2. Nil is no hooks, and
	// an unconfigured path is a 404 like any other.
	Hooks *Hooks
	// OIDC is the identity provider of SPEC 23.4. Nil is no provider,
	// and a login naming one is refused by name.
	OIDC *oidc.Provider
	// LDAP is the directory of SPEC 23.3, reached through `/v1/login`
	// with `eauth: ldap` because unlike OIDC it is a username and a
	// password. Nil is no directory.
	LDAP *ldap.Client
	// Signatures back `/v1/schema`, so a client can discover what a
	// function takes without reading the documentation.
	Signatures *signature.Registry

	Log *log.Logger
	// Now is the clock, for the tests.
	Now func() time.Time

	// MaxBody bounds a request before it is parsed. Zero takes the
	// transport's limit.
	MaxBody int64
	// TokenLifetime and TokenIdle are what a login is given.
	TokenLifetime time.Duration
	TokenIdle     time.Duration

	// Metrics is the registry this service exposes at /v1/metrics,
	// alongside the hub's. Nil records nothing.
	Metrics *metrics.Registry

	metrics   *apiMetrics
	metricsMu sync.Mutex

	pendingAuths *pendingAuth
	pendingOnce  sync.Once
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) maxBody() int64 {
	if s.MaxBody > 0 {
		return s.MaxBody
	}
	return transport.MaxRequestBody
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

// The endpoints of SPEC 22.1 this build serves.
const (
	PathLogin = "/v1/login"
	// The interactive OIDC flow: start, come back, or present a token
	// the caller already holds.
	PathLoginOIDC      = "/v1/login/oidc"
	PathLoginOIDCBack  = "/v1/login/oidc/callback"
	PathLoginOIDCToken = "/v1/login/oidc/token"
	PathLogout         = "/v1/logout"
	PathToken          = "/v1/token"
	PathSchema         = "/v1/schema"
	PathRun            = "/v1/run"
	PathJobs           = "/v1/jobs"
	PathJob            = "/v1/jobs/"
	PathNodes          = "/v1/nodes"
	PathNode           = "/v1/nodes/"
	PathKeys           = "/v1/keys"
	PathKey            = "/v1/keys/"
	PathOrch           = "/v1/orch"
	PathOrchJob        = "/v1/orch/"
	PathPillar         = "/v1/pillar/"
	PathEvents         = "/v1/events"
	PathWSEvent        = "/v1/ws/events"
	PathHook           = "/v1/hook/"
	PathMetrics        = "/v1/metrics"
	PathHealthz        = "/v1/healthz"
	PathReadyz         = "/v1/readyz"
)

// Handler routes the API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Health says nothing about the estate: SPEC 22.3 requires that an
	// unauthenticated endpoint disclose no state, and "which hub am I
	// talking to" is state.
	mux.HandleFunc("GET "+PathHealthz, s.healthz)
	mux.HandleFunc("GET "+PathReadyz, s.readyz)

	mux.HandleFunc("POST "+PathLogin, s.login)
	// The OIDC path of SPEC 23.4, unauthenticated for the same reason
	// `/v1/login` is: it is how somebody with no token gets one.
	mux.HandleFunc("POST "+PathLoginOIDC, s.oidcStart)
	mux.HandleFunc("POST "+PathLoginOIDCBack, s.oidcCallback)
	mux.HandleFunc("POST "+PathLoginOIDCToken, s.oidcToken)
	mux.HandleFunc("POST "+PathLogout, s.authenticated(s.logout))
	mux.HandleFunc("GET "+PathToken, s.authenticated(s.introspect))
	mux.HandleFunc("GET "+PathSchema, s.authenticated(s.schema))
	mux.HandleFunc("GET "+PathMetrics, s.authenticated(s.metricsExposition))

	// The execution surface of SPEC 22.1. Every one of them authorizes
	// the operator behind the token here and is authorized again as
	// this service's own certificate at the hub.
	mux.HandleFunc("POST "+PathRun, s.authenticated(s.run))
	mux.HandleFunc("POST "+PathJobs, s.authenticated(s.submit))
	mux.HandleFunc("GET "+PathJobs, s.authenticated(s.jobList))
	mux.HandleFunc("GET "+PathJob+"{jid}", s.authenticated(s.jobDetail))
	mux.HandleFunc("DELETE "+PathJob+"{jid}", s.authenticated(s.jobDetail))
	mux.HandleFunc("GET "+PathNodes, s.authenticated(s.nodeList))
	mux.HandleFunc("GET "+PathNode+"{id}", s.authenticated(s.nodeDetail))
	mux.HandleFunc("POST "+PathNode+"{id}/state", s.authenticated(s.nodeDetail))
	mux.HandleFunc("GET "+PathKeys, s.authenticated(s.keys))
	mux.HandleFunc("POST "+PathKeys, s.authenticated(s.keys))
	mux.HandleFunc("DELETE "+PathKey+"{id}", s.authenticated(s.keys))
	mux.HandleFunc("POST "+PathOrch, s.authenticated(s.orchestrate))
	mux.HandleFunc("GET "+PathOrchJob+"{jid}", s.authenticated(s.orchDetail))
	mux.HandleFunc("GET "+PathPillar+"{id}", s.authenticated(s.pillar))

	// The event stream, both ways of taking it, filtered by the
	// caller's policy so that nobody subscribes to events about nodes
	// they may not see. SPEC 17.4.
	mux.HandleFunc("GET "+PathEvents, s.authenticated(s.eventStream))
	mux.HandleFunc("GET "+PathWSEvent, s.authenticated(s.wsEventStream))

	// Webhook ingress authenticates per path rather than by a token,
	// because the caller is an external system with a shared secret
	// rather than an operator with a session. SPEC 22.2.
	mux.HandleFunc("POST "+PathHook+"{path...}", s.hook)

	// An unrouted path is a version skew or a scan, and says so without
	// listing what does exist.
	mux.HandleFunc("/", s.notFound)
	return s.hardened(s.logged(mux))
}

// hardened applies the response headers and the body limit of SPEC
// 22.3 to everything.
func (s *Server) hardened(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// No content sniffing, no framing, no referrer, and nothing
		// executable: this serves JSON to programs, so the strictest
		// policy is also the one that costs nothing.
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		// Only over TLS, which is the only way this is served.
		h.Set("Strict-Transport-Security", "max-age=31536000")

		r.Body = http.MaxBytesReader(w, r.Body, s.maxBody())
		next.ServeHTTP(w, r)
	})
}

// logged writes the structured access record SPEC 22.3 calls the audit
// record for the estate.
func (s *Server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := s.now()
		// The route is resolved before the handler runs so that the
		// in-flight gauge names the same route the completed counter
		// will: a gauge that goes up under one label and down under
		// another leaks a series that never returns to zero.
		route := routeOf(r)
		s.m().requestsInFlight.With(route).Add(1)
		defer s.m().requestsInFlight.With(route).Add(-1)

		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// The principal is whatever the handler resolved, which is
		// empty for an unauthenticated request and is the point of
		// logging it either way.
		elapsed := s.now().Sub(started)
		s.info("api request",
			"method", r.Method,
			"path", r.URL.Path,
			"principal", rec.principal,
			"status", rec.status,
			"remote", remoteHost(r),
			"duration_ms", elapsed.Milliseconds())

		// Counted by route rather than by path: `/v1/jobs/{jid}` is
		// one route and a series per job identifier is exactly the
		// unbounded label a metrics endpoint dies of.
		m := s.m()
		m.requests.With(route, strconv.Itoa(rec.status)).Inc()
		if elapsed >= 0 {
			m.requestDuration.With(route).Observe(elapsed.Seconds())
		}
		m.responseBytes.With(route).Add(float64(rec.bytes))
	})
}

// routeOf names the route a request matched, with the variable segments
// removed.
func routeOf(r *http.Request) string {
	path := r.URL.Path
	for _, prefix := range []string{PathJob, PathNode, PathKey, PathOrchJob, PathPillar, PathHook} {
		if rest, ok := strings.CutPrefix(path, prefix); ok {
			// `/v1/nodes/{id}/state` keeps its tail, because applying
			// state to a node and reading one are different routes.
			if _, tail, found := strings.Cut(rest, "/"); found && tail != "" {
				return prefix + "{id}/" + tail
			}
			return prefix + "{id}"
		}
	}
	return path
}

// recorder captures what a handler answered, for the access log.
type recorder struct {
	http.ResponseWriter
	status    int
	principal string
	wrote     bool
	// bytes is what was written to the client, for the response-size
	// counter. An event stream that never ends contributes as it goes,
	// because the record is written when the handler returns.
	bytes int64
}

func (w *recorder) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *recorder) Write(b []byte) (int, error) {
	if !w.wrote {
		w.wrote = true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush lets a streaming handler through the recorder.
func (w *recorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack lets the WebSocket endpoint take the connection.
//
// Without this the wrapper hides the hijacker the server actually
// provides, and every upgrade is refused with "this connection cannot be
// upgraded" — a middleware that only meant to count status codes
// silently removing an endpoint.
func (w *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("this connection cannot be hijacked")
	}
	conn, buffered, err := hijacker.Hijack()
	if err == nil {
		// The handshake writes its own status line past this wrapper,
		// so the access log is told what it was.
		w.wrote = true
		w.status = http.StatusSwitchingProtocols
	}
	return conn, buffered, err
}

// namePrincipal records who a request turned out to be, for the log.
func namePrincipal(w http.ResponseWriter, principal string) {
	if rec, ok := w.(*recorder); ok {
		rec.principal = principal
	}
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authenticated resolves the bearer token and refuses without one.
func (s *Server) authenticated(next func(http.ResponseWriter, *http.Request, *apitoken.Token)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret, ok := bearer(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "no token was presented")
			return
		}
		token, err := s.Tokens.Redeem(secret, r.RemoteAddr)
		if err != nil {
			// The reason is returned: an operator whose token expired
			// and one whose token was revoked need different actions,
			// and neither learns anything about anyone else's.
			s.warn("token refused", "remote", remoteHost(r), "error", err.Error())
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		namePrincipal(w, token.Principal)
		next(w, r, token)
	}
}

// bearer reads the Authorization header.
//
// The header and nothing else: SPEC 23.6 keeps token material out of a
// URL, because a query parameter reaches an access log, a browser
// history, and a Referer header.
func bearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound,
		fmt.Sprintf("%s %s is not an endpoint this service serves", r.Method, r.URL.Path))
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

// readyz says whether the service can do its job, without saying what
// its job reaches.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.Hub == nil {
		writeError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	if _, err := s.Hub.Health(r.Context()); err != nil {
		// The reason stays in the log. A probe endpoint that reports
		// which upstream is unreachable is one that maps the estate
		// for anyone who can reach it.
		s.warn("readiness probe failed", "error", err.Error())
		writeError(w, http.StatusServiceUnavailable, "not ready")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ready")
}

// schema is the module signatures of SPEC 15.6, which is how a client
// discovers what a function takes.
func (s *Server) schema(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	if s.Signatures == nil {
		writeError(w, http.StatusServiceUnavailable, "this service ships no module schema")
		return
	}
	writeJSON(w, http.StatusOK, s.Signatures.JSON())
}

// Listen opens the API's port with TLS 1.3 only, per SPEC 22.3.
func Listen(addr string, cert tls.Certificate) (net.Listener, error) {
	if addr == "" {
		addr = ":4511"
	}
	ln, err := tls.Listen("tcp", addr, &tls.Config{
		Certificates: []tls.Certificate{cert},
		// 1.3 only. Everything that talks to this is a program or a
		// current browser, so there is nothing to be compatible with
		// and nothing to gain by offering less.
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"h2", "http/1.1"},
	})
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", addr, err)
	}
	return ln, nil
}

// Serve runs the API until the context ends.
func (s *Server) Serve(ln net.Listener) *http.Server {
	return &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Minute,
		// No write timeout: the event stream of SPEC 22.1 is meant to
		// stay open, and a deadline would cut it at a fixed age. The
		// idle timeout is what closes a connection nobody is using.
		IdleTimeout: 90 * time.Second,
		ErrorLog:    nil,
	}
}

// writeJSON sends a value with the canonical settings of SPEC 6.4.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// Error is the shape of every failure this service returns.
type Error struct {
	Error string `json:"error"`
}

// writeError sends a failure in the one shape.
//
// A sentence, never a stack trace: SPEC 22.3 keeps an internal detail
// out of a response body, because the body is the part an attacker
// reads.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Error{Error: msg})
}

// APIVersion is what `/v1/token` reports alongside the principal, so a
// client can tell which build it is talking to.
var APIVersion = version.String
