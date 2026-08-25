// Package bridge is the extension protocol of SPEC section 24.
//
// Salt's extensibility is a Python file dropped in `_modules/` on the
// file server, which the agent imports and runs in process, as root,
// with no signature requirement. SPEC 24.1 calls that a code
// distribution channel, and this package is what replaces it: an
// extension is a separate executable speaking JSON over stdio, started
// by the host, bounded by a sandbox, and killed when it misbehaves.
//
// The properties that matter are structural rather than careful. A
// hung extension cannot hang the agent because it is a process. A
// crashing extension cannot take the agent down for the same reason. An
// extension that needs root has to say so, and one that does not runs
// without it.
package bridge

import (
	"encoding/json"
	"fmt"
	"io"
)

// ProtocolVersion is what the host offers in its hello frame.
//
// A single integer rather than a range: an extension either speaks this
// protocol or it does not, and a host that negotiates down to a version
// it no longer tests is a host with an untested path in it.
const ProtocolVersion = 1

// MaxFrameSize bounds one frame.
//
// An extension is a separate process and its output is not trusted. A
// frame that claims a gigabyte is either broken or hostile, and reading
// it would be the host running out of memory on the extension's say-so.
const MaxFrameSize = 16 << 20

// Frame kinds. The host sends `hello` and `call`; the extension sends
// everything else.
const (
	KindHello    = "hello"
	KindHelloOK  = "hello_ok"
	KindCall     = "call"
	KindLog      = "log"
	KindProgress = "progress"
	KindEvent    = "event"
	KindResult   = "result"
	// KindShutdown asks an extension to exit. It is a courtesy: the
	// host kills it either way, and this only lets a well-behaved one
	// flush and close cleanly.
	KindShutdown = "shutdown"
)

// Frame is one message in either direction.
//
// One struct rather than a type per kind, because the wire carries one
// shape and a reader must be able to see what arrived before deciding
// what it is. The fields a kind does not use are absent.
type Frame struct {
	Kind string `json:"kind"`
	// ID ties a response to its call. Absent on `hello`.
	ID string `json:"id,omitempty"`

	// hello.
	Protocol int    `json:"protocol,omitempty"`
	Ext      string `json:"extension_kind,omitempty"`

	// hello_ok.
	Name      string            `json:"name,omitempty"`
	Version   string            `json:"version,omitempty"`
	Functions []json.RawMessage `json:"functions,omitempty"`
	// Declares is what the extension says it needs — `root`, `network`,
	// or a path it writes outside its working directory. The sandbox
	// grants nothing that is not declared, and SPEC 24.3 makes the
	// declaration visible in `sys.list_extensions`.
	Declares []string `json:"declares,omitempty"`

	// call.
	Function string          `json:"function,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Kwargs   json.RawMessage `json:"kwargs,omitempty"`
	Context  *CallContext    `json:"context,omitempty"`

	// log, progress.
	Level   string `json:"level,omitempty"`
	Message string `json:"message,omitempty"`
	Done    int    `json:"done,omitempty"`
	Total   int    `json:"total,omitempty"`

	// event.
	Tag  string          `json:"tag,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`

	// result.
	OK    bool            `json:"ok,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
	Error string          `json:"error,omitempty"`
}

// CallContext is what an extension is told about the run it is part of.
//
// Deliberately thin. An extension gets the node's identity and the job
// it belongs to, and not the pillar: an extension that wants a secret
// is given it as an argument by the state that calls it, which keeps
// the decision to hand it over in the tree where somebody can read it.
type CallContext struct {
	NodeID string `json:"node_id,omitempty"`
	JobID  string `json:"job_id,omitempty"`
	Env    string `json:"env,omitempty"`
	// Test marks a run that must change nothing, which an extension
	// honours the same way a built-in module does.
	Test bool `json:"test,omitempty"`
	// Timeout is how long the host will wait, in seconds, so an
	// extension can bound its own work rather than being killed
	// part-way through.
	Timeout float64 `json:"timeout_seconds,omitempty"`
}

// WriteFrame writes one length-prefixed frame.
//
// Length-prefixed rather than newline-delimited: a frame boundary must
// not depend on an extension never emitting a newline inside a string,
// and an extension written in a language whose JSON encoder pretty
// prints would otherwise break the stream in a way that looks like a
// protocol error in the host.
func WriteFrame(w io.Writer, f Frame) error {
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(body) > MaxFrameSize {
		return fmt.Errorf("a %d byte frame, past the %d byte limit", len(body), MaxFrameSize)
	}
	header := [4]byte{
		byte(len(body) >> 24), byte(len(body) >> 16),
		byte(len(body) >> 8), byte(len(body)),
	}
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// ReadFrame reads one.
func ReadFrame(r io.Reader) (Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Frame{}, err
	}
	size := int(header[0])<<24 | int(header[1])<<16 | int(header[2])<<8 | int(header[3])
	if size < 0 || size > MaxFrameSize {
		// Refused before anything is allocated for it.
		return Frame{}, fmt.Errorf("the extension announced a %d byte frame, past the %d byte limit",
			size, MaxFrameSize)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return Frame{}, err
	}
	var f Frame
	if err := json.Unmarshal(body, &f); err != nil {
		return Frame{}, fmt.Errorf("the extension sent a frame that is not readable: %w", err)
	}
	if f.Kind == "" {
		return Frame{}, fmt.Errorf("the extension sent a frame with no kind")
	}
	return f, nil
}
