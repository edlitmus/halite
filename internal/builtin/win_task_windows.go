package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/wintask"
)

// The Windows half of win_task. internal/wintask holds the scheduler
// work; this is the translation between it and a module's arguments and
// return values.

// toWinTask turns a declaration into the scheduler's own shape.
func toWinTask(d taskDecl) wintask.Task {
	t := wintask.Task{
		Path:             d.Path,
		Command:          d.Command,
		Arguments:        d.Arguments,
		WorkingDirectory: d.WorkingDirectory,
		RunAs:            d.RunAs,
		RunLevel:         d.RunLevel,
		Enabled:          d.Enabled,
		Description:      d.Description,
		XML:              d.XML,
	}
	if d.Trigger != "" {
		t.Triggers = []string{d.Trigger}
	}
	return t
}

func winTaskList(c *exec.Context) (any, error) {
	names, err := wintask.List(c)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, n)
	}
	return out, nil
}

func winTaskInfo(c *exec.Context, name string) (any, error) {
	if name == "" {
		return nil, fmt.Errorf("win_task.info needs a task name")
	}
	t, err := wintask.Info(c, name)
	if err != nil {
		return nil, err
	}
	triggers := make([]any, 0, len(t.Triggers))
	for _, s := range t.Triggers {
		triggers = append(triggers, s)
	}
	return value.MapOf(
		"name", t.Path,
		"command", t.Command,
		"arguments", t.Arguments,
		"working_directory", t.WorkingDirectory,
		"run_as", t.RunAs,
		"run_level", t.RunLevel,
		"enabled", t.Enabled,
		"description", t.Description,
		"author", t.Author,
		"triggers", triggers,
		"xml", t.XML,
	), nil
}

func winTaskExists(c *exec.Context, name string) (bool, error) {
	if name == "" {
		return false, fmt.Errorf("win_task needs a task name")
	}
	return wintask.Exists(c, name)
}

func winTaskCreate(c *exec.Context, d taskDecl) error {
	if d.Path == "" {
		return fmt.Errorf("win_task.create needs a task name")
	}
	return wintask.Create(c, toWinTask(d))
}

func winTaskDelete(c *exec.Context, name string) error {
	if name == "" {
		return fmt.Errorf("win_task.delete needs a task name")
	}
	return wintask.Delete(c, name)
}

func winTaskRun(c *exec.Context, name string) error {
	if name == "" {
		return fmt.Errorf("win_task.run needs a task name")
	}
	return wintask.Run(c, name)
}

func winTaskStop(c *exec.Context, name string) error {
	if name == "" {
		return fmt.Errorf("win_task.stop needs a task name")
	}
	return wintask.Stop(c, name)
}

func winTaskSetEnabled(c *exec.Context, name string, on bool) error {
	if name == "" {
		return fmt.Errorf("win_task.set_enabled needs a task name")
	}
	return wintask.SetEnabled(c, name, on)
}

// winTaskMatches reports whether the host's task is what the state
// declared.
//
// A declaration given as XML is compared as XML, because that is what
// the state said: the fields the summary carries are a projection, and
// two definitions that project the same are not necessarily the same
// definition.
func winTaskMatches(c *exec.Context, d taskDecl) (bool, error) {
	current, err := wintask.Info(c, d.Path)
	if err != nil {
		return false, err
	}
	if d.XML != "" {
		return wintask.SameXML(d.XML, current.XML), nil
	}
	return wintask.SameDefinition(toWinTask(d), current), nil
}
