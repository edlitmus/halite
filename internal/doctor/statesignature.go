package doctor

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// StateSignatureState is what a node knows about signed state content
// (issue 764, SPEC 25.8) without fetching anything.
type StateSignatureState struct {
	// Mode is `require_state_signature`: false, warn or true.
	Mode string
	// Keys is how many `state_signer_keys` parsed.
	Keys int
	// Local is `state_signature_local`.
	Local bool
	// MaxAge is `state_manifest_max_age`; zero is no bound.
	MaxAge time.Duration
	// Revisions are the accepted high-water marks.
	Revisions []StateRevision
	// RecordErr is a failure to read them.
	RecordErr error
	Now       time.Time
}

// StateRevision is one environment's accepted manifest.
type StateRevision struct {
	Env    string
	Issued time.Time
	Signer string
	ID     string
}

const pillarNote = "Pillar is not covered: the hub compiles it."

// StateSignatures reports a node's requirement, its keys, and the revision
// it accepted for each environment, with that revision's age against the
// freeze bound. It reads the node's records and fetches nothing, so it
// says what the next state run starts from rather than what the hub
// serves now.
func StateSignatures(s StateSignatureState) Check {
	return Check{
		Name:  "state signatures",
		Roles: []string{RoleNode},
		Run: func(context.Context) Result {
			res := Result{Name: "state signatures"}
			if s.Mode == "" || s.Mode == "false" {
				res.Status = Skip
				res.Detail = "require_state_signature is false; this node trusts the hub's digest for each file"
				res.Remedy = "Set require_state_signature: warn, then true, with state_signer_keys, " +
					"to refuse a tree a compromised hub altered (SPEC 25.8). " + pillarNote
				return res
			}
			if s.RecordErr != nil {
				res.Status = Fail
				res.Detail = "the accepted revisions cannot be read: " + firstLine(s.RecordErr.Error())
				res.Remedy = "They are under <state_dir>/tree-revisions; a node that cannot read its " +
					"high-water marks refuses every tree."
				return res
			}
			if s.Keys == 0 {
				status := Fail
				if s.Mode == "warn" {
					status = Warn
				}
				res.Status = status
				res.Detail = fmt.Sprintf("require_state_signature is %s and state_signer_keys is empty", s.Mode)
				res.Remedy = "Every state run is refused (or, under warn, counted as one true would " +
					"refuse). Add the signer's key: the line halite-hub keys signer create prints."
				return res
			}
			scope := "the hub's tree"
			if s.Local {
				scope += " and this node's own roots"
			}
			var parts, stale []string
			for _, r := range s.Revisions {
				age := s.Now.Sub(r.Issued).Round(time.Minute)
				parts = append(parts, fmt.Sprintf("%s issued %s (%s ago) by %s",
					r.Env, r.Issued.UTC().Format(time.RFC3339), age, r.Signer))
				if s.MaxAge > 0 && age > s.MaxAge {
					stale = append(stale, r.Env)
				}
			}
			bound := "no age bound"
			if s.MaxAge > 0 {
				bound = "at most " + s.MaxAge.String() + " old"
			}
			res.Detail = fmt.Sprintf("require_state_signature: %s for %s, %d key(s), %s", s.Mode, scope, s.Keys, bound)
			if len(parts) > 0 {
				res.Detail += "; accepted " + strings.Join(parts, "; ")
			} else {
				res.Detail += "; no revision accepted yet"
			}
			switch {
			case len(stale) > 0:
				res.Status = Fail
				res.Remedy = fmt.Sprintf("The accepted revision of %s is older than state_manifest_max_age, so "+
					"this node refuses it: sign the tree again and let the node fetch it. If the hub is "+
					"down, that is the freeze bound working; state runs resume when a fresh manifest "+
					"arrives. %s", strings.Join(stale, ", "), pillarNote)
			case s.Mode == "warn":
				res.Status = Warn
				res.Remedy = "Nothing is refused yet. Watch halite_node_tree_refusals_total over real " +
					"highstates, then set require_state_signature: true. " + pillarNote
			default:
				res.Status = Pass
				res.Detail += ". " + pillarNote
			}
			return res
		},
	}
}

// ServedTreeSignatures is the hub's half: the environments it refuses to
// serve because their `_halite/` holds something else, and whether its
// hide rules hide the manifest from every node.
func ServedTreeSignatures(envs int, reserved []string, hidden bool) Check {
	return Check{
		Name:  "state signatures",
		Roles: []string{RoleHub},
		Run: func(context.Context) Result {
			res := Result{Name: "state signatures"}
			switch {
			case envs == 0:
				res.Status = Skip
				res.Detail = "this hub serves no files"
				res.Remedy = "Set file_roots or gitfs_remotes for a tree to sign."
			case len(reserved) > 0:
				res.Status = Fail
				res.Detail = strings.Join(reserved, "; ")
				res.Remedy = "The hub refuses every request for these environments, from every node. " +
					"_halite/ holds manifest.json and manifest.sig and nothing else: move the rest out."
			case hidden:
				res.Status = Fail
				res.Detail = "file_ignore_glob or file_ignore_regex hides _halite/manifest.json or its signature"
				res.Remedy = "Every node with require_state_signature refuses this hub's tree as unsigned. " +
					"Exempt _halite/ from the hide rules."
			default:
				res.Status = Pass
				res.Detail = fmt.Sprintf("%d environment(s) served; no _halite/ holds anything but a manifest "+
					"and its signature. Whether they are signed is for halite-hub tree verify.", envs)
			}
			return res
		},
	}
}
