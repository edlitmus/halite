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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/account"
	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/log"
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
	PathLogin   = "/v1/login"
	PathLogout  = "/v1/logout"
	PathToken   = "/v1/token"
	PathSchema  = "/v1/schema"
	PathHealthz = "/v1/healthz"
	PathReadyz  = "/v1/readyz"
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
	mux.HandleFunc("POST "+PathLogout, s.authenticated(s.logout))
	mux.HandleFunc("GET "+PathToken, s.authenticated(s.introspect))
	mux.HandleFunc("GET "+PathSchema, s.authenticated(s.schema))

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
		rec := &recorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		// The principal is whatever the handler resolved, which is
		// empty for an unauthenticated request and is the point of
		// logging it either way.
		s.info("api request",
			"method", r.Method,
			"path", r.URL.Path,
			"principal", rec.principal,
			"status", rec.status,
			"remote", remoteHost(r),
			"duration_ms", s.now().Sub(started).Milliseconds())
	})
}

// recorder captures what a handler answered, for the access log.
type recorder struct {
	http.ResponseWriter
	status    int
	principal string
	wrote     bool
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
	return w.ResponseWriter.Write(b)
}

// Flush lets a streaming handler through the recorder.
func (w *recorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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
