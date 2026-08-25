package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// Extension is the other side of the protocol: what an extension
// written in Go implements.
//
// It is here rather than in a separate module because the reference
// bridges of SPEC 24.4 ship in this tree, and because an extension and
// the host disagreeing about the protocol is the failure this package
// exists to make impossible.
type Extension struct {
	// Name and Version identify it in the handshake and in
	// `sys.list_extensions`.
	Name    string
	Version string
	// Kind is what it provides. The host names the kind it wants in
	// the hello frame, and an extension asked for a kind it does not
	// provide refuses rather than pretending.
	Kind string
	// Functions are the signatures of section 15.6, already encoded.
	Functions []json.RawMessage
	// Declares is what it needs: `root`, `network`. Anything not
	// declared is not granted.
	Declares []string
	// Handler runs one call.
	Handler func(Call) (any, error)
}

// Call is one invocation, as the extension sees it.
type Call struct {
	Function string
	Args     json.RawMessage
	Kwargs   json.RawMessage
	Context  CallContext

	// Log, Progress, and Event emit the streaming frames of SPEC 24.2
	// before the result. They are safe to call from the handler and
	// nowhere else: the protocol is one frame at a time, and a
	// goroutine emitting after the result has answered a question that
	// is closed.
	Log      func(level, message string)
	Progress func(done, total int, message string)
	Event    func(tag string, data any)
}

// Serve runs the extension against a host on stdin and stdout.
//
// Everything the extension wants to say to a person goes to stderr:
// stdout is the protocol, and a stray `fmt.Println` in an extension
// would otherwise be a frame the host cannot read and a process it
// kills.
func (e *Extension) Serve() error {
	return e.serve(os.Stdin, os.Stdout)
}

func (e *Extension) serve(in io.Reader, out io.Writer) error {
	var mu sync.Mutex
	write := func(f Frame) error {
		mu.Lock()
		defer mu.Unlock()
		return WriteFrame(out, f)
	}

	hello, err := ReadFrame(in)
	if err != nil {
		return err
	}
	if hello.Kind != KindHello {
		return fmt.Errorf("the host opened with a %q frame", hello.Kind)
	}
	if hello.Protocol != ProtocolVersion {
		return fmt.Errorf("the host speaks protocol %d and this extension speaks %d",
			hello.Protocol, ProtocolVersion)
	}
	if e.Kind != "" && hello.Ext != "" && hello.Ext != e.Kind {
		return fmt.Errorf("the host asked for a %q extension and this one is %q", hello.Ext, e.Kind)
	}
	if err := write(Frame{
		Kind: KindHelloOK, Name: e.Name, Version: e.Version,
		Functions: e.Functions, Declares: e.Declares,
	}); err != nil {
		return err
	}

	for {
		frame, err := ReadFrame(in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		switch frame.Kind {
		case KindShutdown:
			return nil
		case KindCall:
			e.answer(write, frame)
		default:
			return fmt.Errorf("the host sent a %q frame", frame.Kind)
		}
	}
}

// answer runs one call and writes exactly one result frame.
//
// A handler that panics produces a failed result rather than taking the
// extension down: the host would report "the extension exited without
// answering", which is true and tells nobody which function did it.
func (e *Extension) answer(write func(Frame) error, frame Frame) {
	id := frame.ID
	result := Frame{Kind: KindResult, ID: id}

	defer func() {
		if r := recover(); r != nil {
			result = Frame{Kind: KindResult, ID: id,
				Error: fmt.Sprintf("%s panicked: %v", frame.Function, r)}
		}
		_ = write(result)
	}()

	if e.Handler == nil {
		result.Error = "this extension provides no functions"
		return
	}
	call := Call{
		Function: frame.Function, Args: frame.Args, Kwargs: frame.Kwargs,
		Log: func(level, message string) {
			_ = write(Frame{Kind: KindLog, ID: id, Level: level, Message: message})
		},
		Progress: func(done, total int, message string) {
			_ = write(Frame{Kind: KindProgress, ID: id, Done: done, Total: total, Message: message})
		},
		Event: func(tag string, data any) {
			encoded, err := json.Marshal(data)
			if err != nil {
				return
			}
			_ = write(Frame{Kind: KindEvent, ID: id, Tag: tag, Data: encoded})
		},
	}
	if frame.Context != nil {
		call.Context = *frame.Context
	}

	value, err := e.Handler(call)
	if err != nil {
		result.Error = err.Error()
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		result.Error = "the result could not be encoded: " + err.Error()
		return
	}
	result.OK, result.Value = true, encoded
}
