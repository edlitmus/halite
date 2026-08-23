// Package job is the job identity, the replay defences, and the return
// schema of SPEC sections 6.3 and 9.4.
//
// The hub and the node both depend on this, and they have to agree
// exactly: a jid the hub assigns is the key the node records to refuse
// a replay, the key the job cache files a return under, and the string
// an operator types into `jobs show`.
package job

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// IDLayout is SPEC 6.3's YYYYMMDDThhmmssffffff: sortable, readable, and
// the shape Salt's jid has, so operator muscle memory survives.
const IDLayout = "20060102T150405.000000"

// DefaultTTL is SPEC 6.3's default job lifetime. A node refuses a job
// past its expiry rather than running a stale instruction.
const DefaultTTL = 15 * time.Minute

// ID is a job identifier.
type ID string

// Time reads the moment a jid was assigned.
func (id ID) Time() (time.Time, error) {
	t, err := time.Parse(IDLayout, dotted(string(id)))
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a job identifier: %w", id, err)
	}
	return t, nil
}

// Day is the segment a job's records live in.
func (id ID) Day() string {
	if len(id) < 8 {
		return "unknown"
	}
	return string(id[:8])
}

// Valid reports whether a string is a well-formed jid. It is checked on
// the way in from the network, because the jid becomes a path.
func (id ID) Valid() bool {
	// Eight for the date, one for the T, six for the time, six for the
	// microseconds.
	if len(id) != 21 {
		return false
	}
	_, err := id.Time()
	return err == nil
}

// dotted puts back the separator the wire form leaves out. The layout
// needs one to parse fractional seconds; the jid does not have one,
// because Salt's does not and an operator pastes Salt's.
func dotted(s string) string {
	if len(s) != 21 {
		return s
	}
	return s[:15] + "." + s[15:]
}

// Clock assigns identifiers that do not collide.
//
// Two jobs submitted in the same microsecond would otherwise share a
// jid, and a jid is the key everything else is filed under: the job
// cache, the replay cache, and the return. So the clock never repeats
// and never goes backwards, even when the system clock does.
type Clock struct {
	mu   sync.Mutex
	last time.Time
	Now  func() time.Time
}

func (c *Clock) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Next assigns the next identifier.
func (c *Clock) Next() ID {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC().Truncate(time.Microsecond)
	if !now.After(c.last) {
		now = c.last.Add(time.Microsecond)
	}
	c.last = now
	return ID(format(now))
}

// NewID is the identifier for one moment, for a caller with no hub
// behind it: a local run, or a test.
func NewID(now time.Time) ID { return ID(format(now.UTC())) }

func format(t time.Time) string {
	s := t.Format(IDLayout)
	// The layout carries a dot so that fractional seconds parse; the
	// jid does not carry one.
	return s[:15] + s[16:]
}

// Nonce is SPEC 6.3's 128 bits of randomness, recorded by the node in a
// bounded replay cache.
func Nonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating a job nonce: %w", err)
	}
	return hex.EncodeToString(raw), nil
}
