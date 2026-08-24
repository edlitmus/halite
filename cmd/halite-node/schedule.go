package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/job"
	hlog "github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/schedule"
	"github.com/edlitmus/halite/internal/value"
)

// startSchedule runs the scheduled jobs of SPEC section 20 for as long
// as the node is running.
//
// It runs whether or not the node has a hub: a schedule is how a node
// keeps itself converged, and the case where that matters most is the
// one where the hub cannot be reached.
func (n *node) startSchedule(ctx context.Context) {
	raw, err := n.scheduleConfig()
	if err != nil {
		cli.Fatalf("%v", err)
	}
	if raw == nil {
		return
	}
	loc, err := n.scheduleLocation()
	if err != nil {
		cli.Fatalf("%v", err)
	}
	n.openReturner(nil)
	jobs, err := schedule.Parse(raw, loc)
	if err != nil {
		// A schedule that will not parse stops the node rather than
		// starting one whose jobs silently never run. A schedule is
		// written once and trusted for years.
		cli.Fatalf("%v", err)
	}
	if len(jobs) == 0 {
		return
	}

	engine := &schedule.Engine{
		Jobs:     jobs,
		Location: loc,
		Execute:  n.runScheduled,
		Log: func(level, msg string, kv ...any) {
			lv, _ := hlog.ParseLevel(level)
			n.log.Log(lv, msg, append([]any{"component", "schedule"}, kv...)...)
		},
	}
	names := make([]string, 0, len(jobs))
	for _, j := range jobs {
		names = append(names, j.Name)
	}
	n.schedule = engine
	// `schedule.reload` re-reads the files and replaces the running
	// set, which is how a runtime change made and not saved is
	// discarded deliberately.
	n.reloadSchedule = func() error {
		raw, err := n.scheduleConfig()
		if err != nil {
			return err
		}
		loc, err := n.scheduleLocation()
		if err != nil {
			return err
		}
		reloaded, err := schedule.Parse(raw, loc)
		if err != nil {
			return err
		}
		engine.Replace(reloaded)
		return nil
	}
	n.log.Info("schedule started", "jobs", names)
	go func() {
		if err := engine.Run(ctx); err != nil {
			n.log.Error("the schedule stopped", "error", err.Error())
		}
	}()
}

// scheduleConfig is `schedule` from the node configuration, merged with
// every fragment in `schedule.d`.
//
// SPEC 20.1 names three sources: the configuration file, that
// directory, and pillar. The directory is also where the node writes
// its own runtime changes.
func (n *node) scheduleConfig() (any, error) {
	base, _ := n.cfg.Get("schedule")
	dropIns, files, err := config.LoadDefinitions(n.scheduleDir(), "schedule")
	if err != nil {
		return nil, err
	}
	if dropIns == nil {
		return base, nil
	}
	if len(files) > 0 {
		n.log.Info("schedule fragments read", "files", files)
	}
	if base == nil {
		return dropIns, nil
	}
	return value.Merge(base, dropIns, value.MergeOpts{}), nil
}

func (n *node) scheduleDir() string {
	return filepath.Join(n.root, "schedule.d")
}

// scheduleLocation is the time zone schedules evaluate in.
//
// The node's local zone by default, per SPEC 20.1, with `timezone` in
// the configuration overriding it and each job able to override that.
// Go's embedded database means the node needs no tzdata package.
func (n *node) scheduleLocation() (*time.Location, error) {
	name := n.cfg.String("timezone", "")
	if name == "" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

// runScheduled performs one scheduled job as a local execution.
//
// It goes through the same path a job from the hub takes, so a
// scheduled `state.apply` and a driven one are the same run: the same
// pillar, the same modules, the same return schema.
func (n *node) runScheduled(ctx context.Context, r schedule.Run) error {
	kwargs := map[string]any{}
	if r.Job.Kwargs != nil {
		for _, e := range r.Job.Kwargs.Entries() {
			kwargs[value.KeyString(e.Key)] = e.Val
		}
	}
	j := &job.Job{
		JID:     job.ID(newJobID()),
		Fun:     r.Job.Function,
		Arg:     r.Job.Args,
		Kwarg:   kwargs,
		Created: time.Now(),
		// The schedule is the submitter, named so that a return in the
		// job cache says what asked for it rather than looking like an
		// operator did.
		Submitter: "schedule:" + r.Job.Name,
	}

	n.log.Info("running a scheduled job",
		"job", r.Job.Name, "fun", r.Job.Function, "jid", string(j.JID),
		"due", r.Fire.Format(time.RFC3339))

	ret := n.executeJob(j)
	if r.Job.ReturnJob {
		// Wherever `returner:` says. The default is `local`, which is
		// append-only NDJSON on the node -- not the hub's job cache,
		// because the hub refuses a return for a job it never
		// dispatched, and that is the right refusal.
		if err := n.returns.Return(context.Background(), ret); err != nil {
			n.log.Warn("could not record a scheduled job's return",
				"job", r.Job.Name, "jid", string(j.JID),
				"returner", n.returns.Name(), "error", err.Error())
		}
	}
	if !ret.Success {
		return errScheduledFailure
	}
	return nil
}

var errScheduledFailure = errString("the scheduled job reported a failure")

type errString string

func (e errString) Error() string { return string(e) }
