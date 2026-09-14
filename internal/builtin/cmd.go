package builtin

import (
	"fmt"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerCmd installs the cmd module.
//
// Default execution is **through a shell**, as Salt's is, so an existing
// tree's `cmd.run` call sites mean what they meant. `cmd_default_shell:
// false` takes an argument vector instead, and is the hardened setting
// an estate moves to once its call sites are quoted or converted to
// `name` plus `args`. SPEC section 15.2.
func registerCmd(r *Registries) {
	cmdParams := func(extra ...signature.Param) []signature.Param {
		base := []signature.Param{
			req("name", signature.String, "The program to run, or the shell line when shell is true."),
			opt("args", signature.List, nil, "Arguments to the program, when not using a shell."),
			// The declared default is nil rather than false so that an
			// omitted `shell` stays absent from the bound arguments and
			// can fall back to the `cmd_default_shell` setting, while an
			// explicit `shell: false` still wins over it.
			//
			// The type is `any` because the argument carries two
			// spellings that do not overlap. See shellRequest.
			opt("shell", signature.Any, nil, "Which shell, or whether: `true` for the platform's own, the path of one — Salt's `shell: /bin/bash` — or `false` for none, in which case `name` is the program and `args` its arguments. On by default, or per cmd_default_shell; see SPEC section 15.2."),
			opt("cwd", signature.Path, "", "Working directory."),
			opt("runas", signature.String, "", "Account to run as, applied with setuid and setgid."),
			opt("umask", signature.String, "", "Umask for the child."),
			opt("env", signature.Map, nil, "Environment for the child, replacing the clean default."),
			opt("stdin", signature.String, "", "Text written to the process."),
			opt("timeout", signature.Duration, nil, "How long the command may run."),
			opt("ignore_retcode", signature.Bool, false, "Treat a non-zero exit as success."),
		}
		return append(base, extra...)
	}

	// defaultShell is `cmd_default_shell`, and it is on.
	//
	// A `cmd.run` that says nothing runs through a shell, which is what
	// Salt does and what an existing tree is written against. This build
	// shipped the inverse for most of its life -- an argument vector
	// unless a state opted in -- on the reasoning that a shell line is
	// re-interpreted by the shell and that most of Salt's injection
	// findings begin there. The reasoning still holds and the default no
	// longer follows it: a tree carried over from Salt has every one of
	// its `cmd.run` call sites written as a shell line, and a default
	// that silently reads those as program names is a migration hazard
	// of its own -- one that fails loudly at best and, where a program
	// of that name exists, quietly runs the wrong thing.
	//
	// `cmd_default_shell: false` is the hardened setting now, and it is
	// the one an estate moves to once its call sites are quoted or
	// converted to `name` plus `args`. A single state still opts either
	// way with `shell:`.
	defaultShell := func(c *exec.Context) bool {
		if c == nil || c.Config == nil {
			return true
		}
		v, ok := c.Config.Get("cmd_default_shell")
		if !ok {
			return true
		}
		return value.Truthy(v)
	}

	// shellRequest reads the `shell` argument, which carries two
	// spellings.
	//
	// SPEC 15.2 spells it `shell: true` -- a boolean opting into a
	// shell, the platform's own. Salt spells it `shell: /bin/bash` -- a
	// path naming which shell to use, which necessarily opts in as well,
	// since choosing an interpreter is choosing to have one. The two do
	// not overlap and both are accepted, so a tree carried over from
	// Salt keeps its meaning and one written to the spec keeps its own.
	//
	// The path is honoured rather than merely tolerated. `shell:
	// /bin/bash` in an existing tree is a request for bash's syntax, and
	// accepting the argument while running the line under /bin/sh would
	// be a different program silently -- the shape of the `cloud_grains`
	// defect in 5.78, where a setting was read and then dropped.
	//
	// `use` is whether to run through a shell at all; `path` is which,
	// empty meaning the platform's own.
	shellRequest := func(c *exec.Context, args *value.Map) (use bool, path string, err error) {
		v, ok := args.Get("shell")
		if !ok || v == nil {
			return defaultShell(c), "", nil
		}
		switch t := v.(type) {
		case bool:
			return t, "", nil
		case string:
			if strings.TrimSpace(t) == "" {
				return defaultShell(c), "", nil
			}
			return true, t, nil
		default:
			return false, "", fmt.Errorf(
				"`shell` must be true, false, or the path of a shell such as `/bin/bash`, found %s. "+
					"SPEC section 15.2", value.TypeName(v))
		}
	}

	// argvForm reports whether a declaration gave `args`, which is how a
	// state says it has been converted to an argument vector.
	argvForm := func(args *value.Map) bool {
		v, ok := args.Get("args")
		if !ok || v == nil {
			return false
		}
		list, ok := v.([]any)
		return ok && len(list) > 0
	}

	// conflictingShell reports the contradiction of asking for a shell
	// line and supplying argument-vector arguments in the same state.
	conflictingShell := func(args *value.Map) bool {
		v, ok := args.Get("shell")
		if !ok || v == nil {
			return false
		}
		if s, isStr := v.(string); isStr {
			return strings.TrimSpace(s) != "" && argvForm(args)
		}
		return value.Truthy(v) && argvForm(args)
	}

	build := func(c *exec.Context, args *value.Map) exec.Command {
		use, shellPath, _ := shellRequest(c, args)
		cmd := exec.Command{
			Shell:          use,
			ShellPath:      shellPath,
			Dir:            states.Str(args, "cwd", ""),
			RunAs:          states.Str(args, "runas", ""),
			Umask:          states.Str(args, "umask", ""),
			Stdin:          states.Str(args, "stdin", ""),
			IgnoreExitCode: true,
		}
		name := states.Str(args, "name", "")
		// A state that supplies `args` is in argument-vector form, and
		// says so. `cmd_default_shell` is a default for states that do
		// not say, so it does not apply here: applying it dropped the
		// arguments silently, and appending them to a shell line would
		// hand the shell strings the author had already separated from
		// it. An explicit `shell: true` beside `args` is a contradiction
		// and is refused by the caller.
		if cmd.Shell && !argvForm(args) {
			cmd.Argv = []string{name}
		} else {
			cmd.Shell = false
			cmd.ShellPath = ""
			cmd.Argv = append([]string{name}, states.Strings(args, "args")...)
		}
		if envMap := states.Mapping(args, "env"); envMap != nil {
			env := exec.CleanEnv()
			for _, e := range envMap.Entries() {
				env = append(env, value.KeyString(e.Key)+"="+value.KeyString(e.Val))
			}
			cmd.Env = env
		}
		if v, ok := args.Get("timeout"); ok && v != nil {
			if d, err := durationOf(v); err == nil {
				cmd.Timeout = d
			}
		}
		return cmd
	}

	runAll := func(c *exec.Context, args *value.Map) (*value.Map, error) {
		if _, _, err := shellRequest(c, args); err != nil {
			return nil, err
		}
		if conflictingShell(args) {
			return nil, fmt.Errorf("this state asks for a shell and also gives `args`; " +
				"a shell line is one string and an argument vector is a list, so pick one. SPEC section 15.2")
		}
		cmd := build(c, args)
		if cmd.Shell {
			c.Logf("warn", "cmd is running through a shell: %s", cmd.String())
		}
		res, err := c.Run(cmd)
		if err != nil {
			return nil, err
		}
		return value.MapOf(
			"stdout", trimOutput(res.Stdout),
			"stderr", trimOutput(res.Stderr),
			"retcode", int64(res.Code),
			"pid", int64(0),
		), nil
	}

	arbitrary := func(module, function, doc string, params []signature.Param) signature.Signature {
		return signature.Signature{
			Module: module, Function: function, Doc: doc, Params: params,
			Mutates:       true,
			ArbitraryCode: true,
			TestMode:      signature.TestUnreliable,
			Privileges:    []string{"whatever the command needs"},
			Section:       "15.2",
		}
	}

	r.Exec.Add(
		exec.Module{
			Sig: arbitrary("cmd", "run_all", "Run a command and return its stdout, stderr, and exit status.", cmdParams()),
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return runAll(c, args)
			},
		},
		exec.Module{
			Sig: arbitrary("cmd", "run", "Run a command and return its stdout.", cmdParams()),
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				out, err := runAll(c, args)
				if err != nil {
					return nil, err
				}
				code, _ := out.Get("retcode")
				if n, _ := code.(int64); n != 0 && !states.Bool(args, "ignore_retcode", false) {
					stderr, _ := out.Get("stderr")
					return nil, fmt.Errorf("command exited %d: %s", n, firstLine(value.KeyString(stderr)))
				}
				v, _ := out.Get("stdout")
				return v, nil
			},
		},
		exec.Module{
			Sig: arbitrary("cmd", "run_stdout", "Run a command and return its stdout, whatever the exit status.", cmdParams()),
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				out, err := runAll(c, args)
				if err != nil {
					return nil, err
				}
				v, _ := out.Get("stdout")
				return v, nil
			},
		},
		exec.Module{
			Sig: arbitrary("cmd", "run_stderr", "Run a command and return its stderr.", cmdParams()),
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				out, err := runAll(c, args)
				if err != nil {
					return nil, err
				}
				v, _ := out.Get("stderr")
				return v, nil
			},
		},
		exec.Module{
			Sig: arbitrary("cmd", "retcode", "Run a command and return only its exit status.", cmdParams()),
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				out, err := runAll(c, args)
				if err != nil {
					return nil, err
				}
				v, _ := out.Get("retcode")
				return v, nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "cmd", Function: "which",
				Doc:      "Report the path of a program, or an empty string.",
				Params:   []signature.Param{req("name", signature.String, "The program to look for.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return c.Which(states.Str(args, "name", "")), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "cmd", Function: "has_exec",
				Doc:      "Report whether a program is on the path.",
				Params:   []signature.Param{req("name", signature.String, "The program to look for.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return c.Which(states.Str(args, "name", "")) != "", nil
			},
		},
	)

	// The state form. Its test mode is honest about what it cannot know:
	// a command's effect cannot be predicted, so test mode reports that it
	// would run the command rather than pretending to know the outcome.
	runState := func(c *exec.Context, args *value.Map) (states.Result, error) {
		if _, _, err := shellRequest(c, args); err != nil {
			return states.False(err.Error()), nil
		}
		if conflictingShell(args) {
			return states.False("This state asks for a shell and also gives `args`; " +
				"a shell line is one string and an argument vector is a list, so pick one. SPEC section 15.2"), nil
		}
		cmd := build(c, args)
		if c.Test {
			return states.WouldChange(
				fmt.Sprintf("The command %q would be run.", cmd.String()),
				value.MapOf("cmd", states.Change("not run", cmd.String())),
			), nil
		}
		if cmd.Shell {
			c.Logf("warn", "cmd.run is running through a shell: %s", cmd.String())
		}
		start := time.Now()
		res, err := c.Run(cmd)
		if err != nil {
			// The state's Name line already carries the command, and
			// the error names it again, so this does not. Repeating it
			// with %q also quoted it twice, because String() quotes an
			// argument that needs it.
			return states.False(fmt.Sprintf("The command could not be run: %v", err)), nil
		}
		ignore := states.Bool(args, "ignore_retcode", false)
		changes := value.MapOf(
			"pid", int64(0),
			"retcode", int64(res.Code),
			"stdout", trimOutput(res.Stdout),
			"stderr", trimOutput(res.Stderr),
			"duration_ms", res.Duration.Milliseconds(),
		)
		_ = start
		if res.Code != 0 && !ignore {
			out := states.False(fmt.Sprintf("The command %q exited %d.", cmd.String(), res.Code))
			out.Changes = changes
			return out, nil
		}
		return states.Changed(fmt.Sprintf("The command %q ran.", cmd.String()), changes), nil
	}

	r.States.Add(
		states.Module{
			Sig: signature.Signature{
				Module: "cmd", Function: "run",
				Doc:           "Run a command. Use unless, onlyif, or creates to make it idempotent.",
				Params:        cmdParams(),
				Mutates:       true,
				ArbitraryCode: true,
				TestMode:      signature.TestUnreliable,
				Section:       "15.5",
			},
			Fn:       runState,
			ModWatch: runState,
		},
		states.Module{
			Sig: signature.Signature{
				Module: "cmd", Function: "wait",
				Doc: "Run a command only when a watch requisite fires. On its own it does nothing, " +
					"which is the whole point: the reaction is the state.",
				Params:        cmdParams(),
				Mutates:       true,
				ArbitraryCode: true,
				TestMode:      signature.TestUnreliable,
				Section:       "15.5",
			},
			Fn: func(c *exec.Context, args *value.Map) (states.Result, error) {
				return states.True("This command waits for a watch requisite to fire, and none did."), nil
			},
			ModWatch: runState,
		},
	)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func durationOf(v any) (time.Duration, error) {
	switch t := v.(type) {
	case int64:
		return time.Duration(t) * time.Second, nil
	case float64:
		return time.Duration(t * float64(time.Second)), nil
	case string:
		return time.ParseDuration(t)
	}
	return 0, fmt.Errorf("%s is not a duration", value.TypeName(v))
}

// trimOutput removes the line ending a program leaves on its last line.
//
// Both characters, not just the newline: a program on Windows ends its
// output "\r\n", so trimming only "\n" left a carriage return on the end
// of every cmd.run and cmd.script return value. It reached a state's
// changes, an `unless` comparison, and anything a template did with the
// output — invisibly, since a stray \r does not show in a log line.
func trimOutput(s string) string { return strings.TrimRight(s, "\r\n") }
