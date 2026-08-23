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
	notYet := func(function, doc string) exec.Module {
		return exec.Module{
			Sig: signature.Signature{
				Module: "schedule", Function: function, Doc: doc,
				AnyKwargs: true,
				TestMode:  signature.TestNotApplicable,
				Section:   "20.1",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return nil, fmt.Errorf(
					"schedule.%s changes a running node's schedule, which arrives with "+
						"the runtime management of SPEC section 20.1; `schedule` in the "+
						"configuration is what sets it today", function)
			},
		}
	}

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "schedule", Function: "list",
				Doc: "The jobs this node is configured to run, with the next time each " +
					"would fire.",
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
				Params: []signature.Param{
					req("name", signature.String, "The scheduled job."),
				},
			},
			Fn: showNextFireTime,
		},
		notYet("add", "Add a job to a running node's schedule."),
		notYet("modify", "Change a running node's scheduled job."),
		notYet("delete", "Remove a job from a running node's schedule."),
		notYet("enable", "Enable the schedule on a running node."),
		notYet("disable", "Disable the schedule on a running node."),
		notYet("enable_job", "Enable one scheduled job on a running node."),
		notYet("disable_job", "Disable one scheduled job on a running node."),
		notYet("run_job", "Run one scheduled job now, out of its turn."),
		notYet("save", "Write the running schedule to disk."),
		notYet("reload", "Re-read the schedule from disk."),
	)
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
