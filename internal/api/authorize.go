package api

import (
	"fmt"
	"net/http"

	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/policy"
)

// authorize decides whether the operator behind a token may ask for
// something.
//
// This is the first of two checks, not the only one. The API then
// forwards to the hub under its own certificate, and the hub authorizes
// that certificate against the same policy file. Both have to pass:
//
//   - The operator's check is what stops an authenticated user doing
//     more than their roles allow. Without it, logging in would hand
//     out the API's whole authority.
//   - The hub's check is what bounds the API itself. SPEC 5.2 makes
//     the API a client precisely so that compromising it yields one
//     certificate with one grant rather than the control plane.
//
// An estate that grants the API less than the sum of its operators gets
// exactly that, which is a real control and not an accident.
func (s *Server) authorize(w http.ResponseWriter, token *apitoken.Token, req policy.Request) bool {
	req.Principal = token.Principal
	// The roles frozen into the token, not the roles the principal has
	// now: a role taken away is a reason to revoke, and a role added
	// should not widen a token already in someone's hands. SPEC 23.6.
	decision := s.Policy.AuthorizeAs(req, token.Roles)
	if decision.Allowed {
		s.info("authorized",
			"principal", token.Principal, "fun", req.Fun, "target", req.Target,
			"role", decision.Role, "rule", decision.RuleIndex)
		return true
	}
	s.warn("refused by policy",
		"principal", token.Principal, "fun", req.Fun, "target", req.Target,
		"reason", decision.Reason)
	writeError(w, http.StatusForbidden, decision.Reason)
	return false
}

// requestFor builds one authorization question.
func requestFor(fun, target string, arg []string, kwarg map[string]any, runner bool) policy.Request {
	return policy.Request{
		Target: target,
		Fun:    fun,
		Arg:    arg,
		Kwarg:  kwarg,
		Runner: runner,
	}
}

// errUnknownClient names the client types SPEC 22.1 preserves from
// Salt's netapi, and which of them this build serves.
func errUnknownClient(client string) error {
	return fmt.Errorf(
		"%q is not a client type this build serves; there are local, local_async, "+
			"local_batch, runner, runner_async, and wheel. `ssh` arrives with agentless "+
			"mode in phase 5", client)
}
