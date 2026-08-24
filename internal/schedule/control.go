package schedule

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// The runtime management of SPEC section 20.1: `schedule.add`,
// `modify`, `delete`, `enable`, `disable`, `enable_job`, `disable_job`,
// `list`, `run_job`, `save`, and `reload`.

// List describes the configured jobs with the next time each fires.
func (e *Engine) List() *value.Map {
	e.mu.Lock()
	jobs := append([]*Job(nil), e.Jobs...)
	paused := e.paused
	e.mu.Unlock()

	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	now := e.now()
	out := value.NewMap(len(jobs))
	for _, j := range jobs {
		entry := Describe(j, now)
		if paused {
			// A held schedule reports every job as not firing, because
			// that is what is true, rather than reporting the time it
			// would have fired.
			entry.Set("enabled", false)
			entry.Set("next_fire_time", nil)
		}
		out.Set(j.Name, entry)
	}
	return out
}

// Add installs a job on a running node.
func (e *Engine) Add(name string, definition *value.Map) error {
	if name == "" {
		return fmt.Errorf("a scheduled job needs a name")
	}
	j, err := parseJob(name, definition, e.location())
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, existing := range e.Jobs {
		if existing.Name == name {
			return fmt.Errorf("%s is already scheduled; `schedule.modify` changes one", name)
		}
	}
	e.Jobs = append(e.Jobs, j)
	if next, ok := j.Next(e.now()); ok {
		e.markDue(name, next)
	}
	e.logf("info", "scheduled job added", "job", name)
	return nil
}

// Modify replaces a job's definition.
func (e *Engine) Modify(name string, definition *value.Map) error {
	j, err := parseJob(name, definition, e.location())
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, existing := range e.Jobs {
		if existing.Name != name {
			continue
		}
		if _, said := definition.Get("enabled"); !said {
			j.Enabled = existing.Enabled
		}
		// The last run carries over, so an interval job does not start
		// its clock again every time someone adjusts it.
		j.LastRun = existing.LastRun
		e.Jobs[i] = j
		if next, ok := j.Next(e.now()); ok {
			e.markDue(name, next)
		} else {
			delete(e.due, name)
		}
		e.logf("info", "scheduled job modified", "job", name)
		return nil
	}
	return fmt.Errorf("%s is not scheduled on this node", name)
}

// Delete removes a job.
func (e *Engine) Delete(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, j := range e.Jobs {
		if j.Name != name {
			continue
		}
		e.Jobs = append(e.Jobs[:i], e.Jobs[i+1:]...)
		delete(e.due, name)
		e.logf("info", "scheduled job deleted", "job", name)
		return nil
	}
	return fmt.Errorf("%s is not scheduled on this node", name)
}

// SetEnabled turns one job on or off, or the whole schedule when the
// name is empty.
func (e *Engine) SetEnabled(name string, on bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if name == "" {
		e.paused = !on
		e.logf("info", "schedule paused", "paused", e.paused)
		return nil
	}
	for _, j := range e.Jobs {
		if j.Name != name {
			continue
		}
		j.Enabled = on
		if next, ok := j.Next(e.now()); ok && on {
			e.markDue(name, next)
		} else {
			delete(e.due, name)
		}
		e.logf("info", "scheduled job enabled state changed", "job", name, "enabled", on)
		return nil
	}
	return fmt.Errorf("%s is not scheduled on this node", name)
}

// RunJob runs one job now, out of its turn, without disturbing when it
// next runs on its own.
func (e *Engine) RunJob(ctx context.Context, name string) error {
	e.mu.Lock()
	var found *Job
	for _, j := range e.Jobs {
		if j.Name == name {
			found = j
			break
		}
	}
	e.mu.Unlock()
	if found == nil {
		return fmt.Errorf("%s is not scheduled on this node", name)
	}
	if e.Execute == nil {
		return fmt.Errorf("this node has nowhere to send a scheduled job")
	}
	// Synchronously, and with no splay: an operator asking for it now
	// means now, and wants to see what it did.
	return e.Execute(ctx, Run{Job: found, Fire: e.now()})
}

// Replace swaps the whole schedule, which is what `schedule.reload`
// does after re-reading the files.
func (e *Engine) Replace(jobs []*Job) {
	e.mu.Lock()
	defer e.mu.Unlock()
	previous := map[string]time.Time{}
	for _, j := range e.Jobs {
		previous[j.Name] = j.LastRun
	}
	e.Jobs = jobs
	e.due = map[string]time.Time{}
	now := e.now()
	for _, j := range jobs {
		// A job that survived the reload keeps its last run, so an
		// interval job does not start its clock again because someone
		// edited an unrelated file.
		if last, ok := previous[j.Name]; ok {
			j.LastRun = last
		}
		if next, ok := j.Next(now); ok {
			e.due[j.Name] = next
		}
	}
	e.logf("info", "schedule reloaded", "jobs", len(jobs))
}

// Snapshot renders the running schedule in the shape the configuration
// file uses, which is what `schedule.save` writes.
func (e *Engine) Snapshot() *value.Map {
	e.mu.Lock()
	jobs := append([]*Job(nil), e.Jobs...)
	e.mu.Unlock()

	sort.Slice(jobs, func(i, j int) bool { return jobs[i].Name < jobs[j].Name })
	out := value.NewMap(len(jobs))
	for _, j := range jobs {
		entry := value.NewMap(10)
		entry.Set("function", j.Function)
		if len(j.Args) > 0 {
			args := make([]any, len(j.Args))
			for i, a := range j.Args {
				args[i] = a
			}
			entry.Set("args", args)
		}
		if j.Kwargs != nil && j.Kwargs.Len() > 0 {
			entry.Set("kwargs", j.Kwargs)
		}
		switch {
		case j.Cron != nil:
			entry.Set("cron", j.Cron.Expr)
		case j.Interval > 0:
			entry.Set("every", j.Interval.String())
		case !j.Once.IsZero():
			entry.Set("once", j.Once.Format("2006-01-02T15:04:05"))
		case len(j.When) > 0:
			when := make([]any, len(j.When))
			for i, t := range j.When {
				when[i] = t.Format("2006-01-02T15:04:05")
			}
			entry.Set("when", when)
		}
		if j.Splay > 0 {
			entry.Set("splay", int64(j.Splay.Seconds()))
		}
		if j.MaxRunning != 1 {
			entry.Set("maxrunning", int64(j.MaxRunning))
		}
		if !j.ReturnJob {
			entry.Set("return_job", false)
		}
		if j.RunOnStart {
			entry.Set("run_on_start", true)
		}
		if j.Catchup {
			entry.Set("catchup", true)
		}
		if !j.Enabled {
			entry.Set("enabled", false)
		}
		if j.Metadata != nil && j.Metadata.Len() > 0 {
			entry.Set("metadata", j.Metadata)
		}
		out.Set(j.Name, entry)
	}
	return out
}

// NextFireTime is when one job runs next.
func (e *Engine) NextFireTime(name string) (time.Time, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, j := range e.Jobs {
		if j.Name != name {
			continue
		}
		if e.paused {
			return time.Time{}, false, nil
		}
		next, ok := j.Next(e.now())
		return next, ok, nil
	}
	return time.Time{}, false, fmt.Errorf("%s is not scheduled on this node", name)
}

// markDue records a fire time. The caller holds the lock.
func (e *Engine) markDue(name string, at time.Time) {
	if e.due == nil {
		e.due = map[string]time.Time{}
	}
	e.due[name] = at
}

func (e *Engine) location() *time.Location {
	if e.Location != nil {
		return e.Location
	}
	return time.Local
}
