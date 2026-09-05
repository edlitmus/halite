package api

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/oidc"
)

// AuthStateTTL is how long an interactive login may sit between the
// redirect out and the callback back.
//
// Short: it is the time a person takes to log in at their provider, not
// a session. A pending login is a nonce and a PKCE verifier held in
// memory, and holding them for hours widens the window in which a stolen
// authorization code is worth anything.
const AuthStateTTL = 10 * time.Minute

// pendingAuth holds the interactive logins that have been started and
// not yet come back.
//
// In memory, deliberately. A pending login is worthless after ten
// minutes and meaningless to another process, and persisting the PKCE
// verifier would put a short-lived secret on disk for no gain. The cost
// is that a service restart mid-login makes the operator start again,
// which takes them one click.
type pendingAuth struct {
	mu    sync.Mutex
	byKey map[string]*oidc.AuthRequest
	now   func() time.Time
}

func newPendingAuth(now func() time.Time) *pendingAuth {
	return &pendingAuth{byKey: map[string]*oidc.AuthRequest{}, now: now}
}

func (p *pendingAuth) put(req *oidc.AuthRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep()
	p.byKey[req.State] = req
}

// take returns a pending login and removes it, so a state is good once.
func (p *pendingAuth) take(state string) *oidc.AuthRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweep()
	req, ok := p.byKey[state]
	if !ok {
		return nil
	}
	delete(p.byKey, state)
	return req
}

// sweep drops what has expired. The caller holds the lock.
func (p *pendingAuth) sweep() {
	cutoff := p.now().Add(-AuthStateTTL)
	for key, req := range p.byKey {
		if req.Created.Before(cutoff) {
			delete(p.byKey, key)
		}
	}
}

func (p *pendingAuth) size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.byKey)
}

// AuthStartResponse tells a client where to send the operator.
type AuthStartResponse struct {
	// URL is the provider's authorization endpoint, fully formed.
	URL string `json:"url"`
	// State is what the callback must carry back.
	State string `json:"state"`
	// Expires is when this login stops being answerable.
	Expires string `json:"expires"`
}

// oidcStart is `POST /v1/login/oidc`: begin an interactive login.
//
// Unauthenticated, like `/v1/login` — it is how somebody with no token
// gets one. It discloses no estate state: the answer is the provider's
// own URL and a random handle.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeError(w, http.StatusNotFound, "this service is not configured with an OIDC provider")
		return
	}
	req, err := s.OIDC.StartAuth(r.Context())
	if err != nil {
		// The provider's words go to the log; the caller is told the
		// login could not start. A discovery failure names internal
		// endpoints, and this endpoint has no token behind it.
		s.warn("an OIDC login could not be started", "error", err.Error())
		writeError(w, http.StatusBadGateway, "the identity provider could not be reached")
		return
	}
	s.pending().put(req)
	s.info("oidc login started", "state", req.State, "remote", remoteHost(r))
	writeJSON(w, http.StatusOK, AuthStartResponse{
		URL:     req.URL,
		State:   req.State,
		Expires: s.now().Add(AuthStateTTL).UTC().Format(time.RFC3339),
	})
}

// OIDCCallbackRequest is what the client posts after the provider sends
// the operator back.
type OIDCCallbackRequest struct {
	Code  string `json:"code"`
	State string `json:"state"`
	// Error is what the provider said when it refused, passed through
	// so the operator sees it rather than a generic failure.
	Error       string `json:"error,omitempty"`
	Description string `json:"error_description,omitempty"`
}

// oidcCallback is `POST /v1/login/oidc/callback`: finish one.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeError(w, http.StatusNotFound, "this service is not configured with an OIDC provider")
		return
	}
	var req OIDCCallbackRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Error != "" {
		s.m().authAttempts.With("oidc", "refused").Inc()
		s.warn("the provider refused a login",
			"error", req.Error, "description", req.Description, "remote", remoteHost(r))
		writeError(w, http.StatusUnauthorized, "the identity provider refused this login")
		return
	}
	// Taken, not read: a state is good once, so an authorization
	// response replayed a second time finds nothing waiting.
	pending := s.pending().take(req.State)
	if pending == nil || req.Code == "" {
		s.m().authAttempts.With("oidc", "refused").Inc()
		s.warn("an OIDC callback matched no login in progress", "remote", remoteHost(r))
		writeError(w, http.StatusUnauthorized, "those credentials were not accepted")
		return
	}

	identity, err := s.OIDC.Exchange(r.Context(), req.Code, pending)
	if err != nil {
		s.m().authAttempts.With("oidc", "refused").Inc()
		s.warn("an OIDC login failed", "remote", remoteHost(r), "error", err.Error())
		writeError(w, http.StatusUnauthorized, "those credentials were not accepted")
		return
	}
	s.issueForIdentity(w, r, identity)
}

// oidcToken is `POST /v1/login/oidc/token`: a caller that already holds
// a token from the provider presents it directly.
//
// This is how a CI job authenticates. It has no browser, so there is no
// authorization code and no nonce; the assertion is the token itself,
// verified against the same issuer, audience, and key set.
func (s *Server) oidcToken(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeError(w, http.StatusNotFound, "this service is not configured with an OIDC provider")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Token == "" {
		writeError(w, http.StatusBadRequest, "this endpoint takes a token from the identity provider")
		return
	}
	identity, err := s.OIDC.VerifyToken(r.Context(), req.Token)
	if err != nil {
		s.m().authAttempts.With("oidc", "refused").Inc()
		s.warn("an OIDC token was refused", "remote", remoteHost(r), "error", err.Error())
		writeError(w, http.StatusUnauthorized, "that token was not accepted")
		return
	}
	s.issueForIdentity(w, r, identity)
}

// issueForIdentity turns a verified provider identity into a token of
// this service's own.
//
// The roles are the ones the mapping produced, frozen at issue like
// every other token's. An operator whose groups map to nothing is told
// which groups they had, because "you are in these groups and none of
// them is mapped" is actionable where "access denied" is not.
func (s *Server) issueForIdentity(w http.ResponseWriter, r *http.Request, identity *oidc.Identity) {
	if len(identity.Roles) == 0 {
		s.m().authAttempts.With("oidc", "unmapped").Inc()
		s.warn("an operator authenticated and is bound to no role",
			"principal", identity.Principal,
			"groups", strings.Join(identity.Groups, ","),
			"remote", remoteHost(r))
		writeError(w, http.StatusForbidden,
			"you authenticated, and none of your groups ("+
				oidc.DescribeGroups(identity.Groups)+
				") is mapped to a role in this estate")
		return
	}

	lifetime := s.TokenLifetime
	// A session never outlives the assertion it was made on: a provider
	// that expires a token in ten minutes has said something about how
	// long it trusts the operator, and this service does not extend it.
	if !identity.Expiry.IsZero() {
		if until := identity.Expiry.Sub(s.now()); until > 0 && until < lifetime {
			lifetime = until
		}
	}

	token, secret, err := s.Tokens.Issue(apitoken.Options{
		Principal: identity.Principal,
		Roles:     identity.Roles,
		Lifetime:  lifetime,
		IdleFor:   s.TokenIdle,
	})
	if err != nil {
		s.warn("could not issue a token", "principal", identity.Principal, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "the token could not be issued")
		return
	}
	s.m().authAttempts.With("oidc", "accepted").Inc()
	s.m().tokensIssued.With("oidc").Inc()
	namePrincipal(w, token.Principal)
	s.info("login", "principal", token.Principal, "token_id", token.ID,
		"method", "oidc", "remote", remoteHost(r),
		"groups", strings.Join(identity.Groups, ","),
		"roles", strings.Join(token.Roles, ","))

	writeJSON(w, http.StatusOK, LoginResponse{
		Token:     secret,
		TokenID:   token.ID,
		Principal: token.Principal,
		Roles:     token.Roles,
		Expires:   token.Expires.UTC().Format(time.RFC3339),
	})
}

func (s *Server) pending() *pendingAuth {
	s.pendingOnce.Do(func() { s.pendingAuths = newPendingAuth(s.now) })
	return s.pendingAuths
}
