//go:build !windows

package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/exec"
)

// win_task off Windows.
//
// The signatures declare `platforms: windows`, so a call is refused
// before it reaches any of this. These exist so the module registers
// everywhere, which is what keeps "not written yet" and "you have
// mistyped it" different messages.

func notWindowsTask(function string) error {
	return fmt.Errorf("win_task.%s manages the Windows task scheduler, "+
		"and this node is not Windows", function)
}

func winTaskList(c *exec.Context) (any, error) { return nil, notWindowsTask("list") }

func winTaskInfo(c *exec.Context, name string) (any, error) { return nil, notWindowsTask("info") }

func winTaskExists(c *exec.Context, name string) (bool, error) {
	return false, notWindowsTask("exists")
}

func winTaskCreate(c *exec.Context, d taskDecl) error { return notWindowsTask("create") }

func winTaskDelete(c *exec.Context, name string) error { return notWindowsTask("delete") }

func winTaskRun(c *exec.Context, name string) error { return notWindowsTask("run") }

func winTaskStop(c *exec.Context, name string) error { return notWindowsTask("stop") }

func winTaskSetEnabled(c *exec.Context, name string, on bool) error {
	return notWindowsTask("set_enabled")
}

func winTaskMatches(c *exec.Context, d taskDecl) (bool, error) {
	return false, notWindowsTask("present")
}
