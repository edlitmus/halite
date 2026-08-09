// Package modules implements halite's state and execution modules.
// Modules are registered in a flat "module.function" registry and receive a
// shared execution context (test mode, grains, base directory).
package modules

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Ctx is the execution context shared by all module calls in a run.
type Ctx struct {
	Test    bool           // dry-run: report what would change, change nothing
	Grains  map[string]any // system facts
	BaseDir string         // directory of the SLS file, for relative sources
}

// Result is the outcome of a single state application.
type Result struct {
	Ok      bool
	Changed bool
	Comment string
	Changes map[string]string
}

// Func is the signature every state function implements.
type Func func(c *Ctx, id string, args map[string]any) Result

// Registry maps "module.function" to its implementation.
var Registry = map[string]Func{}

func register(name string, f Func) { Registry[name] = f }

func resOK(comment string) Result {
	return Result{Ok: true, Comment: comment}
}

func resChanged(comment string, changes map[string]string) Result {
	return Result{Ok: true, Changed: true, Comment: comment, Changes: changes}
}

func resWould(comment string) Result {
	return Result{Ok: true, Changed: true, Comment: comment}
}

func resFail(format string, a ...any) Result {
	return Result{Comment: fmt.Sprintf(format, a...)}
}

// Str returns args[key] as a string, or def if absent or not a string.
func Str(args map[string]any, key, def string) string {
	if v, ok := args[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

// Bool interprets args[key] as a boolean flag.
func Bool(args map[string]any, key string, def bool) bool {
	s := Str(args, key, "")
	if s == "" {
		return def
	}
	switch strings.ToLower(s) {
	case "true", "yes", "1", "on":
		return true
	}
	return false
}

// List returns args[key] as a string slice; a scalar becomes a 1-element list.
func List(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// run executes argv directly (no shell) and returns combined behavior pieces.
func run(name string, arg ...string) (stdout, stderr string, rc int, err error) {
	cmd := exec.Command(name, arg...)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	rc = cmd.ProcessState.ExitCode()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); isExit {
			err = nil // non-zero exit is expressed via rc
		}
	}
	return out.String(), errb.String(), rc, err
}

// shellRun executes a command string through the platform shell.
func shellRun(command, cwd string, env []string) (stdout, stderr string, rc int, err error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("/bin/sh", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	rc = -1
	if cmd.ProcessState != nil {
		rc = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		if _, isExit := err.(*exec.ExitError); isExit {
			err = nil
		}
	}
	return out.String(), errb.String(), rc, err
}

func has(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
