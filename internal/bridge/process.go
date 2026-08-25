package bridge

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Options is how one extension process is started.
type Options struct {
	// Path is the executable.
	Path string
	// Kind is the extension kind named in the hello frame — `module`,
	// `returner`, and the rest of SPEC 24.2's list.
	Kind string
	// WorkDir is the process's working directory and, under the
	// sandbox, the only place it may write.
	WorkDir string
	// Env is the whole environment. Not appended to the host's: an
	// extension inheriting the agent's environment inherits whatever
	// credentials are in it.
	Env []string
	// Timeout bounds one call. Zero takes DefaultTimeout.
	Timeout time.Duration
	// Sandbox restricts the process. Nil runs it with the host's own
	// identity and limits, which is what a development build does and
	// what production must not.
	Sandbox *Sandbox
	// OnLog, OnProgress, and OnEvent receive the streaming frames an
	// extension may emit before its result. Nil discards them.
	OnLog      func(level, message string)
	OnProgress func(done, total int, message string)
	OnEvent    func(tag string, data json.RawMessage)
	// Stderr receives whatever the extension writes there, line by
	// line. An extension that crashes says why on stderr, and losing
	// that leaves "the extension exited" as the whole diagnosis.
	Stderr func(line string)
}

// DefaultTimeout bounds a call that names no timeout of its own.
const DefaultTimeout = 60 * time.Second

// Process is one running extension.
//
// One call at a time. SPEC 24.2 says concurrency is by process pool
// rather than in-process threading, and the reason is here: an
// extension is arbitrary code in another language, and asking it to be
// thread-safe is asking for a class of bug the host cannot see.
type Process struct {
	opts Options

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	mu sync.Mutex
	// info is what the extension said about itself at handshake.
	info Info
	// dead records that this process has been killed for a protocol
	// violation or a timeout, so it is not used again.
	dead    bool
	deadFor string
	nextID  int
}

// Info is what an extension declared about itself.
type Info struct {
	Name    string
	Version string
	// Functions are the signatures of section 15.6, as the extension
	// sent them. Carried as raw JSON because the host does not need to
	// understand a signature to route a call, and a host that parses
	// one is a host that can refuse an extension over a field it
	// happened to spell differently.
	Functions []json.RawMessage
	// Declares is what it says it needs.
	Declares []string
}

// Start launches the extension and completes the handshake.
func Start(ctx context.Context, opts Options) (*Process, error) {
	if opts.Path == "" {
		return nil, errors.New("an extension needs an executable")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}

	cmd := exec.Command(opts.Path)
	cmd.Dir = opts.WorkDir
	// The whole environment, never the host's. An extension that
	// inherits the agent's environment inherits whatever is in it.
	cmd.Env = opts.Env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	if err := opts.Sandbox.apply(cmd); err != nil {
		return nil, err
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", opts.Path, err)
	}

	p := &Process{opts: opts, cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	go p.drainStderr(stderr)

	if err := p.handshake(ctx); err != nil {
		p.Kill("the handshake failed")
		return nil, err
	}
	return p, nil
}

// Info reports what the extension said about itself.
func (p *Process) Info() Info {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

// handshake exchanges hello frames.
func (p *Process) handshake(ctx context.Context) error {
	if err := WriteFrame(p.stdin, Frame{
		Kind: KindHello, Protocol: ProtocolVersion, Ext: p.opts.Kind,
	}); err != nil {
		return fmt.Errorf("greeting %s: %w", p.opts.Path, err)
	}

	frame, err := p.readWithin(ctx, p.opts.Timeout)
	if err != nil {
		return fmt.Errorf("%s did not answer the handshake: %w", p.opts.Path, err)
	}
	if frame.Kind != KindHelloOK {
		return fmt.Errorf("%s answered the handshake with a %q frame", p.opts.Path, frame.Kind)
	}
	if frame.Name == "" {
		return fmt.Errorf("%s did not name itself", p.opts.Path)
	}
	p.mu.Lock()
	p.info = Info{
		Name: frame.Name, Version: frame.Version,
		Functions: frame.Functions, Declares: frame.Declares,
	}
	p.mu.Unlock()
	return nil
}

// Call invokes one function.
//
// A protocol violation kills the process rather than failing the call.
// An extension that sent something the host cannot read has lost track
// of the stream, and the next frame it sends is a response to a
// question nobody asked.
func (p *Process) Call(ctx context.Context, function string, args, kwargs any, callCtx *CallContext) (json.RawMessage, error) {
	p.mu.Lock()
	if p.dead {
		reason := p.deadFor
		p.mu.Unlock()
		return nil, fmt.Errorf("this extension was stopped: %s", reason)
	}
	p.nextID++
	id := fmt.Sprintf("%d", p.nextID)
	p.mu.Unlock()

	timeout := p.opts.Timeout
	if callCtx != nil && callCtx.Timeout > 0 {
		timeout = time.Duration(callCtx.Timeout * float64(time.Second))
	}

	encodedArgs, err := encodeOrNull(args)
	if err != nil {
		return nil, err
	}
	encodedKwargs, err := encodeOrNull(kwargs)
	if err != nil {
		return nil, err
	}
	if err := WriteFrame(p.stdin, Frame{
		Kind: KindCall, ID: id, Function: function,
		Args: encodedArgs, Kwargs: encodedKwargs, Context: callCtx,
	}); err != nil {
		p.Kill("the call could not be written")
		return nil, err
	}

	deadline := time.Now().Add(timeout)
	for {
		frame, err := p.readWithin(ctx, time.Until(deadline))
		if err != nil {
			p.Kill(fmt.Sprintf("%s: %v", function, err))
			return nil, fmt.Errorf("%s: %w", function, err)
		}
		// A frame for another call is a stream that has lost its place.
		if frame.ID != "" && frame.ID != id {
			p.Kill("a frame arrived for a call that was not in progress")
			return nil, fmt.Errorf("%s answered call %s while %s was in progress",
				p.opts.Path, frame.ID, id)
		}
		switch frame.Kind {
		case KindLog:
			if p.opts.OnLog != nil {
				p.opts.OnLog(frame.Level, frame.Message)
			}
		case KindProgress:
			if p.opts.OnProgress != nil {
				p.opts.OnProgress(frame.Done, frame.Total, frame.Message)
			}
		case KindEvent:
			if p.opts.OnEvent != nil {
				p.opts.OnEvent(frame.Tag, frame.Data)
			}
		case KindResult:
			if !frame.OK {
				message := frame.Error
				if message == "" {
					message = "the extension reported a failure and said nothing about it"
				}
				return nil, fmt.Errorf("%s: %s", function, message)
			}
			return frame.Value, nil
		default:
			p.Kill(fmt.Sprintf("an unexpected %q frame", frame.Kind))
			return nil, fmt.Errorf("%s sent a %q frame while answering a call",
				p.opts.Path, frame.Kind)
		}
	}
}

// readWithin reads one frame, bounded.
//
// The read runs in a goroutine because a pipe read cannot be cancelled;
// what the timeout does is stop waiting, and the kill that follows is
// what actually ends the read.
func (p *Process) readWithin(ctx context.Context, timeout time.Duration) (Frame, error) {
	if timeout <= 0 {
		return Frame{}, errors.New("the extension ran out of time")
	}
	type outcome struct {
		frame Frame
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		frame, err := ReadFrame(p.stdout)
		done <- outcome{frame, err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case got := <-done:
		if got.err != nil {
			if errors.Is(got.err, io.EOF) || errors.Is(got.err, io.ErrUnexpectedEOF) {
				return Frame{}, errors.New("the extension exited without answering")
			}
			return Frame{}, got.err
		}
		return got.frame, nil
	case <-timer.C:
		return Frame{}, fmt.Errorf("the extension did not answer within %s", timeout)
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	}
}

// Kill ends the process.
//
// SIGTERM then SIGKILL, as SPEC 24.3 asks: an extension holding a lock
// or a partial write is given the chance to finish, and one that
// ignores it is stopped anyway. The grace period is short because the
// caller is waiting.
func (p *Process) Kill(reason string) {
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return
	}
	p.dead, p.deadFor = true, reason
	p.mu.Unlock()

	_ = p.stdin.Close()
	if p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)

	exited := make(chan struct{})
	go func() { _, _ = p.cmd.Process.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		_ = p.cmd.Process.Kill()
		<-exited
	}
}

// Close asks the extension to exit and then makes sure it has.
func (p *Process) Close() {
	p.mu.Lock()
	dead := p.dead
	p.mu.Unlock()
	if !dead {
		_ = WriteFrame(p.stdin, Frame{Kind: KindShutdown})
	}
	p.Kill("the host is shutting down")
}

// Dead reports whether this process has been stopped, and why.
func (p *Process) Dead() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead, p.deadFor
}

func (p *Process) drainStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), 64<<10)
	for scanner.Scan() {
		if p.opts.Stderr != nil {
			p.opts.Stderr(scanner.Text())
		}
	}
}

func encodeOrNull(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return raw, nil
}
