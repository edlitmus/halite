// Package log is the structured logging of SPEC section 26.1.
//
// One object per line to stderr, to a file, or to both, with the fields
// that section names. A console format exists for interactive use, where
// a JSON object per diagnostic is worse than useless.
//
// The value-based redactor of 26.1 is applied here, at the sink, so a
// value cannot escape through a path that forgot about it. What is not
// here is per-component levels and the journal sink.
package log

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/redact"
)

// Level is a severity. Ordered so that a threshold is a comparison.
type Level int

const (
	Error Level = iota
	Warn
	Info
	Debug
	Trace
)

var levelNames = map[Level]string{
	Error: "error", Warn: "warn", Info: "info", Debug: "debug", Trace: "trace",
}

func (l Level) String() string {
	if s, ok := levelNames[l]; ok {
		return s
	}
	return "info"
}

// ParseLevel reads a level name, including the three Salt spells that
// have no halite equivalent. SPEC 26.1 asks that they be mapped on input
// rather than refused, because an operator translating a configuration
// should not have to look them up.
func ParseLevel(s string) (Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "error", "critical", "fatal":
		return Error, true
	case "warn", "warning":
		return Warn, true
	case "info", "":
		return Info, true
	case "debug":
		return Debug, true
	case "trace", "garbage":
		return Trace, true
	case "quiet":
		// Salt's `quiet` silences everything short of a failure.
		return Error, true
	case "profile":
		// Salt's `profile` sits between info and debug; there is no such
		// level here, and info is the honest neighbour.
		return Info, true
	}
	return Info, false
}

// Format selects the rendering.
type Format int

const (
	// JSON is one object per line, which is the default because a node
	// runs unattended far more often than it is watched.
	JSON Format = iota
	// Console is for a person reading it now.
	Console
)

func ParseFormat(s string) (Format, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "json", "":
		return JSON, true
	case "console", "text":
		return Console, true
	}
	return JSON, false
}

// Logger writes records at or below its level.
type Logger struct {
	mu    sync.Mutex
	out   io.Writer
	level Level
	form  Format
	// fields are attached to every record: the node identity and the
	// component, per SPEC 26.1.
	fields map[string]any
	// now is the clock, so a test can pin the timestamp.
	now func() time.Time
	// secrets scrubs known values out of every record. SPEC 26.1 puts
	// the redactor at the sink for the reason that matters: a log line
	// added later cannot forget to call it.
	secrets *redact.Set
	// closers are the sinks this logger opened and must close.
	closers []io.Closer
}

// Options configure a logger.
type Options struct {
	Level  Level
	Format Format
	// File, when set, receives the records as well as stderr.
	File string
	// FileLevel is the level for the file sink, defaulting to Level.
	FileLevel *Level
	// Fields are attached to every record.
	Fields map[string]any
	// Stderr is where the console sink writes. Nil means os.Stderr.
	Stderr io.Writer
	// Secrets are scrubbed from every record. Nil scrubs nothing.
	Secrets *redact.Set
}

// New builds a logger. A file that cannot be opened is an error rather
// than a silent fall back to stderr: an operator who asked for a file is
// relying on it being there.
func New(opts Options) (*Logger, error) {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	l := &Logger{out: stderr, level: opts.Level, form: opts.Format, fields: opts.Fields,
		now: time.Now, secrets: opts.Secrets}
	if l.fields == nil {
		l.fields = map[string]any{}
	}
	if opts.File != "" {
		f, err := os.OpenFile(opts.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("opening the log file: %w", err)
		}
		l.closers = append(l.closers, f)
		l.out = io.MultiWriter(stderr, f)
	}
	return l, nil
}

// Close releases any file the logger opened.
func (l *Logger) Close() error {
	var err error
	for _, c := range l.closers {
		if cerr := c.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}

// Enabled reports whether a level would be written, so a caller can skip
// building a message nobody will see.
func (l *Logger) Enabled(level Level) bool { return level <= l.level }

// With returns a logger that attaches one more field to every record.
func (l *Logger) With(key string, value any) *Logger {
	fields := make(map[string]any, len(l.fields)+1)
	for k, v := range l.fields {
		fields[k] = v
	}
	fields[key] = value
	return &Logger{out: l.out, level: l.level, form: l.form, fields: fields, now: l.now, secrets: l.secrets}
}

// Log writes one record.
func (l *Logger) Log(level Level, msg string, kv ...any) {
	if !l.Enabled(level) {
		return
	}
	rec := make(map[string]any, len(l.fields)+len(kv)/2+3)
	for k, v := range l.fields {
		rec[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		rec[key] = kv[i+1]
	}
	// Every field, not only the message: a secret arrives in whichever
	// one the caller happened to put it in.
	for k, v := range rec {
		rec[k] = l.secrets.ScrubValue(v)
	}
	rec["ts"] = l.now().UTC().Format("2006-01-02T15:04:05.000000Z07:00")
	rec["level"] = level.String()
	rec["msg"] = l.secrets.Scrub(msg)

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.form == Console {
		fmt.Fprintln(l.out, consoleLine(rec, l.fields))
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		// A record that will not marshal still has to reach the
		// operator; losing it silently is the failure this whole
		// package is here to avoid.
		fmt.Fprintln(l.out, consoleLine(rec, l.fields))
		return
	}
	fmt.Fprintln(l.out, string(line))
}

func (l *Logger) Error(msg string, kv ...any) { l.Log(Error, msg, kv...) }
func (l *Logger) Warn(msg string, kv ...any)  { l.Log(Warn, msg, kv...) }
func (l *Logger) Info(msg string, kv ...any)  { l.Log(Info, msg, kv...) }
func (l *Logger) Debug(msg string, kv ...any) { l.Log(Debug, msg, kv...) }

// consoleLine renders a record for a person: the message first, because
// that is what they are reading for, and the rest after it.
//
// A field the logger carries on every line is left out, because it is
// the same on every line: a node's own `node_id` and `component` are
// context, not news. A field of the same name that came from the call
// site is kept -- on the hub, `node_id` is which node, and eliding it
// there made every enrollment line say that something happened to
// somebody.
func consoleLine(rec map[string]any, fixed map[string]any) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %v", rec["level"], rec["msg"])
	keys := make([]string, 0, len(rec))
	for k := range rec {
		switch k {
		case "ts", "level", "msg":
			continue
		}
		if v, ok := fixed[k]; ok && fmt.Sprint(v) == fmt.Sprint(rec[k]) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, " %s=%v", k, rec[k])
	}
	return b.String()
}
