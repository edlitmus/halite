// Package reactor turns events into work: rules match event tags and
// dispatch jobs, with the event's own fields available as template values.
//
// The dangerous property of any reactor is the feedback loop — dispatching
// a job raises events, which can match a rule, which dispatches a job. Two
// things break that here: the reactor never reacts to work it caused, and
// reactions are rate limited with a loud log when the limit bites.
package reactor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/edlitmus/halite/internal/event"
	"github.com/edlitmus/halite/internal/logging"
	"github.com/edlitmus/halite/internal/sls"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/yamlite"
)

// Action is one job a rule dispatches. Every string field is a template
// rendered against the event that triggered it.
type Action struct {
	Kind   string
	Target string
	SLS    []string
	Fn     string
	Args   map[string]string
	Test   bool
}

// Rule is a tag pattern and what to do when it matches.
type Rule struct {
	Tag     string
	Actions []Action
}

// Dispatcher queues a job. The control plane supplies one that goes
// through the same path an operator's dispatch does.
type Dispatcher func(transport.DispatchRequest, string) (transport.DispatchResponse, error)

// DefaultRateLimit is how many reactions one rule may fire per minute
// before its reactions are refused. Generous for real use, low enough that
// a loop announces itself instead of melting the fleet. The budget is per
// rule so a looping rule cannot starve the others.
const DefaultRateLimit = 60

// Reactor watches the bus and dispatches.
type Reactor struct {
	rules    []Rule
	dispatch Dispatcher
	log      *logging.Logger

	limit  int
	mu     sync.Mutex
	window time.Time
	fired  map[string]int // reactions per rule tag in the current window
}

// New builds a reactor over the given rules.
func New(rules []Rule, dispatch Dispatcher, logger *logging.Logger) *Reactor {
	return &Reactor{rules: rules, dispatch: dispatch, log: logger,
		limit: DefaultRateLimit, fired: map[string]int{}}
}

// Rules reports how many rules are loaded, for startup logging.
func (r *Reactor) Rules() int { return len(r.rules) }

// Run subscribes to the bus and reacts until ctx is cancelled.
func (r *Reactor) Run(ctx context.Context, bus *event.Bus) {
	if len(r.rules) == 0 {
		return
	}
	events, cancel := bus.Subscribe("**")
	defer cancel()

	for {
		select {
		case ev, open := <-events:
			if !open {
				return
			}
			r.react(ev)
		case <-ctx.Done():
			return
		}
	}
}

// react runs every rule matching an event.
func (r *Reactor) react(ev event.Event) {
	// Work the reactor caused must not feed it again. Without this, a rule
	// on halite/job/** dispatches a job, whose dispatch and return events
	// match the same rule, forever.
	if caused, _ := ev.Data["reactor"].(bool); caused {
		return
	}
	for _, rule := range r.rules {
		if !event.TagMatch(rule.Tag, ev.Tag) {
			continue
		}
		for _, action := range rule.Actions {
			r.fire(rule, action, ev)
		}
	}
}

func (r *Reactor) fire(rule Rule, action Action, ev event.Event) {
	if !r.allow(rule.Tag) {
		r.log.Warnf("reactor: rule %q hit its rate limit of %d/min, dropping reaction for %q "+
			"(it is probably reacting to its own work)", rule.Tag, r.limit, ev.Tag)
		return
	}
	req, err := render(action, ev)
	if err != nil {
		r.log.Errorf("reactor: rule %q: %v", rule.Tag, err)
		return
	}
	resp, err := r.dispatch(req, "reactor")
	if err != nil {
		r.log.Errorf("reactor: rule %q: dispatch: %v", rule.Tag, err)
		return
	}
	r.log.Infof("reactor: %q fired on %q: job %s -> %d agent(s)",
		rule.Tag, ev.Tag, resp.JobID, len(resp.Agents))
}

// allow implements a simple per-minute budget, counted per rule.
func (r *Reactor) allow(tag string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if now.Sub(r.window) >= time.Minute {
		r.window = now
		r.fired = map[string]int{}
	}
	if r.fired[tag] >= r.limit {
		return false
	}
	r.fired[tag]++
	return true
}

// render fills an action's templates from the event and validates the
// result. Rendering produces data for a dispatch, never a shell command.
func render(action Action, ev event.Event) (transport.DispatchRequest, error) {
	req := transport.DispatchRequest{Kind: action.Kind, Test: action.Test}

	var err error
	if req.Target, err = renderString("target", action.Target, ev); err != nil {
		return req, err
	}
	if req.Fn, err = renderString("fn", action.Fn, ev); err != nil {
		return req, err
	}
	for _, name := range action.SLS {
		rendered, err := renderString("sls", name, ev)
		if err != nil {
			return req, err
		}
		req.SLS = append(req.SLS, rendered)
	}
	if len(action.Args) > 0 {
		req.Args = make(map[string]string, len(action.Args))
		for key, value := range action.Args {
			rendered, err := renderString("args."+key, value, ev)
			if err != nil {
				return req, err
			}
			req.Args[key] = rendered
		}
	}

	if req.Target == "" {
		return req, fmt.Errorf("action has no target after rendering")
	}
	return req, nil
}

// renderString fills one template from the event. Unlike SLS rendering,
// a missing key is an error rather than an empty string: a rule that
// references data the event does not carry is a broken rule, and
// dispatching a job with a blank target would be worse than not firing.
func renderString(field, tmpl string, ev event.Event) (string, error) {
	if tmpl == "" || !strings.Contains(tmpl, "{{") {
		return tmpl, nil
	}
	parsed, err := template.New("reactor:" + field).
		Option("missingkey=error").
		Funcs(sls.TemplateFuncs()).
		Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	var buf bytes.Buffer
	if err := parsed.Execute(&buf, eventValues(ev)); err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	return strings.TrimSpace(buf.String()), nil
}

// eventValues exposes the event to templates as {{ .Tag }}, {{ .Source }},
// {{ .Data.x }}.
func eventValues(ev event.Event) map[string]any {
	return map[string]any{
		"ID":     ev.ID,
		"Tag":    ev.Tag,
		"Source": ev.Source,
		"Time":   ev.Time,
		"Data":   ev.Data,
	}
}

// Load reads a reactor rules file:
//
//	'halite/agent/*/hello':
//	  - run:
//	      kind: state.highstate
//	      target: '{{ .Source }}'
//
// A missing file is not an error — no rules simply means no reactor.
func Load(path string) ([]Rule, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	tree, err := yamlite.Parse(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	root, ok := tree.(*yamlite.Map)
	if !ok {
		return nil, fmt.Errorf("%s: top level must be a mapping of tag patterns", path)
	}

	var rules []Rule
	for _, tag := range root.Keys {
		actions, err := parseActions(root.Vals[tag])
		if err != nil {
			return nil, fmt.Errorf("%s: %q: %w", path, tag, err)
		}
		rules = append(rules, Rule{Tag: tag, Actions: actions})
	}
	return rules, nil
}

func parseActions(v any) ([]Action, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("must be a list of actions")
	}
	var actions []Action
	for _, item := range list {
		entry, ok := item.(*yamlite.Map)
		if !ok {
			return nil, fmt.Errorf("each action must be a '- run:' mapping")
		}
		for _, verb := range entry.Keys {
			if verb != "run" {
				return nil, fmt.Errorf("unknown action %q (only 'run' exists)", verb)
			}
			action, err := parseRun(entry.Vals[verb])
			if err != nil {
				return nil, err
			}
			actions = append(actions, action)
		}
	}
	if len(actions) == 0 {
		return nil, fmt.Errorf("no actions")
	}
	return actions, nil
}

func parseRun(v any) (Action, error) {
	body, ok := v.(*yamlite.Map)
	if !ok {
		return Action{}, fmt.Errorf("run: must be a mapping")
	}
	action := Action{}
	for _, key := range body.Keys {
		value := body.Vals[key]
		switch key {
		case "kind":
			action.Kind, _ = value.(string)
		case "target":
			action.Target, _ = value.(string)
		case "fn":
			action.Fn, _ = value.(string)
		case "test":
			text, _ := value.(string)
			action.Test = text == "true" || text == "yes"
		case "sls":
			names, err := stringList(value)
			if err != nil {
				return action, fmt.Errorf("sls: %w", err)
			}
			action.SLS = names
		case "args":
			argMap, ok := value.(*yamlite.Map)
			if !ok {
				return action, fmt.Errorf("args: must be a mapping")
			}
			action.Args = map[string]string{}
			for _, argKey := range argMap.Keys {
				text, ok := argMap.Vals[argKey].(string)
				if !ok {
					return action, fmt.Errorf("args: %s must be a scalar", argKey)
				}
				action.Args[argKey] = text
			}
		default:
			return action, fmt.Errorf("unknown key %q in run:", key)
		}
	}
	if action.Kind == "" {
		return action, fmt.Errorf("run: needs a kind")
	}
	if action.Target == "" {
		return action, fmt.Errorf("run: needs a target")
	}
	return action, nil
}

func stringList(v any) ([]string, error) {
	switch t := v.(type) {
	case string:
		return []string{t}, nil
	case []any:
		var out []string
		for _, item := range t {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("entries must be scalars")
			}
			out = append(out, text)
		}
		return out, nil
	}
	return nil, fmt.Errorf("must be a scalar or a list")
}
