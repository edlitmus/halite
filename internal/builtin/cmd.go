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
// Default execution is **without a shell**, taking an argument vector.
// Salt's default of a shell for cmd.run is the root of most Salt injection
// findings, and inverting the default is a deliberate compatibility break
// with `cmd_default_shell` for a transition. SPEC section 15.2.
func registerCmd(r *Registries) {
	cmdParams := func(extra ...signature.Param) []signature.Param {
		base := []signature.Param{
			req("name", signature.String, "The program to run, or the shell line when shell is true."),
			opt("args", signature.List, nil, "Arguments to the program, when not using a shell."),
			opt("shell", signature.Bool, false, "Run the command through a shell. Off by default; see SPEC section 15.2."),
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

	build := func(c *exec.Context, args *value.Map) exec.Command {
		cmd := exec.Command{
			Shell:          states.Bool(args, "shell", false),
			Dir:            states.Str(args, "cwd", ""),
			RunAs:          states.Str(args, "runas", ""),
			Umask:          states.Str(args, "umask", ""),
			Stdin:          states.Str(args, "stdin", ""),
			IgnoreExitCode: true,
		}
		name := states.Str(args, "name", "")
		if cmd.Shell {
			cmd.Argv = []string{name}
		} else {
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
		cmd := build(c, args)
		if cmd.Shell {
			c.Logf("warn", "cmd is running through a shell: %s", cmd.String())
		}
		res, err := c.Run(cmd)
		if err != nil {
			return nil, err
		}
		return value.MapOf(
			"stdout", strings.TrimRight(res.Stdout, "\n"),
			"stderr", strings.TrimRight(res.Stderr, "\n"),
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
			return states.False(fmt.Sprintf("The command %q could not be run: %v", cmd.String(), err)), nil
		}
		ignore := states.Bool(args, "ignore_retcode", false)
		changes := value.MapOf(
			"pid", int64(0),
			"retcode", int64(res.Code),
			"stdout", strings.TrimRight(res.Stdout, "\n"),
			"stderr", strings.TrimRight(res.Stderr, "\n"),
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
