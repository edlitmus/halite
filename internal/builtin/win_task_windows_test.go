package builtin

import (
	"testing"

	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/wintask"
)

// What win_task.present enforces when a state leaves a field out, through
// the path a state really takes: the module's own argument defaults
// (taskFromArgs), then toWinTask, then wintask.SameDefinition.
//
// The owner decided (issue 892, DIVERGENCE 5.631.1) that an omitted field
// is not enforced on a task that already exists. That holds for run_as,
// the trigger, working_directory and description. It does not hold for
// run_level: the module fills an omitted run_level with "limited" before
// the comparison sees it, so SameDefinition's "omitted means not
// compared" rule never applies to it from a state, and a task running
// with highest privileges is rewritten to limited. wintask's own tests
// hand SameDefinition a struct directly and so cannot see the difference;
// this one goes through the arguments.
func TestWinTaskPresentEnforcesOnlySomeOmittedFields(t *testing.T) {
	// A task as the scheduler reads it back, differing from the creation
	// defaults in every field a state might omit.
	current := wintask.Task{
		Path:             `\halite-test\omitted`,
		Command:          "cmd.exe",
		Arguments:        "/c exit 0",
		WorkingDirectory: `C:\Windows\Temp`,
		RunAs:            "S-1-5-19", // LOCAL SERVICE, not the SYSTEM a creation would use
		RunLevel:         "highest",  // not the limited a creation would use
		Enabled:          true,
		Description:      "set by hand",
		Triggers:         []string{"at boot"},
	}
	base := []any{"name", current.Path, "command", "cmd.exe", "arguments", "/c exit 0"}

	tests := []struct {
		name    string
		extra   []any
		matches bool
	}{
		{"omitting every optional field still compares run_level as limited", nil, false},
		{"run_as, trigger, working_directory and description are not enforced when omitted",
			[]any{"run_level", "highest"}, true},
		{"a declared run_as that differs is enforced",
			[]any{"run_level", "highest", "run_as", "SYSTEM"}, false},
		{"a declared trigger that differs is enforced",
			[]any{"run_level", "highest", "trigger", "logon"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := value.MapOf(append(append([]any{}, base...), tt.extra...)...)

			got := wintask.SameDefinition(toWinTask(taskFromArgs(args)), current)

			if got != tt.matches {
				t.Errorf("SameDefinition = %v, want %v", got, tt.matches)
			}
		})
	}
}
