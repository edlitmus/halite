package policy

import "sort"

// The functions that hand a node's compiled pillar to whoever asked.
//
// SPEC 22.1 makes reading one node's pillar a distinct high-privilege
// permission, because it is reading that node's secrets: a role written to
// let somebody restart a service must not carry it because its list said
// `*`. Like the arbitrary-code functions, a function here is granted only
// by a rule that names it.
//
// It is the function's property and not the caller's. It used to be a
// flag on Request that `GET /v1/pillar/{id}` set and nothing else did, so
// the same `pillar.show_pillar` reached through `POST /v1/run`, the hub's
// own runner endpoint, `halite-hub runner`, a reactor or an orchestration
// was granted by `runners: ['*']`; and `pillar.items` sent to the node,
// which returns the same secrets in clear (5.268), was granted by
// `functions: ['*']` on every route. A flag each caller must remember to
// set is a rule with as many chances to be forgotten as there are
// callers; a set consulted inside AuthorizeAs is asked by every one of
// them, including the next. DIVERGENCE 5.563.
//
// The list is written here rather than read from the signatures, as the
// arbitrary-code set is, because no signature says "returns pillar" and
// adding a field for six functions would be a declaration with as many
// ways to be missed as this list. What it does not cover is said in
// 5.563: an extension that returns pillar is not on it.
var (
	// pillarFunctions are execution functions: run on the node, they
	// return that node's pillar, or a value from it, to the caller.
	// `config.get` searches pillar first, so leaving it off would make
	// `config.get db:password` the way round `pillar.get db:password`.
	//
	// `pillar.keys` is not here: it returns the top-level names and no
	// value, which is the shape of a tree rather than its secrets.
	pillarFunctions = map[string]bool{
		"pillar.items": true,
		"pillar.raw":   true,
		"pillar.get":   true,
		"config.get":   true,
	}
	// pillarRunners run on the hub and compile, or will read from a
	// cache, the pillar of a node the caller names. `cache.pillar` is
	// declared and not built; it is listed so that the build which
	// builds it is covered without anybody remembering this file.
	//
	// `pillar.show_top` is not here: it returns the SLS names the top
	// file selects, not what they compile to.
	pillarRunners = map[string]bool{
		"pillar.show_pillar": true,
		"cache.pillar":       true,
	}
)

// ReadsPillar reports whether fun hands a node's pillar to the caller, and
// so is never granted by a wildcard. runner says which namespace fun is
// in: a runner and an execution function may share a name and not a
// meaning.
func ReadsPillar(fun string, runner bool) bool {
	if runner {
		return pillarRunners[fun]
	}
	return pillarFunctions[fun]
}

// PillarReaders lists ReadsPillar's set, sorted, for `policy show`.
func PillarReaders() (functions, runners []string) {
	for f := range pillarFunctions {
		functions = append(functions, f)
	}
	for r := range pillarRunners {
		runners = append(runners, r)
	}
	sort.Strings(functions)
	sort.Strings(runners)
	return functions, runners
}
