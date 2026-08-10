// Package extmod runs state modules that are not compiled into halite.
//
// A module is an executable in the state tree's `_modules/` directory. Its
// file name is the module name, so `_modules/nginx` provides `nginx.*`.
// halite runs it with the function as its argument, writes a JSON request
// on stdin, and reads a JSON result from stdout.
//
// This is the escape hatch ADR-1 needs: everything compiled in is Go with
// no dependencies, and anything else is a program you write in whatever
// you like, shipped with your states. It is not a plugin API — there is no
// linking, no ABI, and a module that crashes fails one state.
package extmod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/modules"
)

// DirName is the directory external modules live in, relative to the state
// tree root. It ships to agents with the rest of the tree.
const DirName = "_modules"

// DefaultTimeout bounds one module call.
const DefaultTimeout = 5 * time.Minute

// maxOutput bounds what a module may write on each of stdout and stderr. A
// result is a small JSON document; a runaway module must not OOM the agent
// by flooding a pipe.
const maxOutput = 8 << 20

// boundedBuffer keeps the first maxOutput bytes and discards the rest,
// never failing the writer: the module's exit is judged separately.
type boundedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := maxOutput - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
			b.truncated = true
		}
		b.buf.Write(p)
	} else if n > 0 {
		b.truncated = true
	}
	return n, nil
}

// Request is the JSON written to a module's stdin.
type Request struct {
	Function string         `json:"function"`
	ID       string         `json:"id"`
	Test     bool           `json:"test"`
	Args     map[string]any `json:"args"`
	Grains   map[string]any `json:"grains,omitempty"`
	Pillar   map[string]any `json:"pillar,omitempty"`
	Mine     map[string]any `json:"mine,omitempty"`
}

// Response is the JSON a module writes to stdout.
type Response struct {
	Ok      bool              `json:"result"`
	Changed bool              `json:"changed"`
	Comment string            `json:"comment"`
	Changes map[string]string `json:"changes,omitempty"`
}

// Resolver finds and runs external modules under a directory.
type Resolver struct {
	Dir     string
	Timeout time.Duration
}

// Lookup returns a function resolver that tries the built-in registry
// first and then executables in dir. It is shaped to be handed straight to
// engine.RunWith.
func Lookup(dir string) func(name string) (modules.Func, bool) {
	resolver := &Resolver{Dir: dir}
	return func(name string) (modules.Func, bool) {
		if fn, ok := modules.Registry[name]; ok {
			return fn, true
		}
		return resolver.Lookup(name)
	}
}

// Lookup finds the executable backing a "module.function" name.
func (r *Resolver) Lookup(name string) (modules.Func, bool) {
	if r.Dir == "" {
		return nil, false
	}
	module, function, found := strings.Cut(name, ".")
	if !found || module == "" || function == "" {
		return nil, false
	}
	program, ok := r.program(module)
	if !ok {
		return nil, false
	}
	return func(c *modules.Ctx, id string, args map[string]any) modules.Result {
		return r.run(program, function, c, id, args)
	}, true
}

// Names lists the external modules available, for diagnostics.
func (r *Resolver) Names() []string {
	if r.Dir == "" {
		return nil
	}
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if _, ok := r.program(name); ok {
			out = append(out, name)
		}
	}
	return out
}

// program resolves a module name to an executable path, refusing anything
// that is not a plain file directly inside the directory. A module name
// arrives from an SLS file, which is trusted, but a name containing a
// separator would still be a surprise worth refusing.
func (r *Resolver) program(module string) (string, bool) {
	if module == "" || strings.ContainsAny(module, `/\`) || strings.Contains(module, "..") {
		return "", false
	}
	candidates := []string{module}
	if runtime.GOOS == "windows" {
		candidates = []string{module + ".exe", module + ".bat", module + ".cmd", module}
	}
	for _, candidate := range candidates {
		path := filepath.Join(r.Dir, candidate)
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			continue // present but not executable: not a module
		}
		return path, true
	}
	return "", false
}

// run invokes a module and turns its answer into a Result.
func (r *Resolver) run(program, function string, c *modules.Ctx, id string, args map[string]any) modules.Result {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	request := Request{
		Function: function,
		ID:       id,
		Test:     c.Test,
		Args:     args,
		Grains:   c.Grains,
		Pillar:   c.Pillar,
		Mine:     c.Mine,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return modules.Result{Comment: fmt.Sprintf("encode request: %v", err)}
	}

	cmd := exec.CommandContext(ctx, program, function)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Dir = filepath.Dir(program)
	// Killing a module on timeout kills the module, not whatever it
	// spawned, and a grandchild holding the output pipe would keep Wait
	// blocked forever. WaitDelay closes the pipes shortly after the kill so
	// a timeout is a timeout.
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr boundedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return modules.Result{Comment: fmt.Sprintf("%s %s: timed out after %s",
			filepath.Base(program), function, timeout)}
	}
	if runErr != nil {
		if _, isExit := runErr.(*exec.ExitError); !isExit {
			return modules.Result{Comment: fmt.Sprintf("%s: %v", filepath.Base(program), runErr)}
		}
		// A non-zero exit is a failed state, and stderr is the reason.
		message := strings.TrimSpace(stderr.buf.String())
		if message == "" {
			message = strings.TrimSpace(stdout.buf.String())
		}
		if message == "" {
			message = "exited non-zero without a message"
		}
		return modules.Result{Comment: fmt.Sprintf("%s %s: %s", filepath.Base(program), function, message)}
	}
	if stdout.truncated {
		return modules.Result{Comment: fmt.Sprintf("%s %s: wrote more than %d bytes of output; a result is a small JSON document",
			filepath.Base(program), function, maxOutput)}
	}

	return decode(filepath.Base(program), function, stdout.buf.Bytes(), stderr.buf.String())
}

// decode turns a module's stdout into a Result.
func decode(module, function string, stdout []byte, stderr string) modules.Result {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return modules.Result{Comment: fmt.Sprintf("%s %s: exited 0 but wrote no result", module, function)}
	}
	var response Response
	if err := json.Unmarshal(trimmed, &response); err != nil {
		// Show what it said instead of the raw parse error: the usual cause
		// is a module printing progress where its result should be.
		return modules.Result{Comment: fmt.Sprintf("%s %s: output is not a JSON result: %s",
			module, function, firstLine(string(trimmed)))}
	}
	result := modules.Result{
		Ok:      response.Ok,
		Changed: response.Changed,
		Comment: response.Comment,
		Changes: response.Changes,
	}
	if result.Comment == "" {
		result.Comment = fmt.Sprintf("%s %s completed", module, function)
	}
	// Anything a module wrote to stderr is worth keeping when it failed.
	if !result.Ok && strings.TrimSpace(stderr) != "" {
		result.Comment += ": " + firstLine(strings.TrimSpace(stderr))
	}
	return result
}

func firstLine(s string) string {
	if index := strings.IndexByte(s, '\n'); index >= 0 {
		s = s[:index]
	}
	const limit = 200
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
}
