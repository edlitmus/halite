package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/returner"
)

// EventReturn ships the whole event stream to a returner.
//
// SPEC 20.3 calls this the recommended path to a SIEM, and the useful
// property is the one the bus already has: it resumes from an offset.
// A returner that was unreachable for an hour catches up rather than
// leaving an hour-shaped hole in the audit trail, which is the hole
// that matters.
type EventReturn struct {
	Server   *Server
	Returner returner.Returner
	// Tags narrows what is shipped. Empty ships everything, which is
	// what a SIEM wants.
	Tags []string
	// OffsetFile is where it remembers what it had shipped. Without one
	// a restart starts at the end, which is the same hole.
	OffsetFile string
	// From is where to begin when the offset file says nothing —
	// `latest` by default, because shipping a month of history into a
	// SIEM on first boot is a bill and an alert storm. An estate that
	// wants the backlog sets `earliest` once.
	From string
	// Batch is how many events one read takes.
	Batch int
}

// Run ships events until the context ends.
//
// A delivery failure does not advance the offset. The alternative —
// logging the failure and moving on — turns a returner outage into
// permanent loss, and this exists to prevent exactly that.
func (e *EventReturn) Run(ctx context.Context) error {
	if e.Server == nil || e.Server.Events == nil {
		return errors.New("the event returner needs a hub with an event bus")
	}
	batch := e.Batch
	if batch <= 0 {
		batch = 200
	}
	bus := e.Server.Events
	from := e.readOffset()

	for {
		wake := bus.Wait()
		events, next, err := bus.Read(from, e.Tags, batch)
		if err != nil {
			e.Server.warn("the event returner could not read from its offset; starting from the end",
				"offset", from, "error", err.Error())
			from = eventbus.Latest
			continue
		}
		shipped := 0
		for i := range events {
			if err := e.Returner.Event(ctx, &events[i]); err != nil {
				e.Server.warn("an event could not be shipped",
					"tag", events[i].Tag, "returner", e.Returner.Name(), "error", err.Error())
				e.Server.m().eventsDropped.With("returner_failed").Inc()
				break
			}
			shipped++
		}
		if shipped == len(events) && next != from {
			from = next
			e.writeOffset(from)
		}
		if shipped < len(events) {
			// Wait before trying the ones that did not go, rather than
			// spinning on a receiver that is down.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if len(events) == batch {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-time.After(30 * time.Second):
		}
	}
}

// readOffset is where the shipper had got to.
func (e *EventReturn) readOffset() string {
	start := e.From
	if start == "" {
		start = eventbus.Latest
	}
	if e.OffsetFile == "" {
		return start
	}
	raw, err := os.ReadFile(filepath.Clean(e.OffsetFile))
	if err != nil {
		return start
	}
	offset := strings.TrimSpace(string(raw))
	if offset == "" {
		return start
	}
	return offset
}

func (e *EventReturn) writeOffset(offset string) {
	if e.OffsetFile == "" {
		return
	}
	if err := writeAtomic(e.OffsetFile, []byte(offset+"\n"), 0o600); err != nil {
		e.Server.warn("could not record the event returner's position",
			"file", e.OffsetFile, "error", err.Error())
	}
}
