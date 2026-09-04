package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/account"
	"github.com/edlitmus/halite/internal/apitoken"
)

// LoginRequest is what `/v1/login` takes.
//
// `eauth` is Salt's name for which authentication backend to use, and
// an existing client sends it. This build has one backend, and a
// request naming another is refused rather than quietly authenticated
// against the one that is there.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Code is the TOTP second factor, for an account that has one.
	Code  string `json:"code,omitempty"`
	Eauth string `json:"eauth,omitempty"`
}

// LoginResponse is the token and what it can do.
//
// The token is here and nowhere else: it is not written to the log, not
// put on the event bus, and not returned again by any other endpoint.
// SPEC 23.6.
type LoginResponse struct {
	Token     string   `json:"token"`
	TokenID   string   `json:"token_id"`
	Principal string   `json:"principal"`
	Roles     []string `json:"roles,omitempty"`
	Expires   string   `json:"expires"`
}

// login exchanges credentials for a token.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := readJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch req.Eauth {
	case "", "local":
	case "ldap":
		if s.LDAP == nil {
			writeError(w, http.StatusBadRequest, "this service is not configured with a directory")
			return
		}
		s.ldapLogin(w, r, req)
		return
	case "oidc":
		// Named rather than silently handled here: the OIDC flow is not
		// a username and a password, and answering this request with a
		// token would mean this endpoint had authenticated somebody it
		// never verified.
		writeError(w, http.StatusBadRequest,
			"OIDC does not use this endpoint; POST /v1/login/oidc to start one, "+
				"or /v1/login/oidc/token with a token you already hold")
		return
	default:
		writeError(w, http.StatusBadRequest,
			"this build authenticates against `local`, `ldap`, and `oidc`")
		return
	}

	acct, _ := s.Accounts.Lookup(req.Username)
	// Verified even when the account does not exist, so that the answer
	// takes the same time either way: a login that is faster for an
	// unknown name enumerates accounts.
	ok := acct.Verify(req.Password)
	if ok && acct.Disabled {
		ok = false
	}
	if ok && acct.NeedsSecondFactor() && !acct.VerifyTOTP(req.Code, s.now()) {
		ok = false
	}
	if !ok {
		s.m().authAttempts.With("local", "refused").Inc()
		// One message for every failure. Which of the three it was is
		// in the log and not in the answer, because the difference
		// between "no such account" and "wrong password" is the
		// difference between a guess and a confirmed name.
		s.warn("login refused",
			"username", req.Username, "remote", remoteHost(r),
			"exists", acct != nil)
		writeError(w, http.StatusUnauthorized, "those credentials were not accepted")
		return
	}

	token, secret, err := s.Tokens.Issue(apitoken.Options{
		Principal: acct.Principal(),
		Roles:     s.rolesFor(acct),
		Lifetime:  s.TokenLifetime,
		IdleFor:   s.TokenIdle,
	})
	if err != nil {
		s.warn("could not issue a token", "principal", acct.Principal(), "error", err.Error())
		writeError(w, http.StatusInternalServerError, "the token could not be issued")
		return
	}
	s.m().authAttempts.With("local", "accepted").Inc()
	s.m().tokensIssued.With("local").Inc()
	namePrincipal(w, token.Principal)
	s.info("login", "principal", token.Principal, "token_id", token.ID,
		"remote", remoteHost(r), "roles", strings.Join(token.Roles, ","))

	writeJSON(w, http.StatusOK, LoginResponse{
		Token:     secret,
		TokenID:   token.ID,
		Principal: token.Principal,
		Roles:     token.Roles,
		Expires:   token.Expires.UTC().Format(time.RFC3339),
	})
}

// rolesFor is the role set frozen into a token at issue.
//
// The account's own list and whatever the policy binds to the
// principal, together: an estate may put the binding in either place,
// and a token that carried only one of them would be narrower than the
// policy says without anyone being told.
func (s *Server) rolesFor(acct *account.Account) []string {
	seen := map[string]bool{}
	var out []string
	add := func(roles []string) {
		for _, role := range roles {
			if role == "" || seen[role] {
				continue
			}
			seen[role] = true
			out = append(out, role)
		}
	}
	add(acct.Roles)
	if s.Policy != nil {
		add(s.Policy.RolesFor(acct.Principal()))
	}
	return out
}

// logout revokes the token that was presented.
func (s *Server) logout(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	if _, err := s.Tokens.Revoke(token.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "the token could not be revoked")
		return
	}
	s.m().tokensRevoked.Inc()
	s.info("logout", "principal", token.Principal, "token_id", token.ID)
	w.WriteHeader(http.StatusNoContent)
}

// TokenInfo is what `/v1/token` reports.
type TokenInfo struct {
	TokenID   string   `json:"token_id"`
	Principal string   `json:"principal"`
	Roles     []string `json:"roles,omitempty"`
	Issued    string   `json:"issued"`
	Expires   string   `json:"expires"`
	// IdleExpires is when the token stops if it is not used again,
	// which is the part an operator cannot work out for themselves.
	IdleExpires string `json:"idle_expires,omitempty"`
	SourceCIDR  string `json:"source_cidr,omitempty"`
	Version     string `json:"version"`
}

// introspect describes the presented token.
//
// Its own token and no other: a caller asking about someone else's
// would be asking the service to confirm a guess.
func (s *Server) introspect(w http.ResponseWriter, r *http.Request, token *apitoken.Token) {
	info := TokenInfo{
		TokenID:   token.ID,
		Principal: token.Principal,
		Roles:     token.Roles,
		Issued:    token.Issued.UTC().Format(time.RFC3339),
		Expires:   token.Expires.UTC().Format(time.RFC3339),
		Version:   APIVersion(),
	}
	if token.IdleFor > 0 {
		info.IdleExpires = token.LastUsed.Add(token.IdleFor).UTC().Format(time.RFC3339)
	}
	if token.SourceCIDR != "" {
		info.SourceCIDR = token.SourceCIDR
	}
	writeJSON(w, http.StatusOK, info)
}

// readJSON decodes a request body, refusing anything after the first
// value so that a request cannot smuggle a second one.
func readJSON(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return errRequestBody{err}
	}
	if err := dec.Decode(new(json.RawMessage)); err == nil {
		return errRequestBody{errTrailing{}}
	}
	return nil
}

type errRequestBody struct{ err error }

func (e errRequestBody) Error() string { return "the request body: " + e.err.Error() }

type errTrailing struct{}

func (errTrailing) Error() string { return "there is more than one value in it" }
