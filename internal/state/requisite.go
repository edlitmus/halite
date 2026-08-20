package state

import (
	"strings"

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
	"require_in": Require, "require_any_in": RequireAny,
	"watch_in": Watch, "watch_any_in": WatchAny,
	"onchanges_in": OnChanges, "onchanges_any_in": OnChangesAny,
	"onfail_in": OnFail, "onfail_any_in": OnFailAny,
	"prereq_in": Prereq, "use_in": Use, "listen_in": Listen,
}

// IsRequisiteArg reports whether an argument name is a requisite rather
// than a module parameter.
func IsRequisiteArg(name string) bool {
	if _, ok := forwardReqs[name]; ok {
		return true
	}
	_, ok := inverseReqs[name]
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
				if !ok {
					diags.Add(ref.Pos, c.SLS, c.ID,
						"%s names sls %q, which is not part of this run", req.Kind, ref.SLS)
					return
				}
				req.Resolved = append(req.Resolved, idx...)

			case ref.State != "":
				i, ok := byStateID[ref.State+"|"+ref.ID]
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
				matches := byID[ref.ID]
				switch len(matches) {
				case 0:
					diags.Add(ref.Pos, c.SLS, c.ID,
						"%s names %q, which is not declared in this run", req.Kind, ref.ID)
				case 1:
					req.Resolved = append(req.Resolved, matches[0])
				default:
					// An ID that resolves to more than one module is
					// ambiguous, and picking one silently is how a
					// requisite ends up guarding the wrong state.
					var related []Related
					var names []string
					for _, m := range matches {
						related = append(related, Related{Pos: chunks[m].Pos, Msg: "declared by " + chunks[m].State})
						names = append(names, chunks[m].State)
					}
					diags.AddRelated(ref.Pos, c.SLS, c.ID, related,
						"%s names %q, which is declared by more than one module (%s); write it as `%s: %s`",
						req.Kind, ref.ID, strings.Join(names, ", "), names[0], ref.ID)
				}
			}
		}
	}
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
