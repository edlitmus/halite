package schedule

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// Run is one execution of a scheduled job, handed to the node to
// perform.
type Run struct {
	Job *Job
	// Fire is when the schedule said it should have run, which is not
	// the same as when it does: `splay` delays it deliberately, and a
	// busy node delays it by accident. A report that says both is one
	// an operator can act on.
	Fire time.Time
}

// Engine runs the scheduled jobs of SPEC section 20.
type Engine struct {
	Jobs []*Job
	// Execute performs one run. It blocks for as long as the job takes,
	// which is what `maxrunning` counts.
	Execute func(ctx context.Context, r Run) error
	// Log receives a line. Nil discards.
	Log func(level, msg string, kv ...any)
	// Now is the clock, for the tests.
	Now func() time.Time
	// Rand chooses a splay, for the tests.
	Rand func(n int64) int64
	// Wake is how long the engine sleeps between passes when nothing is
	// due sooner. Zero takes a second.
	Wake time.Duration

	mu      sync.Mutex
	running map[string]int
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// random chooses a splay offset.
//
// crypto/rand, because SPEC 25.3 admits nothing else outside the
// template engine's deliberately deterministic seed -- and because a
// fleet whose splay is predictable is a fleet whose arrival times can
// be predicted by anyone who knows the schedule.
func (e *Engine) random(n int64) int64 {
	if e.Rand != nil {
		return e.Rand(n)
	}
	if n <= 0 {
		return 0
	}
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// A node that cannot read randomness runs at the scheduled
		// time rather than not at all.
		return 0
	}
	return int64(binary.BigEndian.Uint64(buf[:]) >> 1 % uint64(n))
}

func (e *Engine) logf(level, msg string, kv ...any) {
	if e.Log != nil {
		e.Log(level, msg, kv...)
	}
}

func (e *Engine) wake() time.Duration {
	if e.Wake > 0 {
		return e.Wake
	}
	return time.Second
}

// Run drives the schedule until the context ends.
func (e *Engine) Run(ctx context.Context) error {
	if e.Execute == nil {
		return fmt.Errorf("a scheduler needs somewhere to send its jobs")
	}
	e.running = map[string]int{}

	now := e.now()
	due := map[string]time.Time{}
	for _, job := range e.Jobs {
		if !job.Enabled {
			continue
		}
		// `run_on_start` and `@reboot` are the same thing said two
		// ways, and both mean now.
		if job.RunOnStart || (job.Cron != nil && job.Cron.Reboot) {
			e.start(ctx, Run{Job: job, Fire: now})
		}
		// `catchup` runs a job once whose window closed while the node
		// was off. Without it a missed run does not backfill, which is
		// what SPEC 20.1 makes the default.
		if job.Catchup && !job.LastRun.IsZero() {
			if missed, ok := job.Next(job.LastRun); ok && missed.Before(now) {
				e.logf("info", "catching up a missed run",
					"job", job.Name, "was_due", missed.Format(time.RFC3339))
				e.start(ctx, Run{Job: job, Fire: missed})
			}
		}
		if next, ok := job.Next(now); ok {
			due[job.Name] = next
		}
	}

	ticker := time.NewTicker(e.wake())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		now = e.now()
		for _, job := range e.Jobs {
			at, scheduled := due[job.Name]
			if !scheduled || now.Before(at) {
				continue
			}
			e.start(ctx, Run{Job: job, Fire: at})
			job.LastRun = now
			if next, ok := job.Next(now); ok {
				due[job.Name] = next
			} else {
				delete(due, job.Name)
			}
		}
	}
}

// start runs one job, unless too many of it are already running.
func (e *Engine) start(ctx context.Context, r Run) {
	if !e.claim(r.Job) {
		e.logf("warn", "a scheduled job was skipped because it is still running",
			"job", r.Job.Name, "maxrunning", r.Job.MaxRunning)
		return
	}
	go func() {
		defer e.release(r.Job)
		if splay := r.Job.SplayFor(e.random); splay > 0 {
			e.logf("debug", "splaying a scheduled job", "job", r.Job.Name, "for", splay.String())
			select {
			case <-time.After(splay):
			case <-ctx.Done():
				return
			}
		}
		if err := e.Execute(ctx, r); err != nil {
			e.logf("warn", "a scheduled job failed", "job", r.Job.Name, "error", err.Error())
		}
	}()
}

// claim takes one of the job's `maxrunning` slots.
func (e *Engine) claim(job *Job) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running[job.Name] >= job.MaxRunning {
		return false
	}
	e.running[job.Name]++
	return true
}

func (e *Engine) release(job *Job) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running[job.Name] > 0 {
		e.running[job.Name]--
	}
}

// Running reports how many of a job are in flight, for a metric and for
// a test.
func (e *Engine) Running(name string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running[name]
}

// Describe renders a job for `schedule.list`.
func Describe(j *Job, now time.Time) *value.Map {
	out := value.NewMap(10)
	out.Set("function", j.Function)
	if len(j.Args) > 0 {
		args := make([]any, len(j.Args))
		for i, a := range j.Args {
			args[i] = a
		}
		out.Set("args", args)
	}
	if j.Kwargs != nil && j.Kwargs.Len() > 0 {
		out.Set("kwargs", j.Kwargs)
	}
	switch {
	case j.Cron != nil:
		out.Set("cron", j.Cron.Expr)
	case j.Interval > 0:
		out.Set("every", j.Interval.String())
	case !j.Once.IsZero():
		out.Set("once", j.Once.Format(time.RFC3339))
	case len(j.When) > 0:
		when := make([]any, len(j.When))
		for i, t := range j.When {
			when[i] = t.Format(time.RFC3339)
		}
		out.Set("when", when)
	}
	if j.Splay > 0 {
		out.Set("splay", j.Splay.String())
	}
	if j.MaxRunning != 1 {
		out.Set("maxrunning", int64(j.MaxRunning))
	}
	out.Set("return_job", j.ReturnJob)
	out.Set("enabled", j.Enabled)
	if j.Location != nil {
		out.Set("timezone", j.Location.String())
	}
	if j.Metadata != nil && j.Metadata.Len() > 0 {
		out.Set("metadata", j.Metadata)
	}
	if next, ok := j.Next(now); ok {
		out.Set("next_fire_time", next.Format(time.RFC3339))
	} else {
		out.Set("next_fire_time", nil)
	}
	return out
}
