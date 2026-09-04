package builtin

import (
	"fmt"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// win_task, SPEC sections 15.3 and 15.5.
//
// The scheduler is reached through schtasks.exe and the scheduler's own
// XML, which is the opposite of the choice win_service made and is worth
// saying out loud. The service control manager has no machine-readable
// output mode — sc.exe writes a table and its status words are
// localised — so parsing it would mean parsing prose. The task scheduler
// has one: `schtasks /query /xml` emits a published schema whose element
// names are fixed in every locale, and `/create /xml` takes it back.
// That is SPEC 15.2's stated standard for reaching a subsystem through
// its binary.
//
// A task's schedule is declared in a small vocabulary — `boot`,
// `logon`, `daily at HH:MM`, `once at HH:MM` — and anything the
// scheduler can express but that vocabulary cannot is declared by
// handing the state the XML itself. That is the seam: the common case
// is short, and the uncommon one is possible rather than approximated.

func registerWinTask(r *Registries) {
	taskName := req("name", signature.String,
		`The task, including its folder: \halite\nightly.`)

	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "list",
				Doc:       "Name every scheduled task on this node, sorted.",
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winTaskList(c)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "info",
				Doc:    "Return what the scheduler holds for one task.",
				Params: []signature.Param{taskName},
				Returns: "a mapping with command, arguments, working_directory, run_as, " +
					"run_level, enabled, description, triggers and xml",
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winTaskInfo(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "exists",
				Doc:       "Report whether a task is there.",
				Params:    []signature.Param{taskName},
				TestMode:  signature.TestNotApplicable,
				Platforms: winOnly,
				Section:   "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return winTaskExists(c, states.Str(args, "name", ""))
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "create",
				Doc: "Register a task, replacing one of the same name.",
				Params: []signature.Param{
					taskName,
					opt("command", signature.String, "", "The program to run."),
					opt("arguments", signature.String, "", "Its arguments."),
					opt("working_directory", signature.String, "", "Where it runs."),
					opt("run_as", signature.String, "",
						"The account. Empty is SYSTEM, so that a task registered by a "+
							"service and one registered by an operator are the same task."),
					choice("run_level", "limited", "Whether it runs elevated.",
						"limited", "highest"),
					opt("enabled", signature.Bool, true, "Whether the scheduler will run it."),
					opt("description", signature.String, "", "What the console shows."),
					opt("trigger", signature.String, "",
						"When it runs: `boot`, `logon`, `daily at HH:MM` or `once at HH:MM`. "+
							"Empty registers a task that runs only when something asks it to."),
					opt("xml", signature.String, "",
						"The scheduler's own definition, for a task this vocabulary cannot "+
							"express. Everything else is ignored when this is given."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				if err := winTaskCreate(c, taskFromArgs(args)); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "delete",
				Doc:        "Remove a task.",
				Params:     []signature.Param{taskName},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				if err := winTaskDelete(c, states.Str(args, "name", "")); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "run",
				Doc:        "Start a task now, out of its schedule.",
				Params:     []signature.Param{taskName},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				if err := winTaskRun(c, states.Str(args, "name", "")); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "stop",
				Doc:        "End a task that is running.",
				Params:     []signature.Param{taskName},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				if err := winTaskStop(c, states.Str(args, "name", "")); err != nil {
					return nil, err
				}
				return true, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "set_enabled",
				Doc: "Turn a task on or off without changing anything else about it.",
				Params: []signature.Param{
					taskName,
					req("enabled", signature.Bool, "Whether the scheduler will run it."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.3",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				if c.Test {
					return true, nil
				}
				err := winTaskSetEnabled(c, states.Str(args, "name", ""),
					states.Bool(args, "enabled", true))
				if err != nil {
					return nil, err
				}
				return true, nil
			},
		},
	)

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "present",
				Doc: "Ensure a scheduled task is registered as declared.",
				Params: []signature.Param{
					nameParam(`The task, including its folder. Defaults to the state ID.`),
					opt("command", signature.String, "", "The program to run."),
					opt("arguments", signature.String, "", "Its arguments."),
					opt("working_directory", signature.String, "", "Where it runs."),
					opt("run_as", signature.String, "", "The account. Empty is SYSTEM."),
					choice("run_level", "limited", "Whether it runs elevated.",
						"limited", "highest"),
					opt("enabled", signature.Bool, true, "Whether the scheduler will run it."),
					opt("description", signature.String, "", "What the console shows."),
					opt("trigger", signature.String, "",
						"When it runs: `boot`, `logon`, `daily at HH:MM` or `once at HH:MM`."),
					opt("xml", signature.String, "",
						"The scheduler's own definition, for a task this vocabulary cannot express."),
				},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.5",
			},
			Fn: winTaskPresent,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "win_task", Function: "absent",
				Doc:        "Ensure a scheduled task is not registered.",
				Params:     []signature.Param{nameParam("The task. Defaults to the state ID.")},
				Mutates:    true,
				TestMode:   signature.TestReliable,
				Privileges: []string{"root"},
				Platforms:  winOnly,
				Section:    "15.5",
			},
			Fn: winTaskAbsent,
		},
	)
}

// taskDecl is what a state or a call declared, before it reaches the
// platform. A plain struct rather than the wintask type, so that the
// module registration compiles on every platform.
type taskDecl struct {
	Path             string
	Command          string
	Arguments        string
	WorkingDirectory string
	RunAs            string
	RunLevel         string
	Enabled          bool
	Description      string
	Trigger          string
	XML              string
}

func taskFromArgs(args *value.Map) taskDecl {
	name := states.Str(args, "name", "")
	return taskDecl{
		Path:             name,
		Command:          states.Str(args, "command", ""),
		Arguments:        states.Str(args, "arguments", ""),
		WorkingDirectory: states.Str(args, "working_directory", ""),
		RunAs:            states.Str(args, "run_as", ""),
		RunLevel:         states.Str(args, "run_level", "limited"),
		Enabled:          states.Bool(args, "enabled", true),
		Description:      states.Str(args, "description", ""),
		Trigger:          states.Str(args, "trigger", ""),
		XML:              states.Str(args, "xml", ""),
	}
}

// winTaskPresent ensures a task is registered as declared.
//
// The whole definition is replaced rather than edited, because that is
// the only thing schtasks offers and because a declaration is a
// statement about the whole task. What is *compared* is only what the
// state stated: a task read back carries forty settings the declaration
// never mentioned, and comparing them all would make every run report a
// change.
func winTaskPresent(c *exec.Context, args *value.Map) (states.Result, error) {
	decl := taskFromArgs(args)
	if decl.Path == "" {
		return states.False("win_task.present needs a task name."), nil
	}
	if decl.Command == "" && decl.XML == "" {
		return states.False(fmt.Sprintf(
			"%s needs a command to run, or the scheduler's own XML.", decl.Path)), nil
	}

	present, err := winTaskExists(c, decl.Path)
	if err != nil {
		return states.False(fmt.Sprintf("The scheduler could not be read: %v", err)), nil
	}

	if present {
		same, describeErr := winTaskMatches(c, decl)
		if describeErr != nil {
			return states.False(fmt.Sprintf("%s could not be read: %v", decl.Path, describeErr)), nil
		}
		if same {
			return states.True(fmt.Sprintf("%s is already registered as declared.", decl.Path)), nil
		}
	}

	verb := "registered"
	if present {
		verb = "replaced"
	}
	changes := value.NewMap(1)
	changes.Set("task", states.Change(taskWas(present), "as declared"))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be %s.", decl.Path, verb), changes), nil
	}
	if err := winTaskCreate(c, decl); err != nil {
		return states.False(fmt.Sprintf("%s could not be %s: %v", decl.Path, verb, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was %s.", decl.Path, verb), changes), nil
}

func taskWas(present bool) string {
	if present {
		return "registered differently"
	}
	return "absent"
}

// winTaskAbsent ensures a task is not registered.
func winTaskAbsent(c *exec.Context, args *value.Map) (states.Result, error) {
	name := states.Str(args, "name", "")
	if name == "" {
		return states.False("win_task.absent needs a task name."), nil
	}
	present, err := winTaskExists(c, name)
	if err != nil {
		return states.False(fmt.Sprintf("The scheduler could not be read: %v", err)), nil
	}
	if !present {
		return states.True(fmt.Sprintf("%s is not registered.", name)), nil
	}

	changes := value.NewMap(1)
	changes.Set("task", states.Change("registered", "absent"))
	if c.Test {
		return states.WouldChange(fmt.Sprintf("%s would be removed.", name), changes), nil
	}
	if err := winTaskDelete(c, name); err != nil {
		return states.False(fmt.Sprintf("%s could not be removed: %v", name, err)), nil
	}
	return states.Changed(fmt.Sprintf("%s was removed.", name), changes), nil
}
