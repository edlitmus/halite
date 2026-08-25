package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/ldap"
)

// ldapLogin authenticates against the estate's directory.
//
// It reaches this from `/v1/login` with `eauth: ldap`, because unlike
// OIDC it *is* a username and a password: the same request shape Salt's
// clients already send, which is the whole reason `eauth` exists.
//
// The directory decides. This service binds as the operator's own DN
// with the password they typed, and a directory that answers anything
// but success is a login that failed — this code never compares a
// password itself.
func (s *Server) ldapLogin(w http.ResponseWriter, r *http.Request, req LoginRequest) {
	identity, err := s.LDAP.Authenticate(req.Username, req.Password)
	if err != nil {
		s.m().authAttempts.With("ldap", "refused").Inc()
		// One message for every failure, as the local path gives. Which
		// of them it was goes to the log: the difference between "no
		// such user" and "wrong password" is the difference between a
		// guess and a confirmed name, and the difference between either
		// and "the directory is down" is the estate's problem rather
		// than the operator's.
		s.warn("ldap login refused",
			"username", req.Username, "remote", remoteHost(r),
			"reason", ldap.Classify(err), "error", err.Error())
		writeError(w, http.StatusUnauthorized, "those credentials were not accepted")
		return
	}

	if len(identity.Roles) == 0 {
		s.m().authAttempts.With("ldap", "unmapped").Inc()
		s.warn("an operator authenticated and is bound to no role",
			"principal", identity.Principal,
			"groups", strings.Join(identity.Groups, ","),
			"remote", remoteHost(r))
		writeError(w, http.StatusForbidden,
			"you authenticated, and none of your groups ("+
				describeGroups(identity.Groups)+
				") is mapped to a role in this estate")
		return
	}

	token, secret, err := s.Tokens.Issue(apitoken.Options{
		Principal: identity.Principal,
		Roles:     identity.Roles,
		Lifetime:  s.TokenLifetime,
		IdleFor:   s.TokenIdle,
	})
	if err != nil {
		s.warn("could not issue a token", "principal", identity.Principal, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "the token could not be issued")
		return
	}
	s.m().authAttempts.With("ldap", "accepted").Inc()
	namePrincipal(w, token.Principal)
	s.info("login", "principal", token.Principal, "token_id", token.ID,
		"method", "ldap", "dn", identity.DN, "remote", remoteHost(r),
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

// describeGroups bounds a group list for a message, so a directory with
// a hundred groups does not produce a hundred-line answer.
func describeGroups(groups []string) string {
	const most = 10
	if len(groups) == 0 {
		return "no groups"
	}
	if len(groups) <= most {
		return strings.Join(groups, ", ")
	}
	return strings.Join(groups[:most], ", ") + " and more"
}
