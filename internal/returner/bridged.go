package returner

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
)

// Extension is a returner that runs out of process.
//
// SPEC 20.3 marks seventeen destinations Bridged — `postgres`, `redis`,
// `elasticsearch`, `kafka`, and the rest — because each needs a
// database driver, and a control plane does not link one. They are
// extensions of kind `returner`, and this is the adapter that makes one
// look like any other returner to the code that calls it.
type Extension interface {
	// Name identifies it, for a message.
	Name() string
	// Call invokes one of its functions.
	Call(ctx context.Context, function string, args, kwargs any) (json.RawMessage, error)
}

// The two functions a returner extension provides. Named here rather
// than discovered, so that an extension missing one is a failure at
// configuration rather than at the first return nobody sees.
const (
	FunctionReturn = "returner.return"
	FunctionEvent  = "returner.event"
)

// bridgedReturner sends a return to an extension.
type bridgedReturner struct {
	name string
	ext  Extension
	// events records whether this extension provides `returner.event`,
	// so `event_return` naming one that does not is refused at
	// configuration rather than losing the audit trail quietly.
	events bool
}

// FromExtension wraps a loaded returner extension.
//
// `provides` is the function list the extension declared at handshake,
// which is how this knows whether it can carry the event stream.
func FromExtension(name string, ext Extension, provides []string) Returner {
	r := &bridgedReturner{name: name, ext: ext}
	for _, function := range provides {
		if function == FunctionEvent {
			r.events = true
		}
	}
	return r
}

func (r *bridgedReturner) Name() string { return r.name }

func (r *bridgedReturner) Return(ctx context.Context, ret *job.Return) error {
	// The return goes over as the schema of SPEC 11.8, which is what a
	// bridged returner writes into a database column and what every
	// dashboard already parses. An extension gets the same shape the
	// job cache holds.
	raw, err := json.Marshal(ret)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	_, err = r.ext.Call(ctx, FunctionReturn, nil, map[string]any{"return": payload})
	if err != nil {
		return fmt.Errorf("the %s returner: %w", r.name, err)
	}
	return nil
}

func (r *bridgedReturner) Event(ctx context.Context, e *eventbus.Event) error {
	if !r.events {
		return ErrNoEvents
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return err
	}
	if _, err := r.ext.Call(ctx, FunctionEvent, nil, map[string]any{"event": payload}); err != nil {
		return fmt.Errorf("the %s returner: %w", r.name, err)
	}
	return nil
}

// Close does nothing: the extension's process belongs to the extension
// runtime, which starts and stops it. A returner that killed the pool
// would take out every other user of the same extension.
func (r *bridgedReturner) Close() error { return nil }

// Unavailable is a returner that is configured and not present.
//
// It exists so a node whose returner is an extension that has not been
// synchronized yet can still start. The alternative was fatal at
// startup, and it deadlocks: the extension arrives through
// `saltutil.sync_returners`, which needs a running node.
//
// Every return fails, with the reason, rather than going somewhere
// else. Falling back to `local` would put the estate's returns in a
// file nobody is watching while the configuration says they are in a
// database.
func Unavailable(name string, reason error) Returner {
	return &unavailableReturner{name: name, reason: reason}
}

type unavailableReturner struct {
	name   string
	reason error
}

func (r *unavailableReturner) Name() string { return r.name }

func (r *unavailableReturner) Return(ctx context.Context, ret *job.Return) error {
	return fmt.Errorf("the %s returner is not available: %w", r.name, r.reason)
}

func (r *unavailableReturner) Event(ctx context.Context, e *eventbus.Event) error {
	return fmt.Errorf("the %s returner is not available: %w", r.name, r.reason)
}

func (r *unavailableReturner) Close() error { return nil }

// IsUnavailable reports whether a returner is a placeholder for one
// that has not arrived, so a caller can say so once at startup rather
// than on every return.
func IsUnavailable(r Returner) bool {
	_, ok := r.(*unavailableReturner)
	return ok
}
