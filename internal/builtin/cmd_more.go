package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileperm"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The rest of the cmd module, SPEC section 15.2.
//
// `shell` and `script` are the two functions here that run a shell on
// purpose, and both say so. Everything else in this module takes an
// argument vector, which is the inversion the section describes: Salt's
// default of a shell for cmd.run is the root of most of its injection
// findings.

func registerCmdMore(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "cmd", Function: "shell",
				Doc: "Run a command line through a shell. The explicit form of `cmd.run` with shell set; " +
					"prefer an argument vector, which cannot be reinterpreted by anything.",
				Params: []signature.Param{
					req("cmd", signature.String, "The shell line."),
					opt("cwd", signature.Path, "", "Working directory."),
					opt("runas", signature.String, "", "Account to run as."),
					opt("umask", signature.String, "", "Umask for the child."),
					opt("env", signature.Map, nil, "Environment, replacing the clean default."),
					opt("stdin", signature.String, "", "Text written to the process."),
					opt("timeout", signature.Duration, nil, "How long it may run."),
					opt("ignore_retcode", signature.Bool, false, "Treat a non-zero exit as success."),
				},
				Mutates: true,
				// SPEC 23.5 names cmd.shell among the functions a
				// wildcard never grants, and it was not declaring it.
				// A role deliberately refused cmd.run got the same
				// power by asking for cmd.shell instead, which is the
				// whole of the control undone by one missing field.
				ArbitraryCode: true,
				TestMode:      signature.TestUnreliable,
				Section:       "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				cmd, err := buildShellCommand(c, args, states.Str(args, "cmd", ""))
				if err != nil {
					return nil, err
				}
				c.Logf("warn", "cmd.shell is running through a shell: %s", cmd.String())
				res, err := c.Run(cmd)
				if err != nil {
					return nil, err
				}
				return trimOutput(res.Stdout), nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "cmd", Function: "script",
				Doc: "Fetch a script, run it, and remove it. The source may be a salt:// URI or a local path.",
				Params: []signature.Param{
					req("source", signature.String, "Where the script comes from."),
					opt("args", signature.List, nil, "Arguments passed to the script."),
					opt("shell", signature.String, "", "Interpreter to run it with. Empty runs the script itself, honouring its shebang."),
					opt("cwd", signature.Path, "", "Working directory."),
					opt("runas", signature.String, "", "Account to run as."),
					opt("umask", signature.String, "", "Umask for the child."),
					opt("env", signature.Map, nil, "Environment, replacing the clean default."),
					opt("timeout", signature.Duration, nil, "How long it may run."),
					opt("ignore_retcode", signature.Bool, false, "Treat a non-zero exit as success."),
				},
				Mutates: true,
				// It runs whatever script the caller names, which is the
				// definition of SPEC 23.5's class. The state module of the
				// same name always declared it; this one did not, and the
				// node computes `require_job_signature: [arbitrary_code]`
				// from the execution registry alone, so an unsigned
				// `cmd.script` ran there. See DIVERGENCE 5.297.
				ArbitraryCode: true,
				TestMode:      signature.TestUnreliable,
				Section:       "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return runScript(c, args, true)
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "cmd", Function: "script_retcode",
				Doc: "Run a script and return only its exit code.",
				Params: []signature.Param{
					req("source", signature.String, "Where the script comes from."),
					opt("args", signature.List, nil, "Arguments passed to the script."),
					opt("shell", signature.String, "", "Interpreter to run it with."),
					opt("cwd", signature.Path, "", "Working directory."),
					opt("runas", signature.String, "", "Account to run as."),
					opt("timeout", signature.Duration, nil, "How long it may run."),
				},
				Mutates: true,
				// cmd.script under another name: the same runScript, only
				// the return value differs. Leaving it unmarked is leaving
				// cmd.script granted by wildcard (DIVERGENCE 5.297).
				ArbitraryCode: true,
				TestMode:      signature.TestUnreliable,
				Section:       "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				out, err := runScript(c, args, false)
				if err != nil {
					return nil, err
				}
				code, _ := out.(*value.Map).Get("retcode")
				return code, nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "cmd", Function: "exec_code",
				Doc: "Run a snippet of code with an interpreter, without leaving it on disk any longer than the run.",
				Params: []signature.Param{
					req("lang", signature.String, "The interpreter, such as python3 or perl."),
					req("code", signature.String, "The code."),
					opt("args", signature.List, nil, "Arguments passed to the interpreter after the script."),
					opt("cwd", signature.Path, "", "Working directory."),
					opt("runas", signature.String, "", "Account to run as."),
					opt("timeout", signature.Duration, nil, "How long it may run."),
				},
				Mutates: true,
				// The caller supplies the code and the interpreter. There is
				// no plainer case of arbitrary code in the module library,
				// and a wildcard was granting it (DIVERGENCE 5.297).
				ArbitraryCode: true,
				TestMode:      signature.TestUnreliable,
				Section:       "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				lang := states.Str(args, "lang", "")
				if lang == "" {
					return nil, fmt.Errorf("cmd.exec_code needs an interpreter")
				}
				// The code goes to a file rather than to the interpreter's
				// standard input, because an interpreter reading a script
				// from stdin cannot then be given stdin of its own, and
				// because a syntax error names a file the operator can be
				// told about.
				// No suffix: the interpreter is named, so the file is an
				// argument to it rather than something the platform has to
				// recognise on its own.
				path, cleanup, err := tempScript(states.Str(args, "code", ""), os.FileMode(scriptMode()), "", scriptRunAs(c, args))
				if err != nil {
					return nil, err
				}
				defer cleanup()

				argv := append([]string{lang, path}, states.Strings(args, "args")...)
				cmd, err := scriptCommand(c, args, argv)
				if err != nil {
					return nil, err
				}
				res, err := c.Run(cmd)
				if err != nil {
					return nil, err
				}
				return trimOutput(res.Stdout), nil
			},
		},

		exec.Module{
			Sig: signature.Signature{
				Module: "cmd", Function: "run_bg",
				Doc: "Start a command and return its pid without waiting for it. " +
					"Nothing collects its output or its exit code; use it for something that outlives the run.",
				Params: []signature.Param{
					req("name", signature.String, "The program."),
					opt("args", signature.List, nil, "Arguments."),
					opt("cwd", signature.Path, "", "Working directory."),
					opt("runas", signature.String, "", "Account to run as."),
					opt("env", signature.Map, nil, "Environment, replacing the clean default."),
				},
				Mutates: true,
				// cmd.run without waiting: the caller names the program and
				// its arguments. A role refused cmd.run got the same thing,
				// detached, by asking for this (DIVERGENCE 5.297).
				ArbitraryCode: true,
				TestMode:      signature.TestUnreliable,
				Section:       "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				return runInBackground(c, args)
			},
		},
	)
}

// buildShellCommand assembles the shell form, which only cmd.shell and
// cmd.run with shell set may use.
func buildShellCommand(c *exec.Context, args *value.Map, line string) (exec.Command, error) {
	cmd := exec.Command{
		Argv:           []string{line},
		Shell:          true,
		Dir:            states.Str(args, "cwd", ""),
		RunAs:          states.Str(args, "runas", ""),
		Umask:          states.Str(args, "umask", ""),
		Stdin:          states.Str(args, "stdin", ""),
		IgnoreExitCode: states.Bool(args, "ignore_retcode", false),
	}
	if envMap := states.Mapping(args, "env"); envMap != nil {
		env := exec.CleanEnv()
		for _, e := range envMap.Entries() {
			env = append(env, value.KeyString(e.Key)+"="+value.KeyString(e.Val))
		}
		cmd.Env = env
	}
	// An unreadable timeout is refused, as cmd.run refuses it. It was
	// `if d, err := durationOf(v); err == nil`, so `timeout: banana`, or a
	// template that rendered something else, ran the command with no
	// deadline at all: the 5.139 defect, fixed in cmd.run and not here
	// (issue 321, section 5).
	if v, ok := args.Get("timeout"); ok && v != nil {
		d, err := durationOf(v)
		if err != nil {
			return exec.Command{}, fmt.Errorf("timeout: %w", err)
		}
		cmd.Timeout = d
	}
	return cmd, nil
}

func scriptCommand(c *exec.Context, args *value.Map, argv []string) (exec.Command, error) {
	cmd, err := buildShellCommand(c, args, "")
	if err != nil {
		return exec.Command{}, err
	}
	cmd.Shell = false
	cmd.Argv = argv
	return cmd, nil
}

// scriptRunAs is the account a script will run as: the state's runas, or
// the context's.
func scriptRunAs(c *exec.Context, args *value.Map) string {
	if as := states.Str(args, "runas", ""); as != "" {
		return as
	}
	return c.RunAs
}

// tempScript writes a script to a file only its owner can read or run,
// and returns a function that removes it. owner, when given, is the
// account that will run it, and it is given the file.
//
// **A script run as another account has to be that account's** (issue
// 321, M-27). It was written as root, 0700, and never handed over, so
// `cmd.script` and `cmd.exec_code` with `runas` could not work at all: the
// child, running as the account, got "permission denied" opening its own
// script. Salt chowns the script to runas, and so does this now. The mode
// stays 0700, so it is still the one account's alone.
//
// The mode matters: a script that carries a credential, and many do,
// must not be readable by every account on the node for the length of the
// run. It goes in the system temporary directory rather than the target
// directory, because a script is not a configuration file and leaving one
// behind next to the files it manages is worse than a cross-device copy.
func tempScript(body string, mode os.FileMode, suffix, owner string) (path string, cleanup func(), err error) {
	// The suffix matters on a platform where what runs a file is its
	// name rather than its first line. See scriptSuffix.
	//
	// A script another account runs is made in /tmp rather than in
	// $TMPDIR. $TMPDIR is the caller's own, and is often private to it: on
	// macOS it is /var/folders/…/T, 0700, and a node started from a shell
	// under sudo inherits it. A script given to the account in there is
	// still behind a directory the account cannot enter, and the run failed
	// "permission denied", exit 126, on macOS CI. /tmp is sticky and open to
	// every account on every unix this runs on, and CreateTemp's O_EXCL and
	// random name are what make writing a root-owned file there safe.
	dir := ""
	if owner != "" && runtime.GOOS != "windows" {
		dir = "/tmp"
	}
	f, err := os.CreateTemp(dir, "halite-script-*"+suffix)
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	cleanup = func() { os.Remove(name) }
	// fileperm rather than Chmod: on Windows a mode does not decide who
	// can read a file, and a script carrying a credential was left
	// readable by every account for the length of the run.
	if err := fileperm.ApplyFile(f, mode); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if owner != "" && runtime.GOOS != "windows" {
		if err := applyOwnership(name, owner, ""); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("the script could not be given to %s, who is to run it: %w", owner, err)
		}
	}
	return name, cleanup, nil
}

// runScript fetches a script, runs it, and removes it.
func runScript(c *exec.Context, args *value.Map, wantOutput bool) (any, error) {
	source := states.Str(args, "source", "")
	if source == "" {
		return nil, fmt.Errorf("cmd.script needs a source")
	}

	body, err := fetchScript(c, source)
	if err != nil {
		return nil, err
	}
	path, cleanup, err := tempScript(body, os.FileMode(scriptMode()), scriptSuffix(source), scriptRunAs(c, args))
	if err != nil {
		return nil, err
	}
	defer cleanup()

	var argv []string
	if shell := states.Str(args, "shell", ""); shell != "" {
		argv = append(argv, shell)
	} else {
		// A kind the platform does not start on its own gets the
		// program that does start it: a .ps1 is not an executable
		// file, it is an argument to powershell.
		argv = append(argv, scriptInterpreter(path)...)
	}
	argv = append(argv, path)
	argv = append(argv, states.Strings(args, "args")...)

	cmd, err := scriptCommand(c, args, argv)
	if err != nil {
		return nil, err
	}
	cmd.IgnoreExitCode = true
	res, err := c.Run(cmd)
	if err != nil {
		return nil, err
	}
	out := value.MapOf(
		"stdout", trimOutput(res.Stdout),
		"stderr", trimOutput(res.Stderr),
		"retcode", int64(res.Code),
		"duration_ms", int64(res.Duration.Milliseconds()),
	)
	if !wantOutput {
		return out, nil
	}
	if res.Code != 0 && !states.Bool(args, "ignore_retcode", false) {
		return out, fmt.Errorf("%s exited %d: %s", filepath.Base(source), res.Code, firstLineOf(res.Stderr))
	}
	return out, nil
}

// fetchScript resolves a source, which may be a managed URI or a local
// path. A managed URI goes through the file server, so the containment
// rules of SPEC section 13.5 apply to it as they do to any other file.
func fetchScript(c *exec.Context, source string) (string, error) {
	if fileserver.IsManagedURI(source) {
		if c.Files == nil {
			return "", fmt.Errorf("%s needs a file server, and this run has none", source)
		}
		local, err := c.Files.Fetch(c.Env, source)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(local)
		return string(data), err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// runInBackground starts a command and returns its pid without waiting.
//
// It goes through the runner with Command.Background set, which is the
// one place the umask, the environment, the directory and the account
// are applied to a child. It used to build its own os/exec.Cmd, on the
// reasoning that the runner's contract is to wait; the runner has since
// learned not to, and the private copy had quietly diverged from it in
// the way that matters most: it never read `runas`, although the
// signature declares it, so `cmd.run_bg runas=deploy` started the
// program as root and returned a pid. DIVERGENCE 5.301.
//
// A recorded run reports the command it would have started and no pid,
// so a module test still sees the argv -- and now the account too.
func runInBackground(c *exec.Context, args *value.Map) (any, error) {
	name := states.Str(args, "name", "")
	if name == "" {
		return nil, fmt.Errorf("cmd.run_bg needs a program")
	}
	cmd := exec.Command{
		Argv:       append([]string{name}, states.Strings(args, "args")...),
		Dir:        states.Str(args, "cwd", ""),
		RunAs:      states.Str(args, "runas", ""),
		Background: true,
	}
	if envMap := states.Mapping(args, "env"); envMap != nil {
		env := exec.CleanEnv()
		for _, e := range envMap.Entries() {
			env = append(env, value.KeyString(e.Key)+"="+value.KeyString(e.Val))
		}
		cmd.Env = env
	}

	res, err := c.Run(cmd)
	if err != nil {
		return nil, err
	}
	if _, real := c.Runner.(*exec.OSRunner); !real {
		// A test's recorder gets to see the command; nothing is started.
		return value.MapOf("pid", int64(0), "recorded", true, "stdout", res.Stdout), nil
	}
	pid, err := strconv.ParseInt(strings.TrimSpace(res.Stdout), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("cmd.run_bg: the runner reported no pid: %q", res.Stdout)
	}
	return value.MapOf("pid", pid), nil
}

// registerCmdScriptState adds the `cmd.script` state. Salt's takes the
// source as the state's `name`, which is how a tree spells
// `cmd.script: - name: salt://files/x.sh`, and that is the spelling a
// real tree used.
func registerCmdScriptState(r *Registries) {
	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "cmd", Function: "script",
			Doc: "Fetch a script, run it, and remove it. Use unless, onlyif, or creates to make it idempotent.",
			Params: []signature.Param{
				req("name", signature.String, "The script source: a halite:// or salt:// URI, or a local path."),
				opt("source", signature.String, "", "The source, when it differs from the state ID."),
				opt("args", signature.List, nil, "Arguments passed to the script."),
				opt("shell", signature.String, "", "Interpreter to run it with. Empty honours the script's shebang."),
				opt("cwd", signature.Path, "", "Working directory."),
				opt("runas", signature.String, "", "Account to run as."),
				opt("umask", signature.String, "", "Umask for the child."),
				opt("env", signature.Map, nil, "Environment, replacing the clean default."),
				opt("timeout", signature.Duration, nil, "How long it may run."),
				opt("ignore_retcode", signature.Bool, false, "Treat a non-zero exit as success."),
			},
			Mutates:       true,
			ArbitraryCode: true,
			TestMode:      signature.TestUnreliable,
			Section:       "15.5",
		},
		Fn: cmdScriptState,
	})
}

func cmdScriptState(c *exec.Context, args *value.Map) (states.Result, error) {
	source := states.Str(args, "source", "")
	if source == "" {
		source = states.Str(args, "name", "")
	}
	if source == "" {
		return states.False("This state needs a script source."), nil
	}

	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("The script %s would be fetched and run.", source),
			value.MapOf("cmd", states.Change("not run", source)),
		), nil
	}

	// runScript reads `source`, and the state may have carried it as
	// `name`, so the resolved one is put where the execution module
	// looks for it.
	call := args.Clone()
	call.Set("source", source)

	out, err := runScript(c, call, true)
	if err != nil {
		return states.False(fmt.Sprintf("The script %s could not be run: %v", source, err)), nil
	}
	res, ok := out.(*value.Map)
	if !ok {
		return states.Changed(fmt.Sprintf("The script %s ran.", source), value.MapOf("cmd", states.Change("not run", source))), nil
	}

	code, _ := res.Get("retcode")
	changes := value.NewMap(4)
	for _, key := range []string{"pid", "retcode", "stdout", "stderr"} {
		if v, ok := res.Get(key); ok {
			changes.Set(key, v)
		}
	}
	if n, ok := code.(int64); ok && n != 0 && !states.Bool(args, "ignore_retcode", false) {
		failed := states.False(fmt.Sprintf("The script %s exited %d.", source, n))
		failed.Changes = changes
		return failed, nil
	}
	return states.Changed(fmt.Sprintf("The script %s ran.", source), changes), nil
}
