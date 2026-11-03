package state

import "strings"

// jobFunctions are the `state.*` functions a node answers through the
// compiler rather than through its execution registry, which none of
// them is in.
//
// They are listed here, and not in the node, because the hub's policy
// has to know them too. A wildcard on the hub grants only what the
// build ships, since anything else may be an extension's and run
// anything (DIVERGENCE 5.558), and "what the build ships" is the
// execution registry plus these: a policy that read the registry alone
// stopped `state.*` granting `state.apply`. One list, read by both, is
// what keeps the node's dispatch and the hub's catalog the same set.
var jobFunctions = []string{
	"apply", "highstate", "sls",
	"show_top", "show_highstate", "show_sls", "show_lowstate", "show_states",
}

// JobFunction reports whether fun is one of them, and its name without
// the `state.` prefix.
func JobFunction(fun string) (string, bool) {
	name, ok := strings.CutPrefix(fun, "state.")
	if !ok {
		return "", false
	}
	for _, f := range jobFunctions {
		if f == name {
			return name, true
		}
	}
	return "", false
}

// JobFunctions lists them by their full names.
func JobFunctions() []string {
	out := make([]string, len(jobFunctions))
	for i, f := range jobFunctions {
		out[i] = "state." + f
	}
	return out
}
