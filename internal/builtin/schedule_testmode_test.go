package builtin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// recordingSchedule counts every call that would change something.
type recordingSchedule struct{ changes int }

func (s *recordingSchedule) List() *value.Map { return value.NewMap(0) }
func (s *recordingSchedule) Add(string, *value.Map) error {
	s.changes++
	return nil
}

func (s *recordingSchedule) Modify(string, *value.Map) error {
	s.changes++
	return nil
}

func (s *recordingSchedule) Delete(string) error {
	s.changes++
	return nil
}

func (s *recordingSchedule) SetEnabled(string, bool) error {
	s.changes++
	return nil
}

func (s *recordingSchedule) RunJob(context.Context, string) error {
	s.changes++
	return nil
}
func (s *recordingSchedule) Snapshot() *value.Map { return value.NewMap(0) }
func (s *recordingSchedule) NextFireTime(string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

// Every schedule function that says it honours `--test` must honour it.
//
// Seven of them did not: `add`, `modify`, `delete` and the four
// enable/disable pairs changed a running node's schedule under a dry run
// while their signature declared `TestReliable` and docs/modules.md
// printed "honours `--test`" beside each. A dry run that acts is worse
// than one that fails, because the operator has been told it did nothing.
func TestEverySchedulingFunctionHonoursTestMode(t *testing.T) {
	r := New()
	// `job` only where the function takes it: the registry validates
	// arguments, and a function refused before it runs would pass this
	// check by not running at all.
	definition := value.MapOf("function", "cmd.run", "cron", "17 3 * * *")

	for _, name := range r.Exec.Signatures().Names() {
		if !strings.HasPrefix(name, "schedule.") {
			continue
		}
		sig, _ := r.Exec.Signatures().Lookup(name)
		if !sig.Mutates {
			continue
		}
		t.Run(name, func(t *testing.T) {
			ctl := &recordingSchedule{}
			c := newCtx(true)
			c.Schedule = ctl
			c.SaveConfig = func(string, *value.Map) (string, error) {
				ctl.changes++
				return "/tmp/schedule.yaml", nil
			}
			c.ReloadConfig = func(string) error {
				ctl.changes++
				return nil
			}
			args := value.MapOf("name", "nightly")
			if _, takesJob := sig.Param("job"); takesJob {
				args.Set("job", definition)
			}
			if _, err := r.Exec.Call(c, name, args); err != nil {
				t.Fatalf("%s under test mode: %v", name, err)
			}
			if ctl.changes != 0 {
				t.Errorf("%s changed the schedule %d time(s) under `--test`, "+
					"and its signature says it honours it", name, ctl.changes)
			}
		})
	}
}
