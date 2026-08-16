// Package logging gives the two daemons a level and a destination, so an
// operator can decide how much a control plane says and where it says it
// without editing the code.
//
// The line stays the one the fleet has always produced — prefix, date,
// message — with a level token in front of the message:
//
//	halite-master 2026/08/16 10:18:22 INFO  hello from "web1"
//	halite-master 2026/08/16 10:18:23 WARN  throttling enrollment from 10.0.0.5
//
// Anything already reading these logs keeps working, which is the reason
// this is a wrapper around log.Logger rather than a structured logger:
// the daemons run under daemon(8) and journald, and both of those treat a
// log line as text.
package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Level is how much a daemon says. The zero value is Error, so a Logger
// that was never configured still reports the things that matter.
type Level int

const (
	Error Level = iota
	Warn
	Info
	Debug
)

// DefaultLevel is what both daemons run at when nothing says otherwise:
// the lifecycle and per-job lines the fleet has always printed.
const DefaultLevel = Info

var levelNames = [...]string{Error: "ERROR", Warn: "WARN", Info: "INFO", Debug: "DEBUG"}

func (l Level) String() string {
	if l < Error || int(l) >= len(levelNames) {
		return fmt.Sprintf("Level(%d)", int(l))
	}
	return levelNames[l]
}

// Levels names every level, in the order the flag's help lists them.
func Levels() []string {
	names := make([]string, 0, len(levelNames))
	for _, name := range levelNames {
		names = append(names, name)
	}
	return names
}

// ParseLevel reads a level by name, case-insensitively.
func ParseLevel(name string) (Level, error) {
	for level, known := range levelNames {
		if equalFold(name, known) {
			return Level(level), nil
		}
	}
	return DefaultLevel, fmt.Errorf("unknown log level %q (one of: error, warn, info, debug)", name)
}

// equalFold compares two ASCII names without allocating. Log level names
// are ASCII by construction, so the Unicode folding rules do not apply.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// Logger writes levelled lines to one destination. It is safe for
// concurrent use: the daemons log from every request handler and from
// each background runner at once.
type Logger struct {
	inner *log.Logger
	level Level

	// mu guards the destination, which Reopen replaces underneath the
	// goroutines that are writing to it.
	mu   sync.Mutex
	file *os.File // nil unless this Logger opened the file itself
	path string
}

// New writes to an already-open destination — os.Stderr for a daemon
// under an init system that captures it.
func New(out io.Writer, prefix string, level Level) *Logger {
	return &Logger{inner: log.New(out, prefix, log.LstdFlags), level: level}
}

// Discard is a Logger that writes nothing, for tests and for the paths
// that take a logger but have nowhere to put its output.
func Discard() *Logger { return New(io.Discard, "", DefaultLevel) }

// Open writes to a file, creating it and its directory if they are not
// there. The file is opened for appending, so a restart continues the
// same log rather than truncating what the last run wrote.
func Open(path, prefix string, level Level) (*Logger, error) {
	l := &Logger{level: level, path: path}
	file, err := openLogFile(path)
	if err != nil {
		return nil, err
	}
	l.file = file
	l.inner = log.New(file, prefix, log.LstdFlags)
	return l, nil
}

// openLogFile is the part Reopen repeats. The mode keeps the log off a
// world-readable path: job results and agent identities are not secrets,
// but they describe the fleet in enough detail to be worth withholding.
func openLogFile(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("log directory %s: %w", dir, err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("log file %s: %w", path, err)
	}
	return file, nil
}

// Level reports the level this Logger was built with.
func (l *Logger) Level() Level { return l.level }

// Enabled reports whether a message at this level would be written. Use
// it to skip work that only exists to build a log line.
func (l *Logger) Enabled(level Level) bool { return level <= l.level }

// Path is the file this Logger writes to, empty when it writes to a
// destination somebody else opened.
func (l *Logger) Path() string { return l.path }

// Reopen closes the log file and opens the path again, which is what
// newsyslog(8) and logrotate expect after they have renamed it: without
// this the daemon keeps writing to the rotated-away file until it
// restarts. A Logger that does not own its destination — stderr under an
// init system — has nothing to reopen and says so by doing nothing.
func (l *Logger) Reopen() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	file, err := openLogFile(l.path)
	if err != nil {
		// Keep writing to the old file rather than losing the log
		// entirely: a rotated-away destination still reaches the disk.
		return err
	}
	l.inner.SetOutput(file)
	old := l.file
	l.file = file
	return old.Close()
}

// Close releases a log file this Logger opened.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

func (l *Logger) Errorf(format string, args ...any) { l.output(Error, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.output(Warn, format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.output(Info, format, args...) }
func (l *Logger) Debugf(format string, args ...any) { l.output(Debug, format, args...) }

// output pads the level so the messages line up in a terminal, which is
// where these logs are read most often.
func (l *Logger) output(level Level, format string, args ...any) {
	if !l.Enabled(level) {
		return
	}
	l.inner.Printf("%-5s %s", level, fmt.Sprintf(format, args...))
}

// StdLogger adapts this Logger to the *log.Logger that net/http and the
// rest of the standard library expect for their error sinks. Lines
// arrive already formatted, so the adapter only strips the newline the
// standard library adds and files them at the given level.
func (l *Logger) StdLogger(level Level) *log.Logger {
	return log.New(&levelWriter{logger: l, level: level}, "", 0)
}

type levelWriter struct {
	logger *Logger
	level  Level
}

func (w *levelWriter) Write(p []byte) (int, error) {
	message := strings.TrimRight(string(p), "\n")
	if message != "" {
		w.logger.output(w.level, "%s", message)
	}
	return len(p), nil
}
