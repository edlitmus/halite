package state

import (
	"strings"

	"github.com/edlitmus/halite/internal/fnmatch"
	"github.com/edlitmus/halite/internal/value"
)

// ReqKind is one of Salt's requisites. All of them are supported, in both
// directions. SPEC section 11.3.
type ReqKind int

const (
	// Require means the target must have run and succeeded.
	Require ReqKind = iota
	// RequireAny means at least one target succeeded.
	RequireAny
	// Watch is require, plus mod_watch when the target reported changes.
	Watch
	// WatchAny is Watch against any target.
	WatchAny
	// OnChanges runs only if a target reported changes, and otherwise
	// skips with result true.
	OnChanges
	// OnChangesAny is OnChanges against any target.
	OnChangesAny
	// OnFail runs only if a target failed.
	OnFail
	// OnFailAny is OnFail against any target.
	OnFailAny
	// OnFailAll requires every target to have failed.
	OnFailAll
	// Prereq runs this state only if the target would make changes, and
	// runs it before the target. SPEC section 11.5.
	Prereq
	// Use inherits arguments from the target.
	Use
	// Listen is like Watch, but the reaction runs at the end of the run
	// rather than in place.
	Listen
)

var reqNames = map[ReqKind]string{
	Require: "require", RequireAny: "require_any",
	Watch: "watch", WatchAny: "watch_any",
	OnChanges: "onchanges", OnChangesAny: "onchanges_any",
	OnFail: "onfail", OnFailAny: "onfail_any", OnFailAll: "onfail_all",
	Prereq: "prereq", Use: "use", Listen: "listen",
}

func (k ReqKind) String() string { return reqNames[k] }

// forwardReqs maps the argument name written in an SLS file to its kind.
var forwardReqs = map[string]ReqKind{
	"require": Require, "require_any": RequireAny,
	"watch": Watch, "watch_any": WatchAny,
	"onchanges": OnChanges, "onchanges_any": OnChangesAny,
	"onfail": OnFail, "onfail_any": OnFailAny, "onfail_all": OnFailAll,
	"prereq": Prereq, "use": Use, "listen": Listen,
}

// inverseReqs are the `_in` forms, which are resolved into the forward
// form during compilation so that nothing downstream has to know they
// existed.
var inverseReqs = map[string]ReqKind{
	"require_in": Require, "watch_in": Watch,
	"onchanges_in": OnChanges, "onfail_in": OnFail,
	"prereq_in": Prereq, "use_in": Use, "listen_in": Listen,
}

// notInverseReqs are `_in` spellings Salt does not have, mapped to the
// forward requisite they look like. They are refused rather than read.
//
// halite resolved the four `_any_in` forms as if Salt had them. Salt
// has none (`requisite_in` in salt/state.py lists the seven above), so
// it passes the keyword to the state function as an ordinary argument
// and attaches nothing: captured from 3006.28 and 3008.3, with `a`
// failing and `require_any_in: [sls: g1]`, g1's states ran and
// succeeded, where halite skipped them as failed. Read as a requisite
// the keyword guards states it does not guard in Salt; ignored, it is
// a requisite the author thinks is there. DIVERGENCE 5.478.
var notInverseReqs = map[string]string{
	"require_any_in": "require_any", "watch_any_in": "watch_any",
	"onchanges_any_in": "onchanges_any", "onfail_any_in": "onfail_any",
	"onfail_all_in": "onfail_all",
}

// IsRequisiteArg reports whether an argument name is a requisite rather
// than a module parameter.
func IsRequisiteArg(name string) bool {
	if _, ok := forwardReqs[name]; ok {
		return true
	}
	if _, ok := inverseReqs[name]; ok {
		return true
	}
	_, ok := notInverseReqs[name]
	return ok
}

// ReqRef names the target of a requisite. Exactly one of ID, SLS, or the
// State+ID pair identifies it.
type ReqRef struct {
	// State is the state module, when the requisite was written as
	// `- pkg: nginx_installed`.
	State string
	// ID is the target state ID.
	ID string
	// SLS depends on every state in an SLS file.
	SLS string
	Pos value.Pos
}

func (r ReqRef) String() string {
	switch {
	case r.SLS != "":
		return "sls: " + r.SLS
	case r.State != "":
		return r.State + ": " + r.ID
	default:
		return r.ID
	}
}

// Req is one requisite argument on one chunk.
//
// A requisite holds every reference in its list rather than one per
// entry, because the `_any` and `_all` forms are statements about the
// whole list: splitting `onchanges: [a, b]` into two requisites would make
// `_all` mean "either", which is the opposite of what it says.
type Req struct {
	Kind ReqKind
	Refs []ReqRef
	// Resolved holds the chunk indices this requisite points at, filled in
	// by resolution.
	Resolved []int
}

// Describe renders the requisite's targets for a diagnostic.
func (r Req) Describe() string {
	parts := make([]string, len(r.Refs))
	for i, ref := range r.Refs {
		parts[i] = ref.String()
	}
	return strings.Join(parts, ", ")
}

// parseReqList reads the value of a requisite argument into references.
//
// The four spellings of SPEC section 11.3 are all accepted:
// `- pkg: nginx_installed`, `- nginx_installed`, `- sls: some.sls`, and
// `- id: nginx_installed`.
func parseReqList(v any, pos value.Pos, sls, id, argName string, diags *Diags) []ReqRef {
	var items []any
	switch t := v.(type) {
	case []any:
		items = t
	case string, *value.Map:
		// A single requisite written without a list.
		items = []any{t}
	case nil:
		return nil
	default:
		diags.Add(pos, sls, id, "%s must hold a list of requisites, found %s", argName, value.TypeName(v))
		return nil
	}

	var out []ReqRef
	for _, item := range items {
		switch t := item.(type) {
		case string:
			out = append(out, ReqRef{ID: t, Pos: pos})
		case *value.Map:
			if t.Len() != 1 {
				diags.Add(pos, sls, id, "a requisite entry must name exactly one target, found %d keys", t.Len())
				continue
			}
			e := t.Entries()[0]
			key := value.KeyString(e.Key)
			target, ok := e.Val.(string)
			if !ok {
				diags.Add(e.ValPos, sls, id, "a requisite target must be a string, found %s", value.TypeName(e.Val))
				continue
			}
			switch key {
			case "sls":
				out = append(out, ReqRef{SLS: target, Pos: e.ValPos})
			case "id":
				out = append(out, ReqRef{ID: target, Pos: e.ValPos})
			default:
				out = append(out, ReqRef{State: key, ID: target, Pos: e.ValPos})
			}
		default:
			diags.Add(pos, sls, id, "a requisite entry must be a state ID or a `module: id` mapping, found %s", value.TypeName(item))
		}
	}
	return out
}

// resolveRequisites turns every requisite reference into chunk indices,
// and reports every reference that does not resolve.
//
// Reporting all of them together rather than the first is deliberate: a
// tree with twenty stale requisites should take one compile to find them,
// not twenty.
func resolveRequisites(chunks []*Chunk, diags *Diags) {
	byID := map[string][]int{}
	bySLS := map[string][]int{}
	byStateID := map[string]int{}

	for i, c := range chunks {
		byID[c.ID] = append(byID[c.ID], i)
		bySLS[c.SLS] = append(bySLS[c.SLS], i)
		byStateID[c.State+"|"+c.ID] = i
		// A requisite may name the resolved `name` rather than the ID,
		// which trees do when the ID is a path and the name is set.
		if c.Name != c.ID {
			byStateID[c.State+"|"+c.Name] = i
		}
	}

	for _, c := range chunks {
		for ri := range c.Reqs {
			req := &c.Reqs[ri]
			for _, ref := range req.Refs {
				resolveOneRef(chunks, c, req, ref, byID, bySLS, byStateID, diags)
			}
		}
	}
}

// resolveOneRef turns a single reference into chunk indices.
func resolveOneRef(
	chunks []*Chunk, c *Chunk, req *Req, ref ReqRef,
	byID map[string][]int, bySLS map[string][]int, byStateID map[string]int,
	diags *Diags,
) {
	{
		{
			switch {
			case ref.SLS != "":
				idx, ok := bySLS[ref.SLS]
				// `sls: 'web.*'` is every state of every sls the glob
				// matches, in declaration order, as Salt 3006.28 and
				// 3008.3 resolved it (captured 2026-10-29: `sls: "g*"`
				// ran g1's and g2's states first and failed with g2's).
				// One that matches nothing is refused, as 3006.28 has it;
				// 3008.3 ran the state as if the requisite were not
				// there, which turns a typo into a requisite that does
				// nothing. DIVERGENCE 5.475.
				if !ok && fnmatch.HasMeta(ref.SLS) {
					for i, o := range chunks {
						if fnmatch.Match(ref.SLS, o.SLS) {
							idx = append(idx, i)
						}
					}
					if len(idx) == 0 {
						diags.Add(ref.Pos, c.SLS, c.ID,
							"%s names sls %q, a glob that matches no sls in this run", req.Kind, ref.SLS)
						return
					}
					ok = true
				}
				if !ok {
					diags.Add(ref.Pos, c.SLS, c.ID,
						"%s names sls %q, which is not part of this run", req.Kind, ref.SLS)
					return
				}
				req.Resolved = append(req.Resolved, idx...)

			case ref.State != "":
				i, ok := byStateID[ref.State+"|"+ref.ID]
				if !ok && fnmatch.HasMeta(ref.ID) {
					// A glob: every chunk of that module whose ID or name
					// it matches, as Salt resolves `test: 'b*'`. Exact
					// names are tried first, as Salt does, so an ID that
					// happens to hold a `[` still finds itself. A glob
					// that matches nothing is refused, not retried by ID
					// alone: Salt 3008.3 refuses it at compile time and
					// 3006.28 fails the state (DIVERGENCE 5.467).
					matches := globRef(chunks, ref.ID, func(o *Chunk) bool { return o.State == ref.State }, true)
					if len(matches) == 0 {
						diags.Add(ref.Pos, c.SLS, c.ID,
							"%s names `%s: %s`, a glob that matches no %s state in this run", req.Kind, ref.State, ref.ID, ref.State)
						return
					}
					req.Resolved = append(req.Resolved, matches...)
					return
				}
				if !ok {
					// Fall back to matching by ID alone, which is what an
					// operator means when the module name is stale.
					if byIDMatches, idOK := byID[ref.ID]; idOK && len(byIDMatches) == 1 {
						diags.Warn(ref.Pos, c.SLS, c.ID,
							"%s names `%s: %s`, but %q is declared by %s; matching by ID",
							req.Kind, ref.State, ref.ID, ref.ID, chunks[byIDMatches[0]].State)
						req.Resolved = append(req.Resolved, byIDMatches[0])
						return
					}
					diags.Add(ref.Pos, c.SLS, c.ID,
						"%s names `%s: %s`, which is not declared in this run", req.Kind, ref.State, ref.ID)
					return
				}
				req.Resolved = append(req.Resolved, i)

			default:
				// A bare name, with no module. Salt normalises this to
				// an `id` requisite and resolves it against every chunk
				// carrying that id -- its lookup maps an id to a *set*
				// of nodes, and a requisite takes the whole set.
				//
				// So an id declared under two modules is depended on
				// twice, not refused. That is not an edge case: writing
				// several states under one id is ordinary Salt, and
				// one id carrying a `cmd.run` that fetches an archive and
				// an `archive.extracted` that unpacks it is exactly the
				// shape a real tree uses. This refused it as ambiguous and
				// stopped the compilation.
				//
				// Refusing was the conservative reading and it was the
				// wrong one. The ambiguity it worried about -- a
				// requisite guarding the wrong state -- does not arise
				// when the answer is *all* of them: waiting for both
				// halves of an id is what the tree means by naming it.
				matches := byID[ref.ID]
				if len(matches) == 0 && fnmatch.HasMeta(ref.ID) {
					// The bare form globs IDs, as Salt's does.
					matches = globRef(chunks, ref.ID, func(*Chunk) bool { return true }, false)
				}
				if len(matches) == 0 {
					diags.Add(ref.Pos, c.SLS, c.ID,
						"%s names %q, which is not declared in this run", req.Kind, ref.ID)
					return
				}
				req.Resolved = append(req.Resolved, matches...)
			}
		}
	}
}

// globRef lists, in declaration order, the chunks a requisite glob
// matches: by ID, and by name too when byName is set. Salt matches a
// `module: glob` requisite against both, captured on 3006.28 and 3008.3:
// `test: '/tmp/*'` found the state whose ID was `c` and whose name was
// `/tmp/foo`. The bare form was captured matching IDs only.
func globRef(chunks []*Chunk, pattern string, keep func(*Chunk) bool, byName bool) []int {
	var out []int
	for i, o := range chunks {
		if !keep(o) {
			continue
		}
		if fnmatch.Match(pattern, o.ID) || (byName && fnmatch.Match(pattern, o.Name)) {
			out = append(out, i)
		}
	}
	return out
}

// applyUse copies arguments from a `use` target into the chunk that
// declares it, without overwriting an argument the chunk set itself.
func applyUse(chunks []*Chunk, diags *Diags) {
	for _, c := range chunks {
		for _, req := range c.Reqs {
			if req.Kind != Use {
				continue
			}
			for _, idx := range req.Resolved {
				src := chunks[idx]
				for _, e := range src.Args.Entries() {
					name := value.KeyString(e.Key)
					// `name` is identity, and the requisites are the
					// relationship; neither is inherited.
					if name == "name" || name == "names" || IsRequisiteArg(name) {
						continue
					}
					if c.Args.Has(name) {
						continue
					}
					c.Args.SetAt(e.Key, value.Deep(e.Val), e.KeyPos, e.ValPos)
				}
			}
		}
	}
}

// listenSLSRefusal is the refusal of `listen` or `listen_in` naming an
// sls. Salt's listen resolution reads `sls: g1` as state module `sls`,
// ID `g1`, finds no such state, and fails the run with an error state
// "Referenced state sls: g1 does not exist" -- captured for `listen_in`
// from 3006.28 and 3008.3, read from salt/state.py for `listen`, whose
// lookup is the same loop. halite attached a reaction to every state of
// the sls. DIVERGENCE 5.478.
const listenSLSRefusal = "%s names sls %q; Salt's listen takes no sls " +
	"and fails the run with \"Referenced state sls: %[2]s does not exist\""
