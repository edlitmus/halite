// Package schedule runs work on the agent's own clock: a highstate every
// half hour, a state file every night, an execution module every minute.
//
// It is halite's answer to Salt's minion-side scheduler, and to the reason
// most Salt fleets have one — without it a fleet only converges when
// somebody pokes the control plane, so a host that drifts at 02:00 stays
// drifted until someone notices. Splay spreads a fleet's runs across the
// interval, because two hundred hosts pulling a state tree in the same
// second is a thundering herd.
package schedule

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/logging"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/yamlite"
)

// Job is one piece of scheduled work.
type Job struct {
	Name    string
	Kind    string // highstate, apply, or call
	SLS     []string
	Fn      string
	Args    map[string]string
	Every   time.Duration
	Splay   time.Duration
	Test    bool
	AtStart bool
}

// Run executes one scheduled job. The agent supplies it, so the scheduler
// itself knows nothing about control planes.
type Run func(ctx context.Context, job Job)

// Runner fires each job on its own timer.
type Runner struct {
	jobs []Job
	run  Run
	log  *logging.Logger
}

// NewRunner builds a runner. It does nothing until Run is called.
func NewRunner(jobs []Job, run Run, logger *logging.Logger) *Runner {
	return &Runner{jobs: jobs, run: run, log: logger}
}

// Count reports how many jobs are scheduled, for startup logging.
func (r *Runner) Count() int { return len(r.jobs) }

// Run fires jobs until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, job := range r.jobs {
		wg.Add(1)
		go func(job Job) {
			defer wg.Done()
			r.loop(ctx, job)
		}(job)
	}
	wg.Wait()
}

func (r *Runner) loop(ctx context.Context, job Job) {
	if job.AtStart {
		r.fire(ctx, job)
	}
	ticker := time.NewTicker(job.Every)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			// The splay delays the run, not the tick, so the period stays
			// what the config says while the fleet spreads out inside it.
			if !sleep(ctx, splayOffset(job.Splay)) {
				return
			}
			r.fire(ctx, job)
		case <-ctx.Done():
			return
		}
	}
}

func (r *Runner) fire(ctx context.Context, job Job) {
	if ctx.Err() != nil {
		return
	}
	r.log.Infof("schedule %s: running %s", job.Name, job.Kind)
	r.run(ctx, job)
}

func splayOffset(splay time.Duration) time.Duration {
	if splay <= 0 {
		return 0
	}
	return rand.N(splay)
}

// sleep waits, and reports whether it finished rather than being cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Load reads a schedule config file:
//
//	converge:
//	  kind: highstate
//	  interval: 30m
//	  splay: 5m
//	nightly-tls:
//	  kind: apply
//	  sls: web.tls
//	  interval: 24h
//	  test: true
//	disk:
//	  kind: call
//	  fn: disk.usage
//	  interval: 5m
//
// Each key is the job's name, which is also the event tag it reports
// under. A missing file is not an error — an unscheduled agent is a valid
// choice, and the one every existing deployment already has.
func Load(path string) ([]Job, error) {
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
		return nil, fmt.Errorf("%s: top level must be a mapping of job names", path)
	}

	var jobs []Job
	for _, name := range root.Keys {
		body, ok := root.Vals[name].(*yamlite.Map)
		if !ok {
			return nil, fmt.Errorf("%s: %s: must be a mapping of settings", path, name)
		}
		job, err := build(name, body)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", path, name, err)
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// build turns one config entry into a job, rejecting the combinations that
// would fail every time they fired.
func build(name string, body *yamlite.Map) (Job, error) {
	job := Job{Name: name, Kind: str(body, "kind")}
	if job.Kind == "" {
		job.Kind = transport.KindHighstate
	}
	every, err := duration(body, "interval")
	if err != nil {
		return Job{}, err
	}
	if every <= 0 {
		return Job{}, fmt.Errorf("interval is required (e.g. 30m)")
	}
	job.Every = every
	if job.Splay, err = duration(body, "splay"); err != nil {
		return Job{}, err
	}
	if job.Splay >= job.Every {
		return Job{}, fmt.Errorf("splay %s must be shorter than interval %s", job.Splay, job.Every)
	}
	job.Test = truthy(str(body, "test"))
	job.AtStart = truthy(str(body, "at_start"))

	// Kinds are written the short way a schedule reads best, and stored as
	// the job kinds the agent already executes.
	switch job.Kind {
	case "highstate", transport.KindHighstate:
		job.Kind = transport.KindHighstate
	case "apply", transport.KindApply:
		job.Kind = transport.KindApply
		job.SLS = list(body, "sls")
		if len(job.SLS) == 0 {
			return Job{}, fmt.Errorf("apply needs sls")
		}
	case transport.KindCall: // already "call"
		job.Fn = str(body, "fn")
		if job.Fn == "" {
			return Job{}, fmt.Errorf("call needs fn")
		}
		job.Args = stringMap(body, "args")
	default:
		return Job{}, fmt.Errorf("kind %q is not one of highstate, apply, call", job.Kind)
	}
	return job, nil
}

func str(m *yamlite.Map, key string) string {
	s, _ := m.Vals[key].(string)
	return s
}

func list(m *yamlite.Map, key string) []string {
	switch v := m.Vals[key].(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func stringMap(m *yamlite.Map, key string) map[string]string {
	body, ok := m.Vals[key].(*yamlite.Map)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(body.Keys))
	for _, k := range body.Keys {
		if s, ok := body.Vals[k].(string); ok {
			out[k] = s
		}
	}
	return out
}

func duration(m *yamlite.Map, key string) (time.Duration, error) {
	raw := str(m, key)
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration (30s, 5m, 24h)", key, raw)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s: %q is negative", key, raw)
	}
	return d, nil
}

func truthy(raw string) bool {
	switch strings.ToLower(raw) {
	case "true", "yes", "1", "on":
		return true
	}
	return false
}
