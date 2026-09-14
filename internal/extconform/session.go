package extconform

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/ext"
)

// session is one extension process and the pipes to it.
//
// Deliberately not `bridge.Process`. That one is written to run an
// extension: it enforces the protocol, kills a process that breaks it,
// and turns everything into an error for the caller. Here the breaking
// is the subject, so the frames are read and written directly and
// nothing is killed for being wrong.
type session struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader

	timeout time.Duration

	mu      sync.Mutex
	stderr  []string
	exited  chan struct{}
	waitErr error
	// lost marks a session whose reader is no longer ours.
	//
	// A read that times out leaves a goroutine blocked on the stream,
	// and a second read would start another on the same one: two
	// readers racing over one bufio.Reader, framing decided by
	// whichever wakes first. No check reads after a timeout today.
	// Saying so in the type is what keeps that true.
	lost bool
}

// looseFrame is a frame read permissively.
//
// The host's own `ext.Frame` is strict, which is right for running an
// extension and wrong for diagnosing one: a parameter type sent as a
// number fails the whole decode, and "the frame is not readable" is a
// worse answer than naming the parameter. Every field that a candidate
// might get wrong is read as raw JSON here so that it can be described.
type looseFrame struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`

	Declares  []string         `json:"declares"`
	Functions []looseSignature `json:"functions"`

	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value"`
	Error string          `json:"error"`

	Level   string `json:"level"`
	Message string `json:"message"`
	Tag     string `json:"tag"`
}

type looseSignature struct {
	Module   string       `json:"module"`
	Function string       `json:"function"`
	Params   []looseParam `json:"params"`
}

type looseParam struct {
	Name string `json:"name"`
	// Raw, so that a number can be reported as one rather than
	// stopping the decode.
	Type json.RawMessage `json:"type"`
}

// start runs the extension.
func start(ctx context.Context, opts Options) (*session, error) {
	cmd := exec.CommandContext(ctx, opts.Path)
	cmd.Env = opts.Env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", opts.Path, err)
	}

	s := &session{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewReader(stdout),
		timeout: opts.timeout(),
		exited:  make(chan struct{}),
	}

	// Kept, because an extension that dies says why here and losing it
	// leaves "it exited" as the whole diagnosis.
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			s.mu.Lock()
			if len(s.stderr) < 20 {
				s.stderr = append(s.stderr, scanner.Text())
			}
			s.mu.Unlock()
		}
	}()
	go func() {
		s.waitErr = cmd.Wait()
		close(s.exited)
	}()
	return s, nil
}

func (s *session) write(frame ext.Frame) error {
	return ext.WriteFrame(s.stdin, frame)
}

// writeRaw sends bytes that ext.Frame could not express, for the checks
// that are about what a host may send rather than what it does.
func (s *session) writeRaw(body []byte) error {
	header := [4]byte{
		byte(len(body) >> 24), byte(len(body) >> 16),
		byte(len(body) >> 8), byte(len(body)),
	}
	if _, err := s.stdin.Write(header[:]); err != nil {
		return err
	}
	_, err := s.stdin.Write(body)
	return err
}

// errTimeout is a read that did not arrive.
var errTimeout = errors.New("nothing arrived within the timeout")

// errLost is a read after one that timed out.
var errLost = errors.New("an earlier read timed out, so this stream is no longer in step")

// read takes one frame, bounded.
func (s *session) read() (*looseFrame, error) {
	s.mu.Lock()
	lost := s.lost
	s.mu.Unlock()
	if lost {
		return nil, errLost
	}
	type answer struct {
		frame *looseFrame
		err   error
	}
	done := make(chan answer, 1)
	go func() {
		var header [4]byte
		if _, err := io.ReadFull(s.stdout, header[:]); err != nil {
			done <- answer{nil, err}
			return
		}
		size := int(header[0])<<24 | int(header[1])<<16 | int(header[2])<<8 | int(header[3])
		if size < 0 || size > ext.MaxFrameSize {
			done <- answer{nil, fmt.Errorf("it announced a %d byte frame, past the %d byte limit",
				size, ext.MaxFrameSize)}
			return
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(s.stdout, body); err != nil {
			done <- answer{nil, err}
			return
		}
		var frame looseFrame
		if err := json.Unmarshal(body, &frame); err != nil {
			done <- answer{nil, fmt.Errorf("it sent %d bytes that are not a JSON object: %w",
				len(body), err)}
			return
		}
		done <- answer{&frame, nil}
	}()

	select {
	case a := <-done:
		return a.frame, a.err
	case <-time.After(s.timeout):
		s.mu.Lock()
		s.lost = true
		s.mu.Unlock()
		return nil, errTimeout
	}
}

// drainUntilResult reads streaming frames until the result, and reports
// what it saw on the way.
func (s *session) drainUntilResult() (result *looseFrame, streamed []*looseFrame, err error) {
	for {
		frame, readErr := s.read()
		if readErr != nil {
			return nil, streamed, readErr
		}
		switch frame.Kind {
		case ext.FrameResult:
			return frame, streamed, nil
		case ext.FrameLog, ext.FrameProgress, ext.FrameEvent:
			streamed = append(streamed, frame)
			// Bounded: an extension that streams without end is a
			// different failure, and waiting for it is not a diagnosis.
			if len(streamed) > 64 {
				return nil, streamed, fmt.Errorf("it sent more than 64 frames without a result")
			}
		default:
			return nil, streamed, fmt.Errorf("it sent a %q frame where a result belongs", frame.Kind)
		}
	}
}

// waitForExit reports whether the process ended within the window.
func (s *session) waitForExit(within time.Duration) bool {
	select {
	case <-s.exited:
		return true
	case <-time.After(within):
		return false
	}
}

// stderrTail is what the process said to a person, for a diagnosis that
// would otherwise be "it exited".
func (s *session) stderrTail() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stderr) == 0 {
		return ""
	}
	return strings.Join(s.stderr, "; ")
}

func (s *session) close() {
	_ = s.stdin.Close()
	if !s.waitForExit(2 * time.Second) {
		_ = s.cmd.Process.Kill()
	}
}

// describeReadFailure turns a read error into something an author can
// act on, using whatever the process said on its way out.
func describeReadFailure(err error, s *session) string {
	var said string
	if tail := s.stderrTail(); tail != "" {
		said = "; it wrote to stderr: " + tail
	}
	switch {
	case errors.Is(err, errTimeout):
		return "nothing arrived within the timeout" + said +
			". A writer that buffers until exit looks exactly like this: flush after every frame."
	case errors.Is(err, io.EOF):
		return "it exited without answering" + said
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "it exited part-way through a frame" + said
	case errors.Is(err, errLost):
		return "an earlier exchange timed out, so nothing after it could be established" + said
	}
	return err.Error() + said
}
