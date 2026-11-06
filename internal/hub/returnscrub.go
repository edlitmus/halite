package hub

import (
	"encoding/json"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/value"
)

// secrets is the hub's redactor's set: the one its pillar compiles teach.
//
// Read from the pillar options rather than from a field of its own, so
// that the set the hub scrubs returns with cannot be a different set from
// the one OnSecret fills. A second field would be one more seam that has
// to be wired by hand, and 5.110 is what happens to a seam like that: it
// is declared, never assigned, and nothing fails. A hub with no pillar
// options compiles no pillar and decrypts nothing, so nil -- an empty set
// -- is the true answer there and not a gap.
func (s *Server) secrets() *redact.Set {
	if s.Pillar == nil {
		return nil
	}
	return s.Pillar.Secrets
}

// scrubReturn masks the registered secrets in a return before the hub
// keeps it. DIVERGENCE 5.598, and the subsection under it for issue 659.
//
// It is called on the way in, before Jobs.AddReturn, and that placement
// is the whole design. Every other place a return goes starts from the
// copy the hub filed: the `ret` event is built from it, a relay forwards
// it upstream (OnReturn), the event returner ships the bus, and the job
// routes -- `GET /v1/jobs/{jid}`, the `jobs.*` runners, and the API that
// forwards to them -- read the cache. Scrubbing at each of those instead
// would be a call site per route, and the next route somebody adds would
// not have one. Scrubbing once at the door means there is no unscrubbed
// copy on the hub for a new route to find.
//
// **The caller still gets the output.** 5.268's point stands: the return
// of an execution function is data somebody asked for, and it is not
// replaced. What changes is that the registered secrets inside it are
// masked as `pillar items` and `state apply` already mask them on the
// node. A secret in a cached return outlives the job, reaches anyone
// allowed to read the job later, and is copied into returners and
// exports, which is why the owner reversed 5.598's decision.
//
// **Why the hub's own set is enough, and where it is not.** A node asks
// the hub to compile its pillar for every job it runs, state or not
// (`runFunction` and `runStateJob` in cmd/halite-node/execute.go both
// call `compilePillarOrErr`), and the hub's compile hands every value it
// decrypts to this set through OnSecret before it answers. The node's
// own set is seeded from that same answer (`seedPillarSecrets`, from
// `PillarResponse.Secrets`). So for a node whose pillar the hub compiles,
// the values the node knows are secret are, by the time its return
// arrives, values the hub has just been taught. Sending the node's set
// back with each return would teach the hub nothing new about those, and
// would put every one of them on the wire again with every return.
//
// The rest is what this does not cover, and it is named rather than
// assumed away:
//
//   - A node's own configured secrets (`seedConfiguredSecrets`: the
//     settings whose names say they hold one). They never reach the hub
//     and should not: shipping a node's credentials to the hub so the
//     hub can recognise them widens where they live, which is the thing
//     this exists to prevent.
//   - A node that compiles its own pillar because the hub compiles none.
//     The hub decrypted nothing, so it knows nothing.
//   - A return that arrives after the hub restarted, from a job that ran
//     on a last-known-good pillar while the hub was unreachable. The
//     hub's set is in memory and is refilled by compiles, not by restart.
//
// For the first two the node would have to mask its own return; issue
// 659 chose the hub, and those cases are listed as not done in 5.598's
// subsection. The set is also the hub's whole set, not this node's, so a
// value decrypted for another node is masked here too. That masks too
// much, never too little, and a hub cannot tell which node a value
// "belongs" to once two nodes share it.
//
// The return is left byte for byte as it arrived when nothing in it is a
// registered value, including one a node had already masked. A re-encode
// through the value model is not neutral, measured: `1.0` becomes `1`,
// `-0` becomes `0`, and an integer wider than 64 bits becomes
// `1.2345678901234568e+29`. A return that did hold a secret carries those
// changes with its mask.
func (s *Server) scrubReturn(ret *job.Return) {
	set := s.secrets()
	if set == nil {
		return
	}
	for i, a := range ret.FunArgs {
		if masked, changed := set.MaskValue(a); changed {
			ret.FunArgs[i] = masked.(string)
		}
	}
	if len(ret.Return) == 0 {
		return
	}
	decoded, err := value.DecodeJSON(ret.Return)
	if err != nil {
		s.maskUnreadable(ret, err)
		return
	}
	masked, changed := set.MaskValue(decoded)
	if !changed {
		return
	}
	encoded, err := value.EncodeJSON(masked, 0)
	if err != nil {
		s.maskUnreadable(ret, err)
		return
	}
	ret.Return = encoded
}

// maskUnreadable is the fallback for a return the value model will not
// read or write back, and it fails closed.
//
// ReadJSON has already accepted the body, so this is JSON the value
// model's codec is stricter about than encoding/json, and no node of this
// build sends it. The obvious fallback -- keep it as it came -- is the one
// that is wrong here: it puts the secret this function exists to remove
// into the cache because the return was strange, and a strange return is
// no less likely to hold one. Masking the raw text in place is wrong too,
// because a value that contains a quote can match across a JSON boundary
// and leave text that is not JSON, which the cache then refuses to write
// and the node's evidence is lost.
//
// So the masked text is kept as one JSON string. The shape is lost and
// the warning says so; the words are kept, and the secret is not.
func (s *Server) maskUnreadable(ret *job.Return, cause error) {
	masked, changed := s.secrets().MaskValue(string(ret.Return))
	if !changed {
		return
	}
	encoded, err := json.Marshal(masked)
	if err != nil {
		// json.Marshal of a string does not fail; this is here so that
		// a change to that is not a silent unmasked write.
		encoded = []byte(`"` + redact.Placeholder + `"`)
	}
	s.warn("a return held a registered secret and could not be read as a value; it is kept as masked text",
		"jid", string(ret.JID), "node_id", ret.NodeID, "error", cause.Error())
	ret.Return = encoded
}
