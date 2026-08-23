package hub

import (
	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// registerReactorRunner installs `reactor`, which is how an operator
// asks what an event would cause without waiting for one to happen.
func registerReactorRunner(r *Runners) {
	r.Add(
		RunnerModule{
			Sig: runnerSig("reactor", "list",
				"The configured tag globs, the files each runs, and the principal "+
					"each runs as.", "18.1"),
			Fn: func(c *RunnerContext) (any, error) {
				return ReactorSummary(c.Server.Reactors), nil
			},
		},
		RunnerModule{
			Sig: runnerSig("reactor", "test",
				"Render the reactions a tag would fire and print what they would "+
					"dispatch, without dispatching any.", "18.2",
				runnerArg("tag", signature.String, "The event tag to test."),
				runnerOpt("data", signature.Any, nil, "The event payload, as a mapping or as JSON."),
				runnerOpt("node", signature.String, "", "The node the event came from, which a reaction reads as `id`."),
			),
			Fn: func(c *RunnerContext) (any, error) {
				tag := c.arg("tag")
				if err := eventbus.ValidTag(tag); err != nil {
					return nil, err
				}
				data, err := eventData(c.Args)
				if err != nil {
					return nil, err
				}
				event := &eventbus.Event{
					Tag:   tag,
					Node:  c.arg("node"),
					Stamp: c.Server.now(),
					Data:  data,
				}

				matched := Matching(c.Server.Reactors, tag)
				out := value.NewMap(3)
				out.Set("tag", tag)
				if len(matched) == 0 {
					out.Set("matched", []any{})
					out.Set("reactions", []any{})
					return out, nil
				}

				globs := make([]any, 0, len(matched))
				reactions := make([]any, 0)
				for _, entry := range matched {
					globs = append(globs, entry.Tag)
					for _, file := range entry.SLS {
						doc, err := c.Server.renderReaction(file, event)
						if err != nil {
							reactions = append(reactions, reactionError(entry, file, err))
							continue
						}
						parsed, err := parseReactions(doc, file)
						if err != nil {
							reactions = append(reactions, reactionError(entry, file, err))
							continue
						}
						for _, reaction := range parsed {
							reactions = append(reactions, plannedReaction(c, entry, reaction))
						}
					}
				}
				out.Set("matched", globs)
				out.Set("reactions", reactions)
				return out, nil
			},
		},
	)
}

// plannedReaction is one reaction as it would be dispatched, together
// with what the policy would decide about it.
//
// The decision is included because "it renders" and "it is permitted"
// are different questions, and a reaction that renders perfectly and is
// refused at three in the morning is the one an operator wants to find
// out about now. SPEC 18.3.
func plannedReaction(c *RunnerContext, entry ReactorEntry, r Reaction) *value.Map {
	out := value.NewMap(8)
	out.Set("id", r.ID)
	out.Set("sls", r.SLS)
	out.Set("reactor", entry.Tag)
	out.Set("principal", entry.Principal)
	out.Set("kind", r.Kind)
	out.Set("fun", r.Fun)
	if r.Target != "" {
		out.Set("tgt", r.Target)
	}
	if len(r.Arg) > 0 {
		out.Set("arg", stringList(r.Arg))
	}
	if len(r.Kwarg) > 0 {
		kw := value.NewMap(len(r.Kwarg))
		for _, k := range sortedKeys(r.Kwarg) {
			kw.Set(k, r.Kwarg[k])
		}
		out.Set("kwarg", kw)
	}
	out.Set("authorized", c.Server.wouldAuthorize(entry, r))
	return out
}

func reactionError(entry ReactorEntry, file string, err error) *value.Map {
	out := value.NewMap(3)
	out.Set("reactor", entry.Tag)
	out.Set("sls", file)
	out.Set("error", err.Error())
	return out
}

// wouldAuthorize asks the policy what it would decide, without running
// anything.
func (s *Server) wouldAuthorize(entry ReactorEntry, r Reaction) string {
	var decision string
	switch r.Kind {
	case ReactRunner, ReactWheel:
		d := s.Policy.Authorize(policyRequestFor(entry.Principal, r.Fun, "", r.Arg, r.Kwarg, true))
		decision = d.String()
	case ReactCaller:
		d := s.Policy.Authorize(policyRequestFor(entry.Principal, r.Fun, "<the node that fired the event>", r.Arg, r.Kwarg, false))
		decision = d.String()
	default:
		d := s.Policy.Authorize(policyRequestFor(entry.Principal, r.Fun, r.Target, r.Arg, r.Kwarg, false))
		decision = d.String()
	}
	return decision
}
