package main

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/target"
	"github.com/edlitmus/halite/internal/value"
)

// signatureRequirement is what `require_job_signature` asks for.
//
// SPEC 25.6 does not make this a switch: "the recommended configuration
// is to require signatures for `arbitrary_code` functions and for state
// application while leaving read-only functions unsigned. This is
// expressible per function class." So the setting takes `true`, `false`,
// or a list of classes, and an estate that wants the recommendation
// writes the recommendation.
type signatureRequirement struct {
	// all requires a signature on every job, which is `true`.
	all bool
	// classes are the named ones, empty when nothing is required.
	classes map[string]bool
}

// The function classes this build can require a signature for.
//
// Each is a property a node can decide for itself from the function's own
// signature, which is what makes it enforceable here: `state` is a state
// function that applies rather than one that renders, `arbitrary_code` is
// the declaration SPEC 23.5 already uses to keep `cmd.run` out of a
// wildcard grant, and `mutating` is everything that changes the machine.
const (
	classArbitraryCode = "arbitrary_code"
	classState         = "state"
	classMutating      = "mutating"
)

var signatureClasses = []string{classArbitraryCode, classState, classMutating}

// parseSignatureRequirement reads the setting.
//
// A value this build does not understand is fatal rather than ignored. It
// is a security control: an operator who writes `require_job_signature:
// arbitrarycode` and gets a node that quietly requires nothing has the
// worst of both worlds, and would have no way to find out.
func parseSignatureRequirement(raw any, ok bool) (signatureRequirement, error) {
	req := signatureRequirement{classes: map[string]bool{}}
	if !ok || raw == nil {
		return req, nil
	}
	switch t := raw.(type) {
	case bool:
		req.all = t
		return req, nil
	case string:
		// A single class, or a boolean somebody quoted.
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true":
			req.all = true
			return req, nil
		case "false", "":
			return req, nil
		}
		return req, addClasses(req, []any{t})
	case []any:
		return req, addClasses(req, t)
	}
	return req, fmt.Errorf(
		"`require_job_signature` is true, false, or a list of %s, not %T",
		strings.Join(signatureClasses, ", "), raw)
}

func addClasses(req signatureRequirement, raw []any) error {
	for _, item := range raw {
		name, isString := item.(string)
		if !isString {
			return fmt.Errorf("`require_job_signature` lists %v, which is not a function class", item)
		}
		name = strings.ToLower(strings.TrimSpace(name))
		known := false
		for _, c := range signatureClasses {
			if name == c {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("`require_job_signature` names the function class %q; the classes are %s",
				name, strings.Join(signatureClasses, ", "))
		}
		req.classes[name] = true
	}
	return nil
}

// any reports whether anything at all needs a signature.
func (r signatureRequirement) any() bool { return r.all || len(r.classes) > 0 }

// String renders the requirement for a log line and for `doctor`.
func (r signatureRequirement) String() string {
	if r.all {
		return "every job"
	}
	if len(r.classes) == 0 {
		return "nothing"
	}
	var named []string
	for _, c := range signatureClasses {
		if r.classes[c] {
			named = append(named, c)
		}
	}
	return strings.Join(named, ", ")
}

// requires reports whether this function needs a signature, and which
// class made it so.
//
// A function this build does not ship requires one whenever anything
// does. It will fail in a moment with "no such function", and deciding
// that an unknown name is read-only is a decision about a function nobody
// has looked at — the safe direction is the one where a name that means
// nothing here cannot be the way past the control.
func (r signatureRequirement) requires(n *node, fun string) (bool, string) {
	if r.all {
		return true, "every job"
	}
	if len(r.classes) == 0 {
		return false, ""
	}
	if fn, isState := stateFunction(fun); isState {
		// The applying forms only. `state.show_highstate` renders and
		// changes nothing, and SPEC 25.6's recommendation is about
		// state *application*.
		if r.classes[classState] && applyingStateFunction(fn) {
			return true, classState
		}
		if r.classes[classMutating] && applyingStateFunction(fn) {
			return true, classMutating
		}
		return false, ""
	}
	sig, known := n.registry.Exec.Signatures().Lookup(fun)
	if !known {
		return true, "a function this build does not ship"
	}
	if r.classes[classArbitraryCode] && sig.ArbitraryCode {
		return true, classArbitraryCode
	}
	if r.classes[classMutating] && sig.Mutates {
		return true, classMutating
	}
	return false, ""
}

// applyingStateFunction reports whether a state function changes the
// machine, as opposed to rendering what it would do.
func applyingStateFunction(fn string) bool {
	switch fn {
	case "apply", "highstate", "sls":
		return true
	}
	return false
}

// signerKeys reads `job_signer_keys`.
//
// A key that will not parse is fatal, exactly as `extension_trust_keys`
// is and for the same reason: a node trusting fewer keys than the
// operator wrote is a node that refuses jobs it should run, and finding
// that out from a refused job is finding out too late.
func (n *node) signerKeys() []jobsign.SignerKey {
	var keys []jobsign.SignerKey
	for _, line := range n.cfg.StringSlice("job_signer_keys") {
		key, err := jobsign.ParseSignerKey(line)
		if err != nil {
			cli.Fatalf("job_signer_keys: %v", err)
		}
		keys = append(keys, key)
	}
	return keys
}

// checkJobSignature is SPEC 25.6's check, and it is the node's alone.
//
// Nothing the hub says can turn it off: the requirement is this node's
// configuration and the keys are this node's configuration, which is what
// makes it worth anything against a hub that has been taken over.
//
// Three things have to hold, and the order is the order an operator can
// act on. The function has to be one this node requires a signature for;
// the signature has to verify against a key this node trusts; and this
// node has to be one the signed target selects.
func (n *node) checkJobSignature(j *job.Job) (string, error) {
	required, why := n.signatureRequired.requires(n, j.Fun)
	if !required {
		return "", nil
	}
	if len(n.signerKeyList) == 0 {
		// A node asked to require signatures with no key to check them
		// against can run nothing at all. Said plainly, because the
		// symptom is every job refused.
		return "", fmt.Errorf(
			"this node requires a signature for %s (%s) and `job_signer_keys` is empty, "+
				"so it can accept nothing", j.Fun, why)
	}
	signer, err := jobsign.Verify(n.signerKeyList, job.SigningPayload(j), j.Signature)
	if err != nil {
		return "", fmt.Errorf("this node requires a signature for %s (%s): %w", j.Fun, why, err)
	}
	if err := n.matchesSignedTarget(j); err != nil {
		return "", err
	}
	// The key's name travels back to the caller rather than onto the job
	// record: it is what this node worked out, not something the hub
	// sent, and the evidence record says so by recording it separately.
	return signer, nil
}

// matchesSignedTarget checks that this node is one the signature
// authorised the job for.
//
// Without it the signature says what may be run and not where, so a hub
// that has been taken over can take a legitimate signed `state.apply` for
// one host and deliver it to every other: each one verifies the signature
// perfectly and applies a tree meant for somebody else. SPEC 25.6 does
// not ask for this check and the guarantee it names -- hub compromise
// must not equal fleet compromise -- does not survive without it.
//
// A target this node cannot evaluate about itself is refused rather than
// waved through. A nodegroup is the case: it is defined in the hub's
// configuration, so a node has no way to know whether it is in one, and
// "I could not check" must not mean "accepted" in the one control whose
// purpose is to distrust the hub. The refusal names the kind, so an
// operator who signs by nodegroup finds out at once rather than on the
// day it matters.
func (n *node) matchesSignedTarget(j *job.Job) error {
	if j.Target == "" {
		return fmt.Errorf(
			"this job's signature verifies and the job carries no target, so this node " +
				"cannot tell whether it was meant for it; the hub that sent it is older than " +
				"signed targeting")
	}
	kind, ok := target.KindFromFlag(j.TargetKind)
	if !ok {
		return fmt.Errorf("the signed target kind %q is not one this node knows", j.TargetKind)
	}
	// No nodegroups: this node has none and cannot have any, so an
	// expression naming one fails to compile here and is refused below
	// with the reason.
	matcher, err := target.Compile(kind, j.Target, nil)
	if err != nil {
		return fmt.Errorf(
			"this node cannot check that a signed job targeted at %q (%s) was meant for it: %w; "+
				"sign jobs with a target a node can evaluate about itself",
			j.Target, j.TargetKind, err)
	}
	if !matcher.Match(target.Node{ID: n.nodeID, Grains: n.grains, Pillar: n.signedTargetPillar()}) {
		return fmt.Errorf(
			"this job is signed for %q (%s) and this node is %s, which does not match it",
			j.Target, j.TargetKind, n.nodeID)
	}
	return nil
}

// signedTargetPillar is the pillar a pillar-matching target is checked
// against, or nil when it does not compile.
//
// Nil rather than an error: a pillar target on a node whose pillar is
// broken should refuse the job, and it does -- a nil pillar matches
// nothing -- rather than failing the whole check with a message about
// compilation that hides what was actually being decided.
func (n *node) signedTargetPillar() *value.Map {
	p, err := n.compilePillarOrErr()
	if err != nil {
		return nil
	}
	return p
}

// signatureSummary is what the agent logs at startup, so that an estate
// can see what a node will insist on without asking it to run something.
func (n *node) signatureSummary() string {
	if !n.signatureRequired.any() {
		return "no job signature is required"
	}
	return fmt.Sprintf("a signature is required for %s, from %d trusted key(s)",
		n.signatureRequired, len(n.signerKeyList))
}
