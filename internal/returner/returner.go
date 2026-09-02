// Package returner sends a job return, or an event, somewhere durable.
//
// SPEC 20.3 names six returners this build implements in-process and
// sixteen more that belong out of process behind the bridge of SPEC
// section 24 — a database driver is a dependency, and a control plane
// does not link one.
//
// The shape is deliberately narrow. A returner takes a finished return
// and puts it somewhere; it does not decide whether the job succeeded,
// retry the job, or change what is stored. Anything it cannot deliver
// it must either say so about or spool, because a returner that drops
// silently is worse than one that is not configured — an operator who
// configured a returner believes the returns are somewhere.
package returner

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
)

// Returner is one destination.
type Returner interface {
	// Name is the name the configuration uses.
	Name() string
	// Return delivers one job return.
	Return(ctx context.Context, ret *job.Return) error
	// Event delivers one bus event, for `event_return`. A returner that
	// cannot carry events answers with ErrNoEvents rather than
	// pretending, so an estate is told at startup instead of losing an
	// audit trail quietly.
	Event(ctx context.Context, e *eventbus.Event) error
	// Close releases whatever it holds. Delivering after Close is an
	// error rather than a panic.
	Close() error
}

// ErrNoEvents is what a returner answers when it carries returns but
// not the event stream.
var ErrNoEvents = fmt.Errorf("this returner does not carry the event stream")

// ErrNotBuiltIn marks a returner name this build does not implement,
// which may be an extension that has not arrived yet.
//
// The caller decides what that means. On a node it is not fatal —
// an extension arrives through `saltutil.sync_returners`, which needs a
// running node, so refusing to start would deadlock.
var ErrNotBuiltIn = fmt.Errorf("no returner by that name is built in or loaded")

// Options is everything a returner might be configured with.
//
// One struct rather than a map: a misspelt key in a map is a setting
// that silently does nothing, which is the failure mode this whole
// project's configuration handling exists to prevent.
type Options struct {
	// StateDir is where a returner that writes locally puts things.
	StateDir string
	// NodeID names the sender, for a destination that receives from
	// more than one.
	NodeID string

	// File and Local.
	Path      string
	MaxBytes  int64
	KeepFiles int
	Mode      uint32

	// Syslog.
	SyslogAddress  string
	SyslogNetwork  string
	SyslogTag      string
	SyslogFacility string
	SyslogTLS      bool
	SyslogCAFile   string

	// Webhook.
	URL         string
	CAFile      string
	Secret      string
	Timeout     time.Duration
	MaxAttempts int
	SpoolDir    string
	SpoolMax    int64

	// SMTP.
	SMTPAddress  string
	SMTPFrom     string
	SMTPTo       []string
	SMTPSubject  string
	SMTPUsername string
	SMTPPassword string
	SMTPTLS      bool

	// Bridged looks up a returner that runs as an extension, for the
	// sixteen destinations SPEC 20.3 marks Bridged. Nil on a node
	// with no extensions, where those names are reported as needing
	// one rather than as typos.
	Bridged func(name string) (Returner, error)

	// Post files a return with the hub, for `local_cache`. Nil on a
	// node with no hub, which is what makes that returner refuse at
	// startup rather than write nowhere.
	Post func(ctx context.Context, ret *job.Return) error

	// Now is the clock, for the tests.
	Now func() time.Time
	// Log receives what a returner wants an operator to know. Nil is
	// silence.
	Log func(level, msg string, kv ...any)
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o Options) logf(level, msg string, kv ...any) {
	if o.Log != nil {
		o.Log(level, msg, kv...)
	}
}

// Constructor builds one returner from its options.
type Constructor func(Options) (Returner, error)

type entry struct {
	build Constructor
	// events records whether this destination can carry the event
	// stream. Declared rather than discovered: finding out by sending
	// one and seeing what happens means a returner that cannot carry
	// events has already been sent one.
	events bool
}

var registry = map[string]entry{}

// register adds a returner. Called from each returner's own file, so
// that adding one is one file rather than one file and an edit here.
func register(name string, events bool, build Constructor) {
	if _, seen := registry[name]; seen {
		panic("returner: " + name + " is registered twice")
	}
	registry[name] = entry{build: build, events: events}
}

// CarriesEvents reports whether `event_return` may name this returner.
func CarriesEvents(name string) bool {
	e, ok := registry[name]
	return ok && e.events
}

// CheckEventReturn reports why `event_return: <name>` will not work.
//
// Three different problems, and they need three different answers: a
// typo, a destination that runs behind the bridge, and a returner that
// carries returns but not the event stream. Telling an operator who
// wrote `postgres` that it "does not carry the event stream" sends them
// looking for a setting when what they need is section 24.
func CheckEventReturn(name string) error {
	if e, ok := registry[name]; ok {
		if e.events {
			return nil
		}
		return fmt.Errorf("%s carries returns but not the event stream; SPEC 20.3 supports it for %s",
			name, strings.Join(EventReturners(), ", "))
	}
	if _, bridged := BridgedNames[name]; bridged {
		// A bridged returner may carry events; whether it does is
		// something only the extension knows, so this cannot refuse it
		// here. The check happens when the extension is asked.
		return nil
	}
	return fmt.Errorf("%q is not a returner; this build has %s", name, strings.Join(Names(), ", "))
}

// New builds the named returner.
//
// An unknown name is an error naming what exists, and never a returner
// that accepts everything and writes nowhere. SPEC 20.3's bridged
// destinations are named separately, because "postgres is not built"
// and "pstgres is a typo" are different problems.
func New(name string, opts Options) (Returner, error) {
	e, ok := registry[name]
	if ok {
		return e.build(opts)
	}
	// A bridged destination runs as an extension. Asked for by name, so
	// that `returner: postgres` finds the `postgres` extension without
	// the operator having to know it is one.
	if opts.Bridged != nil {
		built, err := opts.Bridged(name)
		if err != nil {
			return nil, err
		}
		if built != nil {
			return built, nil
		}
	}
	// Anything else is a name this build does not have. It may be an
	// extension that has not been synchronized yet, and only the
	// caller knows whether to treat that as fatal — so the error says
	// which case it is and `ErrNotBuiltIn` lets it be told apart.
	if _, bridged := BridgedNames[name]; bridged {
		return nil, fmt.Errorf("the %s returner runs as an extension of kind `returner` "+
			"(SPEC section 20.3), and no extension named %q is loaded: %w", name, name, ErrNotBuiltIn)
	}
	return nil, fmt.Errorf("%q is not a returner this build has (%s) "+
		"and no extension of kind `returner` by that name is loaded: %w",
		name, strings.Join(Names(), ", "), ErrNotBuiltIn)
}

// Names is every returner this build implements, in order.
func Names() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// BridgedNames are the destinations SPEC 20.3 marks Bridged: each needs
// a database or a vendor client, and a control plane does not link one.
//
// Named rather than omitted. An operator who writes `returner: postgres`
// has made a reasonable request, and "postgres is not a returner" would
// be a lie — the answer is that it is an extension.
var BridgedNames = map[string]bool{
	"mysql": true, "postgres": true, "sqlite": true, "redis": true,
	"elasticsearch": true, "influxdb": true, "mongo": true, "carbon": true,
	"splunk": true, "sentry": true, "slack": true, "pagerduty": true,
	"kafka": true, "sqs": true, "sns": true, "cloudwatch": true,
}

// EventReturners is every returner `event_return` may name, in order.
func EventReturners() []string {
	out := make([]string, 0, len(registry))
	for name, e := range registry {
		if e.events {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
