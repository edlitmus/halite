// Package exec is the execution module surface: the registry, the calling
// convention, and the context a module function runs in.
//
// A module function receives its arguments already bound and type-checked
// against its signature, so a function body never has to guess what it was
// handed. That is what the build-time signatures of SPEC section 15.6 buy.
package exec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// Context is what a module function may read and act through.
type Context struct {
	// Ctx carries cancellation and the deadline of the surrounding job.
	Ctx context.Context

	Grains *value.Map
	Pillar *value.Map
	// Config is the effective configuration, redacted.
	Config *value.Map

	NodeID string
	Env    string
	JobID  string

	// Test marks a run that must change nothing. Every function that
	// mutates the system checks it.
	Test bool

	// Files fetches a halite:// or salt:// path. Nil when no file server
	// is available, which is the case for a purely local run over a tree
	// with no file references.
	Files FileFetcher

	// Dispatch calls another module function, which is how a state module
	// reaches the execution module that does the work.
	Dispatch Dispatcher

	// Log receives a line from a module. Nil discards.
	Log func(level, msg string)

	// Runner runs an external command. It is an interface so that a test
	// can observe what a module would have run without running it, which
	// is what makes the platform modules testable off their platform.
	Runner CommandRunner
}

// FileFetcher resolves a managed URI to a local path.
type FileFetcher interface {
	// Fetch returns a local path holding the contents of uri, caching it
	// if necessary.
	Fetch(env, uri string) (localPath string, err error)
	// Hash returns the server-side hash of a managed file.
	Hash(env, uri string) (algorithm, digest string, err error)
	// Exists reports whether a managed URI resolves.
	Exists(env, uri string) bool
}

// Dispatcher calls a module function by name.
type Dispatcher interface {
	Call(c *Context, name string, args *value.Map) (any, error)
	Has(name string) bool
}

// Logf writes a line if a logger is attached.
func (c *Context) Logf(level, format string, args ...any) {
	if c.Log == nil {
		return
	}
	c.Log(level, fmt.Sprintf(format, args...))
}

// Call invokes another module function through the dispatcher.
func (c *Context) Call(name string, args *value.Map) (any, error) {
	if c.Dispatch == nil {
		return nil, fmt.Errorf("no module dispatcher is available to call %s", name)
	}
	return c.Dispatch.Call(c, name, args)
}

// Func is one module function. Arguments arrive bound and type-checked
// against the function's signature.
type Func func(c *Context, args *value.Map) (any, error)

// Module is a function together with its signature.
type Module struct {
	Sig signature.Signature
	Fn  Func
}

// Registry holds the execution modules a build ships.
type Registry struct {
	fns  map[string]Func
	sigs *signature.Registry
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{fns: map[string]Func{}, sigs: signature.NewRegistry()}
}

// Add registers module functions.
func (r *Registry) Add(mods ...Module) {
	for _, m := range mods {
		r.fns[m.Sig.Name()] = m.Fn
		r.sigs.Add(m.Sig)
	}
}

// Signatures exposes the signature registry, which the state compiler and
// the API schema endpoint read.
func (r *Registry) Signatures() *signature.Registry { return r.sigs }

// Has reports whether a function is registered.
func (r *Registry) Has(name string) bool { _, ok := r.fns[name]; return ok }

// Names lists every function, sorted.
func (r *Registry) Names() []string { return r.sigs.Names() }

// Call binds arguments against the signature and invokes the function.
func (r *Registry) Call(c *Context, name string, args *value.Map) (any, error) {
	fn, ok := r.fns[name]
	if !ok {
		return nil, &UnknownFunctionError{Name: name, Known: r.nearMisses(name)}
	}
	sig, _ := r.sigs.Lookup(name)
	bound, errs := sig.Bind(nil, args)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("%s: %s", name, strings.Join(msgs, "; "))
	}
	return fn(c, bound)
}

// CallPositional binds a Salt-style argument vector: positional arguments
// then key=value pairs.
func (r *Registry) CallPositional(c *Context, name string, args []any, kwargs *value.Map) (any, error) {
	fn, ok := r.fns[name]
	if !ok {
		return nil, &UnknownFunctionError{Name: name, Known: r.nearMisses(name)}
	}
	sig, _ := r.sigs.Lookup(name)
	bound, errs := sig.Bind(args, kwargs)
	if len(errs) > 0 {
		msgs := make([]string, len(errs))
		for i, e := range errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("%s: %s", name, strings.Join(msgs, "; "))
	}
	return fn(c, bound)
}

// UnknownFunctionError names the functions a caller may have meant.
type UnknownFunctionError struct {
	Name  string
	Known []string
}

func (e *UnknownFunctionError) Error() string {
	if len(e.Known) == 0 {
		return fmt.Sprintf("%q is not a function this build ships; see the tier table in SPEC section 15", e.Name)
	}
	return fmt.Sprintf("%q is not a function this build ships; did you mean %s?", e.Name, strings.Join(e.Known, ", "))
}

// nearMisses lists the other functions of the same module.
func (r *Registry) nearMisses(name string) []string {
	module, _, ok := strings.Cut(name, ".")
	if !ok {
		return nil
	}
	var out []string
	for _, s := range r.sigs.Functions(module) {
		out = append(out, s.Name())
	}
	sort.Strings(out)
	if len(out) > 6 {
		out = out[:6]
	}
	return out
}

// ---- running external commands ----

// Result is the outcome of an external command.
type Result struct {
	Stdout   string
	Stderr   string
	Code     int
	Duration time.Duration
}

// CommandRunner executes an external command. Modules never call
// os/exec directly, so that a test can substitute a recorder.
type CommandRunner interface {
	Run(ctx context.Context, cmd Command) (Result, error)
}

// Command describes one external invocation.
//
// Argv rather than a shell string is the default everywhere, and Shell is
// the opt-in. Salt's default of a shell for cmd.run is the root of most of
// its injection findings, and inverting the default is a deliberate
// compatibility break with `cmd_default_shell` for a transition. SPEC
// section 15.2.
type Command struct {
	// Argv is the program and its arguments.
	Argv []string
	// Shell runs the first Argv element through a shell instead.
	Shell bool
	// Dir is the working directory.
	Dir string
	// Env replaces the environment entirely when non-nil.
	Env []string
	// Stdin is written to the process.
	Stdin string
	// Timeout bounds the run; zero means the context's deadline governs.
	Timeout time.Duration
	// RunAs switches to another account, using setuid and setgid with the
	// target's full supplementary group set rather than `su -c`.
	RunAs string
	// Umask is applied to the child.
	Umask string
	// IgnoreExitCode stops a non-zero exit from being an error, which is
	// what `unless` and `onlyif` need: the exit code is the answer.
	IgnoreExitCode bool
}

// String renders the command for a log line or a comment.
func (c Command) String() string {
	if c.Shell {
		return strings.Join(c.Argv, " ")
	}
	quoted := make([]string, len(c.Argv))
	for i, a := range c.Argv {
		if strings.ContainsAny(a, " \t\"'\\$") {
			quoted[i] = fmt.Sprintf("%q", a)
			continue
		}
		quoted[i] = a
	}
	return strings.Join(quoted, " ")
}

// OSRunner runs commands through os/exec.
type OSRunner struct {
	// DefaultEnv is the environment a child receives when Command.Env is
	// nil. Children get a clean environment by default: no hub
	// credentials, no pillar values, and an explicit PATH. SPEC section
	// 25.4.
	DefaultEnv []string
}

// CleanEnv is the environment a spawned process receives unless a module
// says otherwise.
func CleanEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	return []string{
		"PATH=" + path,
		"LC_ALL=C",
		"LANG=C",
		"HALITE=1",
	}
}

// Run implements CommandRunner.
func (r *OSRunner) Run(ctx context.Context, cmd Command) (Result, error) {
	if len(cmd.Argv) == 0 {
		return Result{}, fmt.Errorf("no command given")
	}
	if cmd.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cmd.Timeout)
		defer cancel()
	}

	var c *exec.Cmd
	if cmd.Shell {
		shell := "/bin/sh"
		c = exec.CommandContext(ctx, shell, "-c", strings.Join(cmd.Argv, " "))
	} else {
		c = exec.CommandContext(ctx, cmd.Argv[0], cmd.Argv[1:]...)
	}
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	if c.Env == nil {
		c.Env = r.DefaultEnv
		if c.Env == nil {
			c.Env = CleanEnv()
		}
	}
	if cmd.Stdin != "" {
		c.Stdin = strings.NewReader(cmd.Stdin)
	}
	if err := applyCredential(c, cmd); err != nil {
		return Result{}, err
	}

	var stdout, stderr strings.Builder
	c.Stdout = &stdout
	c.Stderr = &stderr

	start := time.Now()
	err := c.Run()
	res := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: time.Since(start),
	}
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok {
			res.Code = ee.ExitCode()
			if cmd.IgnoreExitCode {
				return res, nil
			}
			return res, fmt.Errorf("%s exited %d: %s", cmd.String(), res.Code, firstLine(res.Stderr))
		}
		return res, fmt.Errorf("%s: %w", cmd.String(), err)
	}
	return res, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	ee, ok := err.(*exec.ExitError)
	if ok {
		*target = ee
	}
	return ok
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// RecordingRunner records commands instead of running them, and answers
// from a scripted table. It is what lets the platform modules be tested on
// a machine that is not the platform.
type RecordingRunner struct {
	// Responses maps a command string to its result.
	Responses map[string]Result
	// Errors maps a command string to an error.
	Errors map[string]error
	// Ran records every command in order.
	Ran []Command
	// Default is returned for a command with no scripted response.
	Default Result
}

// Run implements CommandRunner.
func (r *RecordingRunner) Run(_ context.Context, cmd Command) (Result, error) {
	r.Ran = append(r.Ran, cmd)
	key := cmd.String()
	if err, ok := r.Errors[key]; ok {
		return r.Responses[key], err
	}
	if res, ok := r.Responses[key]; ok {
		return res, nil
	}
	return r.Default, nil
}

// RanCommands renders the recorded commands, for an assertion.
func (r *RecordingRunner) RanCommands() []string {
	out := make([]string, len(r.Ran))
	for i, c := range r.Ran {
		out[i] = c.String()
	}
	return out
}

// Run is the convenience wrapper a module uses.
func (c *Context) Run(cmd Command) (Result, error) {
	if c.Runner == nil {
		c.Runner = &OSRunner{}
	}
	ctx := c.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return c.Runner.Run(ctx, cmd)
}

// RunArgv is the common case: an argument vector with no shell.
func (c *Context) RunArgv(argv ...string) (Result, error) {
	return c.Run(Command{Argv: argv})
}

// Which reports the path of a program, or the empty string.
func (c *Context) Which(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}
