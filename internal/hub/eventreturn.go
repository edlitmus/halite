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
// A delivery failure does not advance the offset past the event that
// failed. The alternative — logging the failure and moving on — turns a
// returner outage into permanent loss, and this exists to prevent exactly
// that.
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
			// Three different failures, and they were one: any error set
			// the position to the end of the bus and read again at once.
			// For a shipper whose whole purpose is that an outage does not
			// become a hole, that turned a transient read error into every
			// event between the offset and now being skipped without a
			// trace, and a persistent one into a loop with no pause.
			//
			// Lagged: the position was valid and retention has since
			// removed it. The oldest event still held is the nearest
			// surviving point, so that is where to resume, and what was
			// lost is counted and logged because nothing else will say so.
			// Resuming at the end threw away the retained events as well.
			var lag *eventbus.LagError
			if errors.As(err, &lag) {
				e.Server.m().eventsDropped.With("subscriber_lag").Inc()
				e.Server.warn("the event returner fell behind the event bus and events were lost",
					"offset", lag.From, "segments_pruned", lag.Segments,
					"resuming_from", lag.Oldest)
				from = lag.Oldest
				e.writeOffset(from)
				continue
			}
			// Malformed: it says nothing about where the shipper was, so
			// there is nowhere to resume but the end, as before.
			if errors.Is(err, eventbus.ErrBadOffset) {
				e.Server.warn("the event returner's offset is not one this bus issued; starting from the end",
					"offset", from, "error", err.Error())
				from = eventbus.Latest
				continue
			}
			// Anything else is the disk or the bus failing, which says
			// nothing about the position either. Keep it, and try again
			// after a pause rather than guess.
			e.Server.warn("the event returner could not read the event bus; will retry from the same offset",
				"offset", from, "error", err.Error())
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
			continue
		}
		shipped := 0
		for i := range events {
			if err := e.Returner.Event(ctx, &events[i]); err != nil {
				e.Server.warn("an event could not be shipped",
					"tag", events[i].Tag, "returner", e.Returner.Name(), "error", err.Error())
				// Not `events_dropped`: this event is retried, and counting an
				// outage there made `HaliteEventsDropped` say the bus had
				// dropped events when it had dropped none. DIVERGENCE 5.518.
				e.Server.m().eventReturnerFailures.Inc()
				break
			}
			shipped++
		}
		if shipped == len(events) && next != from {
			from = next
			e.writeOffset(from)
		}
		if shipped < len(events) {
			// What did go is recorded, so the retry sends the rest and not
			// the whole batch again: an event's own offset is the position
			// after it. A receiver that is down for an hour and then takes
			// a batch half way used to be sent the first half twice, and
			// the test below had been written to expect that.
			if shipped > 0 {
				from = events[shipped-1].Offset
				e.writeOffset(from)
			}
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
