// Package rendersandbox is SPEC section 25.4's render sandbox: YAML
// parsing and template rendering in an unprivileged child process.
//
// # Why a child at all
//
// SPEC's argument is one sentence and it is the whole design: "the
// parser and the template engine are the largest and most
// attacker-adjacent code in the system, and they need no privilege at
// all". A state tree is attacker-adjacent because it arrives from a file
// server, from gitfs, or from whoever can commit to the repository the
// tree lives in, and the node that renders it runs as root because
// package and service management require it. Everything between those
// two facts is this package.
//
// # What crosses the boundary
//
// Data, in both directions, and nothing else. The parent sends the file
// body, the stage list, and the context a template may read: grains,
// pillar, the redacted configuration, the environment names. The child
// sends back the parsed value with its source positions intact, the
// rendered text, and the warnings.
//
// Two things the child cannot do for itself come back as callbacks. A
// template that includes another one needs the parent, which is the only
// side that can reach the hub's file server. And `salt['pkg.version']`
// needs the parent, because module execution is the privilege being
// withheld. SPEC 25.4 is explicit that this is the shape: "module
// execution happens in the privileged parent ... the sandbox returns
// data, never a callable and never a command to run without validation".
//
// # What it does not protect against
//
// Stated here rather than left to be discovered. A template that calls
// an execution module still causes the parent to run that module, and a
// tree that can write `{{ salt['cmd.run']('...') }}` gets the same effect
// sandboxed or not: that is what the template language is, and the
// controls for it are RBAC's separate `arbitrary_code` permission and
// the signed-tree work of SPEC 25.1. What moves into the child is the
// code that parses and evaluates the attacker's *text* -- the lexer, the
// parser, the evaluator, the YAML reader -- so that a memory-safety or
// logic defect in any of them lands in a process that owns nothing.
//
// # Honesty about enforcement
//
// `Describe` reports what is actually applied on the machine in front of
// the operator, in the manner of the bridge sandbox of SPEC 24.3. A
// process boundary holds everywhere. Dropping to an unprivileged account
// needs a parent that is root and an account to drop to. Denying the
// network needs a mechanism that only Linux has here. None of it is
// silently skipped and none of it is claimed where it is not applied.
package rendersandbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/template"
)

// DefaultTimeout bounds one render.
//
// The template engine has its own timeout (SPEC 10.2.8) and this is not
// a second copy of it: it bounds the exchange, so that a child that has
// stopped answering -- killed by the out-of-memory killer, stuck in a
// syscall, gone -- fails the render rather than hanging the run. It is
// deliberately much longer than a render takes.
const DefaultTimeout = 120 * time.Second

// Config is how a sandbox is set up.
type Config struct {
	// Exe is the program to run as the child, and Args are the
	// arguments that put it into render-sandbox mode. Empty Exe means
	// this binary, which is the ordinary case: the child is halite
	// itself, re-executed.
	Exe  string
	Args []string

	// User and Group are the unprivileged account to drop to. Empty
	// means the child keeps the parent's identity, which is what a node
	// not running as root gets, and `Describe` says so.
	User  string
	Group string

	// Timeout bounds one render. Zero takes DefaultTimeout.
	Timeout time.Duration

	// Stderr receives the child's standard error. Nil discards it,
	// which is wrong for an operator trying to find out why a render
	// failed, so callers pass the node's log.
	Stderr io.Writer
}

// Sandbox is a render.Engine that renders in a child process.
//
// One child, reused across renders, and one render at a time. A
// highstate compiles its files sequentially, so a pool would add
// concurrency the caller does not have; the child is kept alive between
// renders because starting one per SLS file is the cost that would make
// an operator turn this off.
type Sandbox struct {
	cfg Config

	mu    sync.Mutex
	child *childProcess
}

// New returns a sandbox. It starts no process: the first render does,
// so that turning the setting on cannot fail a node's startup.
func New(cfg Config) *Sandbox {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Sandbox{cfg: cfg}
}

// Close ends the child, if one is running.
func (s *Sandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child == nil {
		return nil
	}
	err := s.child.close()
	s.child = nil
	return err
}

// Render satisfies render.Engine.
//
// The pipeline is split here rather than in the child: the template and
// serializer stages run there, and the data stages -- `gpg`, and only
// `gpg` in this build -- run in this process afterwards, because
// decryption needs a keyring the child deliberately cannot reach. The
// consequence worth stating is that decrypted pillar never enters the
// unprivileged process at all.
func (s *Sandbox) Render(src []byte, opts render.Options) (render.Result, error) {
	stages, body := render.ParsePipelineWith(string(src), opts.Renderer)
	res := render.Result{Pipeline: stages}

	// Checked here as well as in the child, so that an unrunnable
	// pipeline is refused in the same words whether or not the sandbox
	// is on, and before a process is started for it.
	if err := render.CheckPipeline(stages, opts.File); err != nil {
		return res, err
	}
	head, tail := render.SplitPipeline(stages)

	out, err := s.renderInChild(body, head, opts)
	if err != nil {
		return res, err
	}
	out.Pipeline = stages

	if len(tail) > 0 {
		v, err := render.ApplyDataStages(out.Value, tail, opts)
		if err != nil {
			return out, fmt.Errorf("%s: %w", opts.File, err)
		}
		out.Value = v
	}
	return out, nil
}

// Template renders a `file.managed` source in the child.
//
// It is the jinja stage alone over the bytes as they are: no pipeline is
// parsed, because the first line of a managed file is content and
// reading a `#!` there as a renderer would deliver something other than
// the file. That is `render.Template`'s own reasoning, and expressing it
// as a one-stage run is what lets it cross the same boundary.
func (s *Sandbox) Template(src []byte, opts render.Options) (string, []render.Warning, error) {
	res, err := s.renderInChild(string(src), []string{"jinja"}, opts)
	if err != nil {
		return "", res.Warnings, err
	}
	return res.Text, res.Warnings, nil
}

// Describe says what this sandbox actually enforces here.
func (s *Sandbox) Describe() []string {
	out := []string{"process boundary: YAML parsing and template rendering run in a child process"}
	switch {
	case s.cfg.User == "":
		out = append(out, "identity: the child runs as this process's own account, because no render account is configured")
	case !canDropPrivilege():
		out = append(out, "identity: `"+s.cfg.User+"` is configured and cannot be dropped to, because this process cannot drop privilege here")
	default:
		out = append(out, "identity: the child runs as "+s.cfg.User)
	}
	out = append(out, networkEnforcement())
	out = append(out, platformNotes()...)
	out = append(out, "the parent keeps: module dispatch, template loading, and gpg decryption; "+
		"a template that calls an execution module still runs it in this process")
	return out
}

// Unenforced lists the controls this configuration asked for and this
// machine does not apply.
//
// Separate from Describe because the two have different readers.
// Describe is the whole picture, logged once; this is the subset that
// should raise an eyebrow, and the node logs it as a warning. A setting
// that is configured and inert is the failure this project keeps
// finding in its own settings table.
func (s *Sandbox) Unenforced() []string {
	var out []string
	if s.cfg.User != "" && !canDropPrivilege() {
		out = append(out, "`render_sandbox_user` is set to "+s.cfg.User+
			" and cannot be applied, because this process cannot drop privilege here; "+
			"the child runs as this account instead")
	}
	return out
}

// renderInChild runs one exchange, starting a child if there is none.
func (s *Sandbox) renderInChild(body string, stages []string, opts render.Options) (render.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.child == nil {
		c, err := start(s.cfg)
		if err != nil {
			return render.Result{}, err
		}
		s.child = c
	}

	res, err := s.child.render(body, stages, opts, s.cfg.Timeout)
	if err != nil && !errors.Is(err, errRender) {
		// A transport failure leaves the child in an unknown state, so
		// it goes rather than being reused. A render that merely failed
		// -- a template error, a YAML error -- is the child working
		// correctly and it stays.
		_ = s.child.close()
		s.child = nil
	}
	return res, err
}

// errRender marks an error that came back from the child as the outcome
// of the render itself rather than as a failure of the channel. It is
// matched with errors.Is and never printed.
var errRender = errors.New("render failed")

// renderFailed carries the child's message and answers to errRender, so
// that the distinction the parent needs costs the operator no words.
type renderFailed struct{ msg string }

func (e *renderFailed) Error() string { return e.msg }

func (e *renderFailed) Is(target error) bool { return target == errRender }

// childProcess is the running child and its pipes.
type childProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	pid    int
}

// start launches the child and reads its hello.
func start(cfg Config) (*childProcess, error) {
	exe := cfg.Exe
	if exe == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("the render sandbox cannot find this program to re-execute: %w", err)
		}
		exe = self
	}

	cmd := exec.Command(exe, cfg.Args...)
	// A deliberately bare environment. SPEC 25.4 asks for it for the
	// children `cmd` spawns, and the reasoning applies here first: the
	// child has no use for a hub credential, a proxy setting or a home
	// directory, and everything it is given is something that leaks if
	// the render is what goes wrong.
	cmd.Env = []string{"HALITE_RENDER_SANDBOX=1"}
	cmd.Stderr = cfg.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := applyConfinement(cmd, cfg); err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("the render sandbox could not start %s: %w", exe, err)
	}

	c := &childProcess{cmd: cmd, stdin: stdin, stdout: stdout, pid: cmd.Process.Pid}
	hello, err := readFrame(stdout)
	if err != nil {
		_ = c.close()
		return nil, fmt.Errorf("the render sandbox child said nothing: %w", err)
	}
	if hello.Kind != frameHello {
		_ = c.close()
		return nil, fmt.Errorf("the render sandbox child opened with %q rather than a hello", hello.Kind)
	}
	if hello.Version != codecVersion {
		_ = c.close()
		return nil, fmt.Errorf("the render sandbox child speaks protocol %d and this process speaks %d; "+
			"the two are the same binary in every supported arrangement, so this means one of them is not",
			hello.Version, codecVersion)
	}
	return c, nil
}

func (c *childProcess) close() error {
	_ = c.stdin.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	_ = c.stdout.Close()
	return c.cmd.Wait()
}

// render performs one exchange: the request, then the callbacks the
// child asks for, then the result.
func (c *childProcess) render(body string, stages []string, opts render.Options, timeout time.Duration) (render.Result, error) {
	f := newFiles()
	wire, err := encodeOptions(opts, f)
	if err != nil {
		return render.Result{}, fmt.Errorf("%s: the render sandbox cannot carry this render: %w", opts.File, err)
	}

	// A watchdog rather than a read deadline: an os.Pipe has no
	// deadline, and killing the process is what unblocks the read.
	done := make(chan struct{})
	defer close(done)
	timedOut := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(timeout):
			close(timedOut)
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Kill()
			}
		}
	}()

	req := frame{
		Kind:   frameRender,
		Body:   body,
		Stages: stages,
		Opts:   wire,
		Files:  f.names,
	}
	if err := writeFrame(c.stdin, req); err != nil {
		return render.Result{}, fmt.Errorf("%s: the render sandbox child stopped listening: %w", opts.File, err)
	}

	for {
		in, err := readFrame(c.stdout)
		if err != nil {
			select {
			case <-timedOut:
				return render.Result{}, fmt.Errorf("%s: the render sandbox child did not answer within %s and was killed",
					opts.File, timeout)
			default:
			}
			return render.Result{}, fmt.Errorf("%s: the render sandbox child stopped answering: %w", opts.File, err)
		}

		switch in.Kind {
		case frameResult:
			return c.result(in, opts)
		case frameLoad, frameCall, frameHas, frameUndefined:
			if err := c.serve(in, opts); err != nil {
				return render.Result{}, err
			}
		default:
			return render.Result{}, fmt.Errorf("%s: the render sandbox child sent a %q frame, which is not part of this protocol",
				opts.File, in.Kind)
		}
	}
}

// result turns the child's answer into a render.Result.
func (c *childProcess) result(in frame, opts render.Options) (render.Result, error) {
	if !in.OK {
		msg := in.Error
		if msg == "" {
			msg = "the render failed and said nothing about why"
		}
		// The child's words, exactly, and nothing of this package's.
		//
		// It used to be `fmt.Errorf("%w: %s", errRender, msg)`, which
		// reads fine in isolation and is wrong in place: the compiler
		// prefixes a diagnostic with the file unless the message already
		// names it, so every render error arrived as "web.sls: render
		// failed: web.sls:22:1: ...". A lab run found that; no test did,
		// because they all asked whether the message *contained* the
		// renderer's words.
		return render.Result{}, &renderFailed{msg: msg}
	}
	if in.Result == nil {
		return render.Result{}, fmt.Errorf("%s: the render sandbox child reported success with no result", opts.File)
	}
	f := restoreFiles(in.Files)
	out := render.Result{
		Text:     in.Result.Text,
		Pipeline: in.Result.Pipeline,
		Warnings: in.Result.Warnings,
	}
	if in.Result.Value != nil {
		v, err := decodeValue(*in.Result.Value, f)
		if err != nil {
			return render.Result{}, fmt.Errorf("%s: the render sandbox child sent a value this process cannot read: %w",
				opts.File, err)
		}
		out.Value = v
	}
	return out, nil
}

// serve answers one callback.
func (c *childProcess) serve(in frame, opts render.Options) error {
	f := restoreFiles(in.Files)

	switch in.Kind {
	case frameUndefined:
		if opts.OnUndefined != nil {
			pos, err := unposOf(in.Pos, f)
			if err != nil {
				return err
			}
			opts.OnUndefined(in.Name, pos)
		}
		return nil

	case frameLoad:
		reply := frame{Kind: frameReply, ID: in.ID}
		if opts.Loader == nil {
			reply.NotFound = true
		} else {
			src, display, err := opts.Loader.Load(in.Name)
			switch {
			case errors.Is(err, template.ErrNotFound):
				reply.NotFound = true
			case err != nil:
				reply.Error = err.Error()
			default:
				reply.OK, reply.Text, reply.Display = true, src, display
			}
		}
		return writeFrame(c.stdin, reply)

	case frameHas:
		reply := frame{Kind: frameReply, ID: in.ID}
		reply.OK = opts.Salt != nil && opts.Salt.HasModule(in.Name)
		return writeFrame(c.stdin, reply)

	case frameCall:
		reply := frame{Kind: frameReply, ID: in.ID}
		if opts.Salt == nil {
			reply.Error = "this render has no execution module dispatcher"
			return writeFrame(c.stdin, reply)
		}
		args, err := decodeArgs(in.Args, f)
		if err != nil {
			return err
		}
		kwargs, err := decodeKwargs(in.Kwargs, f)
		if err != nil {
			return err
		}
		// The dispatch happens here, in the privileged parent, which is
		// the point of the arrangement.
		v, callErr := opts.Salt.CallModule(in.Name, args, kwargs)
		if callErr != nil {
			reply.Error = callErr.Error()
			return writeFrame(c.stdin, reply)
		}
		out := newFiles()
		node, err := encodeValue(v, out)
		if err != nil {
			reply.Error = fmt.Sprintf("%s returned a %T, which the render sandbox cannot carry", in.Name, v)
			return writeFrame(c.stdin, reply)
		}
		reply.OK, reply.Value, reply.Files = true, &node, out.names
		return writeFrame(c.stdin, reply)
	}
	return fmt.Errorf("a %q frame reached the callback server", in.Kind)
}

// PID reports the child's process id, or zero when none is running. It
// is what a test uses to prove the render really happened somewhere
// else.
func (s *Sandbox) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child == nil {
		return 0
	}
	return s.child.pid
}

// sortedKeys gives a map's keys in a stable order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func timeoutOf(nanos int64) time.Duration { return time.Duration(nanos) }
