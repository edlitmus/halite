package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// The reaction types of SPEC 18.1.
const (
	// ReactLocal is a remote execution job.
	ReactLocal = "local"
	// ReactRunner is a hub runner.
	ReactRunner = "runner"
	// ReactWheel is a hub management function. This build has one
	// hub-function namespace, so it reaches the runner registry.
	ReactWheel = "wheel"
	// ReactCaller is a local execution on the node that fired the
	// event, used from a node's own reactor.
	ReactCaller = "caller"
)

// Reaction is one declaration in a reaction SLS: what to run and how.
type Reaction struct {
	// ID is the declaration name, which is what a report names.
	ID string
	// Kind is local, runner, wheel, or caller.
	Kind string
	// Fun is the function, after the kind.
	Fun string
	// Target is `tgt` for a local reaction.
	Target     string
	TargetKind string
	Arg        []string
	Kwarg      map[string]any
	// SLS is the file it came from, for a diagnostic that names it.
	SLS string
}

// Describe renders the reaction for a report.
func (r Reaction) Describe() string { return r.Kind + "." + r.Fun }

// renderReaction renders one reaction file against an event.
//
// The template context is SPEC 18.1's: `data`, `tag`, `id`, `grains`,
// and `pillar`. `salt` is absent -- SPEC 25.5 restricts the hub's
// dispatcher to a named safe set, and this build gives a reaction none
// rather than one that has not been audited against that list.
func (s *Server) renderReaction(path string, e *eventbus.Event) (*value.Map, error) {
	src, name, err := s.readReaction(path)
	if err != nil {
		return nil, err
	}

	data := value.NewMap(len(e.Data) + 4)
	for _, k := range sortedKeys(e.Data) {
		data.Set(k, e.Data[k])
	}
	// Salt's reaction SLS reads `data['id']` for the node that caused
	// the event, and `data['_stamp']` and its neighbours for the
	// envelope. Both are put where an existing reaction looks.
	if e.Node != "" && !data.Has("id") {
		data.Set("id", e.Node)
	}
	data.Set("_tag", e.Tag)
	data.Set("_stamp", e.Stamp.UTC().Format("2006-01-02T15:04:05.000000Z07:00"))
	if e.Correlation != "" {
		data.Set("_correlation", e.Correlation)
	}

	res, err := render.Render(src, render.Options{
		File:   name,
		SLS:    strings.TrimSuffix(filepath.Base(name), ".sls"),
		Env:    "base",
		NodeID: e.Node,
		Grains: value.NewMap(0),
		Pillar: value.NewMap(0),
		Config: s.configValues(),
		// Strict, because a reaction that silently reads an undefined
		// name is one that dispatches the wrong job or none at all,
		// and the event that triggered it will not come again.
		Undefined: template.Strict,
		Extra: map[string]any{
			"data": data,
			"tag":  e.Tag,
			"id":   e.Node,
		},
	})
	for _, w := range res.Warnings {
		s.warn(w.String(), "component", "reactor", "file", name)
	}
	if err != nil {
		return nil, err
	}
	if res.Value == nil {
		// An empty reaction is legitimate: a template may decide this
		// event needs nothing.
		return value.NewMap(0), nil
	}
	doc, ok := res.Value.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("%s: a reaction is a mapping of declarations, not %s",
			name, value.TypeName(res.Value))
	}
	return doc, nil
}

// readReaction resolves a reaction file, from the hub's tree when it is
// named as one and from the filesystem otherwise.
//
// SPEC 18.1 writes them as absolute paths, which is what an operator
// with a `/srv/reactor` directory has. A `salt://` name is accepted too,
// because an estate that keeps its reactions in the state tree should
// not have to keep them twice.
func (s *Server) readReaction(path string) ([]byte, string, error) {
	if fileserver.IsManagedURI(path) {
		if s.Files == nil {
			return nil, "", fmt.Errorf("%s names the hub's tree and this hub serves none", path)
		}
		env := fileserver.EnvFromURI(path, "base")
		rel := fileserver.StripScheme(path)
		src, resolved, err := s.Files.Read(env, rel)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", path, err)
		}
		return src, resolved, nil
	}
	src, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, "", fmt.Errorf("reading the reaction: %w", err)
	}
	return src, path, nil
}

// parseReactions turns a rendered reaction document into what to run.
//
// The declaration form is Salt's and is not a state declaration:
// `local.service.restart` is the kind, the module, and the function,
// and the state compiler would read it as a module called
// `local.service`. A reaction has no requisites and no ordering either,
// so it gets a parser of its own rather than a compiler bent to fit.
func parseReactions(doc *value.Map, sls string) ([]Reaction, error) {
	var out []Reaction
	for _, decl := range doc.Entries() {
		id := value.KeyString(decl.Key)
		body, ok := decl.Val.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("%s: %s is not a mapping of one reaction", sls, id)
		}
		if body.Len() != 1 {
			return nil, fmt.Errorf("%s: %s declares %d reactions; each declaration runs one thing",
				sls, id, body.Len())
		}
		call := body.Entries()[0]
		kind, fun, found := strings.Cut(value.KeyString(call.Key), ".")
		if !found || fun == "" {
			return nil, fmt.Errorf("%s: %s: %q is not a reaction; it is written as "+
				"local.<function>, runner.<function>, wheel.<function>, or caller.<function>",
				sls, id, value.KeyString(call.Key))
		}
		switch kind {
		case ReactLocal, ReactRunner, ReactWheel, ReactCaller:
		default:
			return nil, fmt.Errorf("%s: %s: %q is not a reaction type; there are "+
				"local, runner, wheel, and caller", sls, id, kind)
		}

		r := Reaction{ID: id, Kind: kind, Fun: fun, SLS: sls}
		if err := applyReactionArgs(&r, call.Val); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", sls, id, err)
		}
		if r.Kind == ReactLocal && r.Target == "" {
			return nil, fmt.Errorf("%s: %s: a local reaction needs `tgt`", sls, id)
		}
		out = append(out, r)
	}
	return out, nil
}

// applyReactionArgs reads the list of single-key mappings under a
// reaction, which is the shape a state declaration uses and which an
// existing reaction SLS is written in.
func applyReactionArgs(r *Reaction, v any) error {
	if v == nil {
		return nil
	}
	items, ok := v.([]any)
	if !ok {
		return fmt.Errorf("the arguments are a list of single-key mappings, not %s", value.TypeName(v))
	}
	for _, item := range items {
		m, ok := item.(*value.Map)
		if !ok || m.Len() != 1 {
			return fmt.Errorf("each argument is a mapping of one key to its value")
		}
		e := m.Entries()[0]
		name := value.KeyString(e.Key)
		switch name {
		case "tgt":
			r.Target = value.KeyString(e.Val)
		case "tgt_type", "expr_form":
			r.TargetKind = value.KeyString(e.Val)
		case "arg", "args":
			// Salt's reactor accepts both spellings, and `args` as a
			// mapping is how the SPEC 18.1 example passes keywords.
			if m, ok := e.Val.(*value.Map); ok {
				r.Kwarg = mapToKwargs(m)
				continue
			}
			list, ok := e.Val.([]any)
			if !ok {
				return fmt.Errorf("`%s` is a list, or a mapping of keyword arguments", name)
			}
			for _, a := range list {
				r.Arg = append(r.Arg, value.KeyString(a))
			}
		case "kwarg", "kwargs":
			m, ok := e.Val.(*value.Map)
			if !ok {
				return fmt.Errorf("`%s` is a mapping", name)
			}
			r.Kwarg = mapToKwargs(m)
		default:
			return fmt.Errorf("%q is not a reaction argument; there are tgt, tgt_type, arg, and kwarg", name)
		}
	}
	return nil
}

func mapToKwargs(m *value.Map) map[string]any {
	out := make(map[string]any, m.Len())
	for _, e := range m.Entries() {
		out[value.KeyString(e.Key)] = e.Val
	}
	return out
}

// ReactionResult is what one reaction did.
type ReactionResult struct {
	Reaction Reaction
	// JID is the job or runner call it produced.
	JID string
	// Nodes is who a local reaction reached.
	Nodes []string
	Error string
}

// Succeeded reports whether the reaction dispatched.
func (r ReactionResult) Succeeded() bool { return r.Error == "" }

// runReactions dispatches every reaction from one file.
//
// Each is authorized as the entry's principal, exactly like a human
// caller: SPEC 18.3's departure from Salt, where the reactor runs with
// the control plane's full privilege and a node that can fire the right
// event can therefore cause anything.
func (s *Server) runReactions(ctx context.Context, entry ReactorEntry, e *eventbus.Event, reactions []Reaction) []ReactionResult {
	out := make([]ReactionResult, 0, len(reactions))
	for _, r := range reactions {
		res := ReactionResult{Reaction: r}
		switch r.Kind {
		case ReactLocal:
			j, err := s.DispatchAs(entry.Principal, Submission{
				Target:      r.Target,
				TargetKind:  r.TargetKind,
				Fun:         r.Fun,
				Arg:         r.Arg,
				Kwarg:       r.Kwarg,
				Correlation: correlationOf(e),
			})
			if err != nil {
				res.Error = err.Error()
				break
			}
			res.JID, res.Nodes = string(j.JID), j.Nodes

		case ReactCaller:
			// The node that fired the event, and only that node. A
			// reaction that could name another node would let one node
			// cause execution on another by choosing a tag.
			if e.Node == "" {
				res.Error = "a caller reaction runs on the node that fired the event, and this event names none"
				break
			}
			j, err := s.DispatchAs(entry.Principal, Submission{
				Target:      e.Node,
				TargetKind:  "L",
				Fun:         r.Fun,
				Arg:         r.Arg,
				Kwarg:       r.Kwarg,
				Correlation: correlationOf(e),
			})
			if err != nil {
				res.Error = err.Error()
				break
			}
			res.JID, res.Nodes = string(j.JID), j.Nodes

		case ReactRunner, ReactWheel:
			outcome, err := s.CallRunner(ctx, RunnerCall{
				Principal: entry.Principal,
				Fun:       r.Fun,
				Arg:       r.Arg,
				Kwarg:     r.Kwarg,
			})
			if err != nil {
				res.Error = err.Error()
				break
			}
			res.JID = string(outcome.JID)
			if !outcome.Success {
				res.Error = outcome.Err
			}
		}
		out = append(out, res)
	}
	return out
}

// correlationOf is the chain an event belongs to, so that what a
// reaction causes can be traced back to what caused the reaction. The
// reactor names one before it acts, so this is only a fallback for a
// caller that did not go through it -- `reactor.test`, which dispatches
// nothing.
func correlationOf(e *eventbus.Event) string {
	if e.Correlation != "" {
		return e.Correlation
	}
	return e.Offset
}

// reactionTimeout is SPEC 18.2's per-reaction bound on rendering and
// dispatch. The job a reaction dispatches has its own.
const reactionTimeout = 60 * time.Second
