// Package mine publishes facts from each agent to the control plane so
// that other hosts can use them. It is halite's Salt Mine: the way a load
// balancer's configuration learns the addresses of every backend without
// anyone writing them down.
//
// Agents push on a schedule; the control plane keeps the latest value per
// agent per function; states read the whole thing as {{ .Mine }}.
package mine

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/logging"
	"github.com/edlitmus/halite/internal/modules"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/yamlite"
)

// DefaultInterval is how often a function is published when the config
// does not say.
const DefaultInterval = 5 * time.Minute

// FunctionGrains publishes the host's grains. It is not an execution
// module, so it is named explicitly.
const FunctionGrains = "grains"

// Job is one function to publish and how often.
type Job struct {
	Function string
	Every    time.Duration
}

// Publisher sends one function's output to the control plane.
type Publisher func(function string, data map[string]any)

// Runner publishes each configured function on its own schedule.
type Runner struct {
	jobs    []Job
	grains  map[string]any
	publish Publisher
	log     *logging.Logger
}

// NewRunner builds a runner. It does nothing until Run is called.
func NewRunner(jobs []Job, grains map[string]any, publish Publisher, logger *logging.Logger) *Runner {
	return &Runner{jobs: jobs, grains: grains, publish: publish, log: logger}
}

// Count reports how many functions are published, for startup logging.
func (r *Runner) Count() int { return len(r.jobs) }

// Run publishes until ctx is cancelled.
func (r *Runner) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, job := range r.jobs {
		wg.Add(1)
		go func(job Job) {
			defer wg.Done()
			r.publishLoop(ctx, job)
		}(job)
	}
	wg.Wait()
}

func (r *Runner) publishLoop(ctx context.Context, job Job) {
	interval := job.Every
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Publish immediately: a host that has just connected should be usable
	// by other hosts' states now, not one interval from now.
	r.publishOnce(job.Function)
	for {
		select {
		case <-ticker.C:
			r.publishOnce(job.Function)
		case <-ctx.Done():
			return
		}
	}
}

func (r *Runner) publishOnce(function string) {
	data, err := r.collect(function)
	if err != nil {
		r.log.Errorf("mine %s: %v", function, err)
		return
	}
	r.publish(function, data)
}

// collect runs one function locally.
func (r *Runner) collect(function string) (map[string]any, error) {
	if function == FunctionGrains {
		return r.grains, nil
	}
	fn, ok := modules.ExecRegistry[function]
	if !ok {
		return nil, fmt.Errorf("not an execution module")
	}
	return fn(&modules.Ctx{Grains: r.grains}, nil)
}

// Load reads a mine config file:
//
//	network.interfaces:
//	  interval: 60s
//	disk.usage:
//	  interval: 5m
//	grains:
//	  interval: 10m
//
// A missing file is not an error — publishing nothing is a valid choice.
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
		return nil, fmt.Errorf("%s: top level must be a mapping of function names", path)
	}

	var jobs []Job
	for _, function := range root.Keys {
		if function != FunctionGrains {
			if _, known := modules.ExecRegistry[function]; !known {
				return nil, fmt.Errorf("%s: %q is not an execution module or %q",
					path, function, FunctionGrains)
			}
		}
		interval, err := jobInterval(root.Vals[function])
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", path, function, err)
		}
		jobs = append(jobs, Job{Function: function, Every: interval})
	}
	return jobs, nil
}

func jobInterval(v any) (time.Duration, error) {
	if v == nil {
		return DefaultInterval, nil
	}
	body, ok := v.(*yamlite.Map)
	if !ok {
		return 0, fmt.Errorf("must be a mapping (or empty for the default interval)")
	}
	for _, key := range body.Keys {
		if key != "interval" {
			return 0, fmt.Errorf("unknown key %q", key)
		}
	}
	raw, _ := body.Vals["interval"].(string)
	if raw == "" {
		return DefaultInterval, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("interval %q: %w", raw, err)
	}
	if interval < time.Second {
		return 0, fmt.Errorf("interval %q is too short (one second minimum)", raw)
	}
	return interval, nil
}

// ForTemplates flattens the wire form into what states see as {{ .Mine }}:
// function -> agent -> the published data, with the dots in a function
// name replaced by underscores so `{{ .Mine.network_interfaces }}` works
// as a template path.
func ForTemplates(raw map[string]map[string]transport.MineEntry) map[string]any {
	out := map[string]any{}
	for function, byAgent := range raw {
		agents := map[string]any{}
		for agentID, entry := range byAgent {
			agents[agentID] = entry.Data
		}
		out[strings.ReplaceAll(function, ".", "_")] = agents
	}
	return out
}
