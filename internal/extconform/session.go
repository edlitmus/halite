package extconform

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	// startTimeout bounds the first read, the one wait that includes
	// starting the process. See DefaultStartTimeout.
	startTimeout time.Duration

	// stdinRead is our own copy of the read end of the child's stdin,
	// held only until the first answer is settled.
	//
	// It is what tells a slow start from a silent extension. When the
	// first answer does not arrive, the question that matters is
	// whether the process has read the hello yet. If the bytes are
	// still in the pipe it had not got that far -- it was still
	// starting, or it never reads stdin -- and nothing it did can be
	// called buffering. If they are gone it was running, and closing
	// its stdin will show whether an answer was being held. Nothing
	// else visible from outside the process separates the two; the
	// message this replaced guessed, and guessed "buffering".
	//
	// Not held for the whole session, because a parent holding a read
	// end means a write to an exited child lands in the pipe buffer
	// instead of failing, and the later checks report a write that
	// cannot be made as exactly that.
	stdinRead *os.File

	mu      sync.Mutex
	stderr  []string
	exited  chan struct{}
	waitErr error
	// sentFirst counts what was written before the first answer, so
	// the probe can tell "none of it was read" from "some of it was".
	sentFirst int
	// reads counts reads begun, so that the first can be told apart.
	reads int
	// lost marks a session whose reader is no longer ours.
	//
	// A read that times out leaves a goroutine blocked on the stream,
	// and a second read would start another on the same one: two
	// readers racing over one bufio.Reader, framing decided by
	// whichever wakes first. No check reads after a timeout today.
	// Saying so in the type is what keeps that true.
	lost bool
	// silence is what the probe established about a first answer that
	// did not come. Empty until it has run.
	silence string
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

	// A pipe of our own rather than StdinPipe, which closes the read
	// end in the parent at Start: that read end is the probe stdinRead
	// describes.
	stdinRead, stdin, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	abandon := func() {
		_ = stdinRead.Close()
		_ = stdin.Close()
	}
	cmd.Stdin = stdinRead
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		abandon()
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		abandon()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		abandon()
		return nil, fmt.Errorf("starting %s: %w", opts.Path, err)
	}

	s := &session{
		cmd:          cmd,
		stdin:        stdin,
		stdout:       bufio.NewReader(stdout),
		timeout:      opts.timeout(),
		startTimeout: opts.startTimeout(),
		stdinRead:    stdinRead,
		exited:       make(chan struct{}),
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
	return ext.WriteFrame(countingWriter{s}, frame)
}

// writeRaw sends bytes that ext.Frame could not express, for the checks
// that are about what a host may send rather than what it does.
func (s *session) writeRaw(body []byte) error {
	header := [4]byte{
		byte(len(body) >> 24), byte(len(body) >> 16),
		byte(len(body) >> 8), byte(len(body)),
	}
	w := countingWriter{s}
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// countingWriter is stdin, counting what is sent while the first answer
// is outstanding: the probe needs to know how much the process could
// have read.
type countingWriter struct{ s *session }

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.s.stdin.Write(p)
	c.s.mu.Lock()
	if c.s.reads == 0 {
		c.s.sentFirst += n
	}
	c.s.mu.Unlock()
	return n, err
}

// takeStdinRead hands over the held read end, once: to be closed when
// the first answer arrives, or to be probed when it does not.
func (s *session) takeStdinRead() *os.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.stdinRead
	s.stdinRead = nil
	return r
}

// errTimeout is a read that did not arrive.
var errTimeout = errors.New("nothing arrived within the timeout")

// errLost is a read after one that timed out.
var errLost = errors.New("an earlier read timed out, so this stream is no longer in step")

type readAnswer struct {
	frame *looseFrame
	err   error
}

// read takes one frame, bounded.
//
// The first read of a session is bounded by the start allowance rather
// than the exchange timeout, because its wait began before the process
// existed. Every later one is a round trip with a process that has
// already answered once.
func (s *session) read() (*looseFrame, error) {
	s.mu.Lock()
	lost := s.lost
	first := s.reads == 0
	s.reads++
	s.mu.Unlock()
	if lost {
		return nil, errLost
	}
	done := make(chan readAnswer, 1)
	go func() {
		var header [4]byte
		if _, err := io.ReadFull(s.stdout, header[:]); err != nil {
			done <- readAnswer{nil, err}
			return
		}
		size := int(header[0])<<24 | int(header[1])<<16 | int(header[2])<<8 | int(header[3])
		if size < 0 || size > ext.MaxFrameSize {
			done <- readAnswer{nil, fmt.Errorf("it announced a %d byte frame, past the %d byte limit",
				size, ext.MaxFrameSize)}
			return
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(s.stdout, body); err != nil {
			done <- readAnswer{nil, err}
			return
		}
		var frame looseFrame
		if err := json.Unmarshal(body, &frame); err != nil {
			done <- readAnswer{nil, fmt.Errorf("it sent %d bytes that are not a JSON object: %w",
				len(body), err)}
			return
		}
		done <- readAnswer{&frame, nil}
	}()

	within := s.timeout
	if first {
		within = s.startTimeout
	}
	select {
	case a := <-done:
		if first {
			if r := s.takeStdinRead(); r != nil {
				_ = r.Close()
			}
		}
		return a.frame, a.err
	case <-time.After(within):
		s.mu.Lock()
		s.lost = true
		s.mu.Unlock()
		if first {
			s.probeSilence(within, done)
		}
		return nil, errTimeout
	}
}

// probeSilence works out why a first answer did not come, and records
// it for describeReadFailure.
//
// It spends the session: stdin is closed, and the hello may be taken
// back out of the pipe. Nothing reads from a session after a timeout
// (see lost), so nothing is lost by it.
//
// The method. Start a read on our copy of the stdin read end, then
// close the write end. That read collects whatever is still in the
// pipe and then sees EOF, so what it returns is exactly what the
// process had not read -- no timing involved, and nothing platform
// specific: a closed write end is EOF on every system this builds for,
// Windows included. All of the hello still there means the process had
// not reached it. None of it means the process read it, and the frame
// read already waiting on stdout then shows what closing stdin made it
// do: a runtime flushes a buffered stdout on its way out, so an answer
// arriving now is an answer that was written and held.
//
// One race, stated rather than hidden: a process that reaches its
// first read in the same instant as the probe may take the hello
// instead of it. That is a process which was, by then, running, and is
// reported as one.
func (s *session) probeSilence(waited time.Duration, pending <-chan readAnswer) {
	s.mu.Lock()
	sent := s.sentFirst
	s.mu.Unlock()
	r := s.takeStdinRead()

	leftover := make(chan int, 1)
	if r != nil {
		go func() {
			n, _ := io.Copy(io.Discard, r)
			leftover <- int(n)
		}()
	}
	_ = s.stdin.Close()

	unread := -1 // could not be established
	if r != nil {
		select {
		case unread = <-leftover:
		case <-time.After(s.timeout):
			// Something else holds the write end, so EOF is not coming.
			// Not a diagnosis of the extension; the answer stays unknown.
		}
		_ = r.Close()
	}

	allowance := fmt.Sprintf("within the %s start allowance", waited)
	if sent > 0 && unread == sent {
		s.setSilence("it had not read the hello " + allowance + ": it was still starting, " +
			"or it does not read stdin. Nothing it sent was wrong, and nothing was established. " +
			"A loaded machine or a slow interpreter does this to a conforming extension; " +
			"run it again, or with a longer --start-timeout")
		return
	}

	read := "it read the hello but"
	if unread < 0 {
		read = "it may or may not have read the hello (that could not be established), and"
	}
	select {
	case a := <-pending:
		switch {
		case a.err == nil && unread == 0:
			s.setSilence(read + " sent nothing " + allowance + "; its answer arrived only once " +
				"its stdin was closed, which is a writer that buffers until exit: flush after every frame")
		case a.err == nil:
			// Without knowing the hello was read, an answer after the
			// close is also what a process still starting when the
			// allowance ran out produces: the hello was waiting in the
			// pipe for it. Both are said, because only the probe could
			// have chosen between them.
			s.setSilence(read + " sent nothing " + allowance + "; its answer arrived only once " +
				"its stdin was closed. Either it holds its output until exit -- flush after every " +
				"frame -- or it was still starting; a longer --start-timeout tells them apart")
		case errors.Is(a.err, io.EOF), errors.Is(a.err, io.ErrUnexpectedEOF):
			s.setSilence(read + " sent nothing " + allowance + ", and exited without answering " +
				"when its stdin was closed")
		default:
			s.setSilence(read + " sent nothing " + allowance + "; once its stdin was closed it " +
				"sent something unreadable: " + a.err.Error())
		}
	case <-time.After(s.timeout):
		s.setSilence(read + " sent nothing " + allowance + ", and nothing more when its stdin " +
			"was closed: it is stuck before answering, or busy with something that is not the hello")
	}
}

func (s *session) setSilence(why string) {
	s.mu.Lock()
	s.silence = why
	s.mu.Unlock()
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
	if r := s.takeStdinRead(); r != nil {
		_ = r.Close()
	}
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
	s.mu.Lock()
	silence := s.silence
	s.mu.Unlock()
	switch {
	case errors.Is(err, errTimeout) && silence != "":
		// A first answer that never came, already probed.
		return silence + said
	case errors.Is(err, errTimeout):
		// A later one. The process answered the handshake, so it was
		// running and its writer had delivered at least once: this is
		// a handler that never answered, or one whose answer is held.
		// Both are named, because nothing from outside tells them
		// apart once the session is past its first frame -- and
		// closing stdin to find out would cost the checks after it.
		return fmt.Sprintf("nothing arrived within the %s timeout", s.timeout) + said +
			". It answered the handshake, so it was running: the handler never answered, " +
			"or its answer is held in a buffer -- flush after every frame."
	case errors.Is(err, io.EOF):
		return "it exited without answering" + said
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "it exited part-way through a frame" + said
	case errors.Is(err, errLost):
		return "an earlier exchange timed out, so nothing after it could be established" + said
	}
	return err.Error() + said
}
