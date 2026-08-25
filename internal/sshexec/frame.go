package sshexec

import "strings"

// The frames the pushed binary writes around its return. They match
// `halite-node oneshot`'s, and the constants are repeated here rather
// than imported because this package must not depend on a command.
const (
	begin = "--- halite-oneshot-begin ---"
	end   = "--- halite-oneshot-end ---"
)

// Unframe pulls the return out of a target's stdout.
//
// SPEC 21.1 asks for a framed delimiter and the reason is what else
// arrives on that stream: a login banner, a motd, a sudo lecture, a
// shell profile that echoes something, and — on a machine somebody has
// been fiddling with — a `.bashrc` that prints a joke. Without the
// frame the caller parses "welcome to production" as JSON and reports
// that the target answered nonsense.
//
// The *last* frame is taken, not the first: a banner that happens to
// contain the begin marker is a banner, and the return is what the
// binary wrote last.
func Unframe(stdout string) (string, bool) {
	start := strings.LastIndex(stdout, begin)
	if start < 0 {
		return "", false
	}
	rest := stdout[start+len(begin):]
	stop := strings.Index(rest, end)
	if stop < 0 {
		// A begin with no end is a run that was cut off part-way —
		// killed, or the connection dropped. Reporting the partial
		// text as a return would be reporting a truncated JSON object
		// as an answer.
		return "", false
	}
	return strings.TrimSpace(rest[:stop]), true
}
