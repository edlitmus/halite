package hub

import (
	"fmt"

	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// A delegated call is one a caller makes for somebody else: halite-api,
// calling the hub with its own certificate for the operator who logged
// in to it (issue 618).
//
// Before this, the hub had only the certificate to judge, and judged it.
// Narrow held the runners that read nodes to the operator's targets
// (M-56), and nothing else was held to anything: a runner that sends jobs
// authorized them as the API, the jobs and orchestrations a caller may
// see were the ones whose owner was the API, and a kill was the API's
// grant. An operator narrowed to `web*` reached db1 through each of them.
//
// What changes is that the hub now asks two questions where it asked
// one: may the caller do this, and may the operator it acts for. Both
// must say yes. The second can only narrow, so a caller gains nothing by
// naming a powerful operator; but the operator's name also decides whose
// runs a caller sees, and is written into every record as who asked,
// so a caller may name one only with `act_for` in its own role.

// checkDelegation refuses a claim to act for an operator from a caller
// the policy does not let make it.
//
// Refused rather than ignored. Ignoring the field from a caller without
// the grant would leave that caller doing exactly what the API did
// before this change, which is the thing being fixed; an API whose role
// lacks `act_for` should find out at its first call, with the line to
// add, rather than keep working with the wrong scope.
func (s *Server) checkDelegation(principal string, ob *transport.Delegation) error {
	if ob == nil {
		return nil
	}
	if ob.Principal == "" {
		return fmt.Errorf("%s says it acts for somebody and names nobody; "+
			"a delegated call names the operator it is for", principal)
	}
	if !s.Policy.MayActFor(principal, ob.Principal) {
		return fmt.Errorf("%s says it acts for %s, and no role bound to it grants act_for that principal; "+
			"only a caller whose role has `act_for` may name whom it acts for (issue 618)",
			principal, ob.Principal)
	}
	return nil
}

// authorizeFor answers one policy question for a caller and, when it
// acts for an operator, for that operator as well, judged by the roles
// the operator's token was issued with. The first refusal is the answer.
//
// Every decision is counted, the operator's too: SPEC 23.5 logs and
// counts each, and a refusal of the operator is a refusal like another.
func (s *Server) authorizeFor(req policy.Request, ob *transport.Delegation) policy.Decision {
	decision := s.Policy.Authorize(req)
	s.countDecision(decision)
	if !decision.Allowed || ob == nil {
		return decision
	}
	theirs := req
	theirs.Principal = ob.Principal
	d := s.Policy.AuthorizeAs(theirs, ob.Roles)
	s.countDecision(d)
	if !d.Allowed {
		d.Reason = fmt.Sprintf("%s acts here for %s, who may not: %s", req.Principal, ob.Principal, d.Reason)
	}
	return d
}

// actingFor is the name of the operator a delegated call is for, and
// empty for a direct one. It is what a record's OnBehalfOf holds.
func actingFor(ob *transport.Delegation) string {
	if ob == nil {
		return ""
	}
	return ob.Principal
}

// sameCaller reports whether a record was made by the caller now asking:
// the same certificate, acting for the same operator or for nobody.
//
// Both halves, always. The certificate alone is what the hub compared
// before, and through the API that made every operator the owner of
// every other operator's runs. The operator alone would let any caller
// with `act_for` read as anyone it may name, which is its grant, but also
// let a direct operator read their API-made runs by a rule nobody wrote.
// A record's OnBehalfOf is only ever written from a delegation the hub
// accepted (recordRunnerJob, Orchestrate), so it can be compared;
// a job's from POST /v1/jobs is the submitter's say-so, and jobVisible
// never asks this of a fleet job.
func sameCaller(submitter, onBehalfOf string, principal string, ob *transport.Delegation) bool {
	return submitter == principal && onBehalfOf == actingFor(ob)
}
