package builtin

import (
	"fmt"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/schedule"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// registerSchedule installs the scheduler management functions of SPEC
// section 20.1.
//
// `list` and `show_next_fire_time` answer from the configuration, which
// needs nothing running. The ones that change a running node's schedule
// need a handle on the engine and somewhere to write the change down,
// and say which phase that is rather than reporting a change nobody
// made.
func registerSchedule(r *Registries) {
	sig := func(function, doc string, params ...signature.Param) signature.Signature {
		return signature.Signature{
			Module: "schedule", Function: function, Doc: doc,
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "20.1",
			Params:   params,
			// A job definition is an open set of keys -- every timing
			// form and every modifier of SPEC 20.1 -- and an operator
			// types them inline: `schedule.add name=nightly
			// function=state.apply cron='17 3 * * *'`. Naming all of
			// them here would be a second copy of the parser's list,
			// and the parser is what refuses one it does not know.
			AnyKwargs: true,
		}
	}
	name := req("name", signature.String, "The scheduled job.")

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "schedule", Function: "list",
				Doc:      "The jobs this node runs, with the next time each would fire.",
				TestMode: signature.TestNotApplicable,
				Section:  "20.1",
			},
			Fn: listSchedule,
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "schedule", Function: "show_next_fire_time",
				Doc:      "When one scheduled job would next run.",
				TestMode: signature.TestNotApplicable,
				Section:  "20.1",
				Params:   []signature.Param{name},
			},
			Fn: showNextFireTime,
		},
		exec.Module{
			Sig: sig("add", "Add a job to a running node's schedule.",
				name,
				opt("job", signature.Map, nil, "The job definition."),
			),
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				return trueOr(ctl.Add(jobName(args), jobDefinition(args)))
			}),
		},
		exec.Module{
			Sig: sig("modify", "Change a running node's scheduled job.",
				name,
				opt("job", signature.Map, nil, "The job definition."),
			),
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				return trueOr(ctl.Modify(jobName(args), jobDefinition(args)))
			}),
		},
		exec.Module{
			Sig: sig("delete", "Remove a job from a running node's schedule.", name),
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				return trueOr(ctl.Delete(jobName(args)))
			}),
		},
		exec.Module{
			Sig: sig("enable", "Let the schedule run again."),
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				return trueOr(ctl.SetEnabled("", true))
			}),
		},
		exec.Module{
			Sig: sig("disable", "Hold the whole schedule without forgetting any job."),
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				return trueOr(ctl.SetEnabled("", false))
			}),
		},
		exec.Module{
			Sig: sig("enable_job", "Let one scheduled job run again.", name),
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				return trueOr(ctl.SetEnabled(jobName(args), true))
			}),
		},
		exec.Module{
			Sig: sig("disable_job", "Hold one scheduled job.", name),
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				return trueOr(ctl.SetEnabled(jobName(args), false))
			}),
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "schedule", Function: "run_job",
				Doc: "Run one scheduled job now, out of its turn and without splay. " +
					"When it next runs on its own is not disturbed.",
				Mutates: true,
				// The job runs whatever function it names, and this
				// build cannot know whether that function honours test
				// mode.
				TestMode:      signature.TestUnreliable,
				ArbitraryCode: true,
				Section:       "20.1",
				Params:        []signature.Param{name},
			},
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				if c.Test {
					return value.MapOf("would_run", jobName(args)), nil
				}
				return trueOr(ctl.RunJob(c.Ctx, jobName(args)))
			}),
		},
		exec.Module{
			Sig: sig("save",
				"Write the running schedule to disk, so it survives a restart. It is "+
					"written to a file of the node's own under schedule.d, never over "+
					"what a package manager put there."),
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				if c.SaveConfig == nil {
					return nil, fmt.Errorf("this node has nowhere to write its schedule")
				}
				if c.Test {
					return value.MapOf("would_save", int64(ctl.Snapshot().Len())), nil
				}
				path, err := c.SaveConfig("schedule", ctl.Snapshot())
				if err != nil {
					return nil, err
				}
				return value.MapOf("saved", path), nil
			}),
		},
		exec.Module{
			Sig: sig("reload", "Re-read the schedule from disk, discarding runtime changes that were never saved."),
			Fn: withSchedule(func(c *exec.Context, args *value.Map, ctl exec.ScheduleControl) (any, error) {
				if c.ReloadConfig == nil {
					return nil, fmt.Errorf("this node cannot re-read its schedule")
				}
				if c.Test {
					return true, nil
				}
				return trueOr(c.ReloadConfig("schedule"))
			}),
		},
	)
}

// withSchedule refuses a management call on a node that is not running
// a schedule, rather than reporting a change nobody made.
func withSchedule(fn func(*exec.Context, *value.Map, exec.ScheduleControl) (any, error)) exec.Func {
	return func(c *exec.Context, args *value.Map) (any, error) {
		if c.Schedule == nil {
			return nil, fmt.Errorf(
				"this node is not running a schedule, so there is nothing to change; " +
					"`schedule` in the configuration is what sets it")
		}
		return fn(c, args, c.Schedule)
	}
}

func trueOr(err error) (any, error) {
	if err != nil {
		return nil, err
	}
	return true, nil
}

func jobName(args *value.Map) string {
	return value.KeyString(mustArg(args, "name"))
}

// jobDefinition reads the definition a caller passed, accepting it
// under `job` or as the remaining keyword arguments — which is how an
// operator types it: `schedule.add name=nightly function=state.apply
// cron='17 3 * * *'`.
func jobDefinition(args *value.Map) *value.Map {
	if raw, ok := args.Get("job"); ok && raw != nil {
		if m, ok := raw.(*value.Map); ok {
			return m
		}
	}
	out := value.NewMap(args.Len())
	for _, e := range args.Entries() {
		key := value.KeyString(e.Key)
		if key == "name" || key == "job" || e.Val == nil {
			continue
		}
		out.Set(key, e.Val)
	}
	return out
}

// scheduleJobs reads what this node is configured to run.
func scheduleJobs(c *exec.Context) ([]*schedule.Job, error) {
	raw, ok := c.Config.Get("schedule")
	if !ok || raw == nil {
		return nil, nil
	}
	loc := time.Local
	if name := configString(c, "timezone"); name != "" {
		found, err := time.LoadLocation(name)
		if err != nil {
			return nil, err
		}
		loc = found
	}
	return schedule.Parse(raw, loc)
}

func configString(c *exec.Context, key string) string {
	v, ok := c.Config.Get(key)
	if !ok || v == nil {
		return ""
	}
	return value.KeyString(v)
}

func listSchedule(c *exec.Context, args *value.Map) (any, error) {
	// A running node answers from its engine, so a job added since
	// startup is in the list.
	if c.Schedule != nil {
		return c.Schedule.List(), nil
	}
	jobs, err := scheduleJobs(c)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := value.NewMap(len(jobs))
	for _, j := range jobs {
		out.Set(j.Name, schedule.Describe(j, now))
	}
	return out, nil
}

func showNextFireTime(c *exec.Context, args *value.Map) (any, error) {
	name := value.KeyString(mustArg(args, "name"))
	if c.Schedule != nil {
		next, ok, err := c.Schedule.NextFireTime(name)
		if err != nil {
			return nil, err
		}
		out := value.NewMap(2)
		out.Set("name", name)
		if !ok {
			out.Set("next_fire_time", nil)
			return out, nil
		}
		out.Set("next_fire_time", next.Format(time.RFC3339))
		return out, nil
	}
	jobs, err := scheduleJobs(c)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Name != name {
			continue
		}
		next, ok := j.Next(time.Now())
		out := value.NewMap(2)
		out.Set("name", name)
		if !ok {
			// A job that will not fire again is a fact, not a failure:
			// `until` has passed, or it is disabled, or it was `once`.
			out.Set("next_fire_time", nil)
			return out, nil
		}
		out.Set("next_fire_time", next.Format(time.RFC3339))
		return out, nil
	}
	return nil, fmt.Errorf("this node has no scheduled job called %q", name)
}
