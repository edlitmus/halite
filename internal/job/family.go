package job

import "strings"

// Family is the module family a function belongs to: the part of its
// name before the first dot, so `pkg.install` is `pkg`. It is what a
// node's family lock is keyed on (SPEC 9.6).
func Family(fun string) string {
	family, _, _ := strings.Cut(fun, ".")
	return family
}

// AlwaysAlone reports whether a job running fun runs alone on a node
// whatever it asks for: a state run, which reaches every family at once
// (SPEC 9.6).
//
// It is here rather than inside the node because the hub needs the same
// answer: an orchestration step or a reaction that asks for a state run
// to share the node is refused where it is written, and that refusal is
// only true while it is the rule the node applies. Two copies of "is
// this a state run" would be two answers free to drift apart.
func AlwaysAlone(fun string) bool {
	return Family(fun) == "state"
}
