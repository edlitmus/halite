// Package eventbus is the hub's event bus: SPEC section 17's durable
// append-only log.
//
// Salt's bus is in-memory ZeroMQ IPC and is lossy by construction —
// every mature Salt estate has learned this during an incident, when
// the events that would have explained what happened were the ones that
// were dropped. This one is a file. A reactor that restarts loses
// nothing, and an incident can be reconstructed afterwards.
package eventbus

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// Schema is the version of the event shape.
const Schema = "halite.event/1"

// Event is one record on the bus.
//
// The underscore-prefixed names are Salt's, so that a reactor SLS which
// reads `data['id']` or `_stamp` translates mechanically. SPEC 17.1.
type Event struct {
	Tag   string    `json:"_tag"`
	Stamp time.Time `json:"_stamp"`
	Node  string    `json:"_node,omitempty"`
	// Correlation is propagated through causally related events, so
	// that "what did this job cause" has an answer.
	Correlation string         `json:"_correlation,omitempty"`
	Schema      string         `json:"_schema"`
	Data        map[string]any `json:"data,omitempty"`
	// Offset is where this record sits in the log. It is filled in on
	// read, not stored, so that a subscriber can resume from it.
	Offset string `json:"_offset,omitempty"`
}

// ValidTag reports whether a tag is one the bus will accept.
//
// Tags are `/`-delimited and rooted at `halite`, and a tag that is a
// path traversal or a control character is refused: a tag reaches a
// log, a glob comparison, and an operator's terminal.
func ValidTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("an event needs a tag")
	}
	if len(tag) > 512 {
		return fmt.Errorf("the tag is %d bytes; the maximum is 512", len(tag))
	}
	if strings.Contains(tag, "..") {
		return fmt.Errorf("the tag %q contains %q", tag, "..")
	}
	for _, r := range tag {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("the tag %q contains a control character", tag)
		}
	}
	return nil
}

// MatchTag reports whether a tag matches a glob.
//
// `/` is a separator, as in a path, so `halite/job/*/new` matches one
// segment and `halite/job/**` matches the rest. The `**` form is not
// path.Match's, so it is handled here: without it a subscriber has to
// know how deep a tag goes, and the tags in SPEC 17.1 vary.
func MatchTag(pattern, tag string) bool {
	if pattern == "" || pattern == "*" || pattern == "**" {
		return true
	}
	if i := strings.Index(pattern, "**"); i >= 0 {
		prefix := pattern[:i]
		return strings.HasPrefix(tag, prefix)
	}
	ok, err := path.Match(pattern, tag)
	return err == nil && ok
}

// MatchAny reports whether a tag matches any of the globs. No globs
// means every tag, which is what a subscriber asking for nothing in
// particular means.
func MatchAny(patterns []string, tag string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if MatchTag(p, tag) {
			return true
		}
	}
	return false
}

// encode renders one record as a line.
func (e *Event) encode() ([]byte, error) {
	e.Schema = Schema
	if e.Stamp.IsZero() {
		e.Stamp = time.Now()
	}
	// The offset is where a record is, not part of it.
	offset := e.Offset
	e.Offset = ""
	raw, err := json.Marshal(e)
	e.Offset = offset
	if err != nil {
		return nil, fmt.Errorf("encoding the event %q: %w", e.Tag, err)
	}
	return append(raw, '\n'), nil
}

// MarshalJSON writes the stamp with microseconds, as SPEC 17.1
// requires: two events in the same millisecond are ordinary, and a
// timestamp that cannot tell them apart makes a log unreadable at
// exactly the moment it matters.
func (e Event) MarshalJSON() ([]byte, error) {
	type alias Event
	return json.Marshal(&struct {
		Stamp string `json:"_stamp"`
		*alias
	}{
		Stamp: e.Stamp.UTC().Format("2006-01-02T15:04:05.000000Z07:00"),
		alias: (*alias)(&e),
	})
}

// UnmarshalJSON reads it back.
//
// The payload comes back through the nine-type model. The standard
// decoder turns every number in a `map[string]any` into a float64, so
// an event carrying a 64-bit identifier -- a job's, a serial, a
// timestamp in nanoseconds -- came off the log with the last digits
// changed. SPEC 6.4 requires that it does not.
func (e *Event) UnmarshalJSON(raw []byte) error {
	type alias Event
	aux := &struct {
		Stamp string `json:"_stamp"`
		*alias
	}{alias: (*alias)(e)}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(aux); err != nil {
		return err
	}
	for k, v := range e.Data {
		e.Data[k] = value.FromJSON(v)
	}
	if aux.Stamp != "" {
		stamp, err := time.Parse(time.RFC3339Nano, aux.Stamp)
		if err != nil {
			return fmt.Errorf("the event's _stamp %q is not a timestamp: %w", aux.Stamp, err)
		}
		e.Stamp = stamp
	}
	return nil
}

// SaltTag is the `salt/...` spelling of a halite tag, for
// `event_tag_compat` — a transition period where an existing consumer
// cannot be changed at the same time as the estate.
//
// The mapping is mechanical for everything but the role name, which
// differs between the two vocabularies. Anything with no // lexicon:allow
// Salt equivalent keeps its own shape under the `salt/` root rather
// than being dropped, because a consumer that is watching a glob would
// rather see a tag it does not recognise than miss an event.
func SaltTag(tag string) string {
	const root = "halite/"
	if !strings.HasPrefix(tag, root) {
		return ""
	}
	rest := tag[len(root):]
	switch {
	case strings.HasPrefix(rest, "node/"):
		// The Salt spelling of the same tag. // lexicon:allow
		return "salt/minion/" + rest[len("node/"):] // lexicon:allow
	case strings.HasPrefix(rest, "state/"):
		// Salt fires salt/state_result/<jid>/<result>/<id>; the order
		// of the last two differs, so it is rebuilt rather than
		// prefixed.
		parts := strings.Split(rest[len("state/"):], "/")
		if len(parts) == 3 {
			return "salt/state_result/" + parts[0] + "/" + parts[2] + "/" + parts[1]
		}
	}
	return "salt/" + rest
}
