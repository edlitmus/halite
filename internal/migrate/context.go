package migrate

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// reactionKinds are the four prefixes a reactor SLS declares a reaction
// under. SPEC section 18.
var reactionKinds = map[string]string{
	"local":  "an execution function on the matched nodes",
	"runner": "a runner on the hub",
	"wheel":  "a wheel function on the hub",
	"caller": "an execution function on the node that raised the event",
}

// classifyNonState decides whether a declaration that is not a node-side
// state is an orchestration step or a reaction, and reports it as such.
//
// A tree carries three kinds of SLS that are syntactically identical: a
// state file applied on a node, an orchestration run by `orch`, and a
// reactor file named in the hub's `reactor` mapping. Nothing in the file
// says which it is — Salt does not mark them either, and a directory
// called `orch/` is a convention rather than a rule — so a declaration
// that resolves in one of the other two registries cannot be called a
// gap. It is reported with the context it needs instead, and left to the
// reader, who knows which kind of file they are looking at.
//
// Reported as a warning rather than blocking: in the file it belongs to
// it is correct and needs no work, and in a node state file it is an
// error the compiler will raise anyway.
func classifyNonState(rep *Report, opts Options, rel string, id, name string, pos value.Pos) bool {
	if sig, ok := lookup(opts.OrchRegistry, name); ok {
		rep.Findings = append(rep.Findings, Finding{
			Category: CatState, Severity: Review, File: rel, Line: pos.Line, Col: pos.Col,
			Subject: name,
			Msg: fmt.Sprintf("%s is an orchestration step, not a node state (SPEC section %s). "+
				"This build ships it", name, sectionOf(sig)),
			Action: "Correct in an orchestration SLS run by `halite-hub orch`. " +
				"In a state file applied on a node it is an error: nothing there can " +
				"dispatch a job to other nodes.",
		})
		return true
	}

	kind, rest, found := strings.Cut(name, ".")
	if !found {
		return false
	}
	what, isReaction := reactionKinds[kind]
	if !isReaction || rest == "" {
		return false
	}

	// The reaction form is right; whether the thing it names exists is a
	// separate question, and one worth answering while it is in hand.
	var missing string
	switch kind {
	case "local", "caller":
		if _, ok := lookup(opts.Registry, rest); !ok && opts.Registry != nil {
			missing = "an execution function"
		}
	case "runner":
		if _, ok := lookup(opts.RunnerRegistry, rest); !ok && opts.RunnerRegistry != nil {
			missing = "a runner"
		}
	}

	if missing != "" {
		rep.Findings = append(rep.Findings, Finding{
			Category: CatState, Severity: Blocking, File: rel, Line: pos.Line, Col: pos.Col,
			Subject: name,
			Msg: fmt.Sprintf("%s is a reaction calling %s, and %s is not %s this build ships",
				name, what, rest, missing),
			Action: "Provide it as a bridged extension, or call something this build has. " +
				"SPEC section 18.",
		})
		return true
	}

	rep.Findings = append(rep.Findings, Finding{
		Category: CatState, Severity: Review, File: rel, Line: pos.Line, Col: pos.Col,
		Subject: name,
		Msg: fmt.Sprintf("%s is a reaction calling %s, not a node state. "+
			"This build ships %s", name, what, rest),
		Action: "Correct in a reactor SLS named in the hub's `reactor` mapping. " +
			"In a state file applied on a node it is an error.",
	})
	return true
}

// lookup is Registry.Lookup with a nil registry answering "not found"
// rather than panicking, because every registry here is optional.
func lookup(r *signature.Registry, name string) (signature.Signature, bool) {
	if r == nil {
		return signature.Signature{}, false
	}
	return r.Lookup(name)
}

// sectionOf answers with the SPEC section a step cites, or 19.1, which
// is where orchestration lives when a step does not say.
func sectionOf(s signature.Signature) string {
	if s.Section == "" {
		return "19.1"
	}
	return s.Section
}
