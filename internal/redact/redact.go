// Package redact scrubs known secret values out of text.
//
// SPEC section 26.1: "A value-based redactor is seeded with every
// decrypted pillar value, every token, and every configured secret, and
// scrubs them from every log record, event, and error message.
// Redaction is applied at the sink, so a value cannot escape through a
// path that forgot to redact."
//
// The sink is the point. A redactor that each caller has to remember to
// invoke is a redactor that leaks the first time somebody adds a log
// line, and the whole reason a decrypted pillar value is dangerous is
// that it travels through code that has no idea what it is holding.
//
// The line this draws is between a diagnostic and requested data. A log
// record, a warning, and an error message are scrubbed. `pillar items`
// is not: it was asked for the pillar, and answering with asterisks
// would be a different program.
package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/edlitmus/halite/internal/value"
)

// Placeholder replaces a secret. It is the same string Salt uses, so an
// operator who has seen one recognises the other.
const Placeholder = "**********"

// minLength is the shortest value worth scrubbing.
//
// A one- or two-character secret cannot be removed from text without
// removing everything that happens to look like it: a pillar value of
// "1" would turn every number in every message into asterisks, which
// destroys the diagnostics without protecting anything, because a
// one-character secret was never secret. Salt draws a similar line.
const minLength = 6

// URLPlaceholder replaces the credentials inside a URL. It is the token
// Salt writes, so an operator who has read one comment recognises the
// other.
const URLPlaceholder = "<redacted>"

// urlCredentials matches the userinfo of a URL: a scheme, "://", and
// everything up to the "@" that ends it.
//
// The character class is what keeps this honest. Salt's own regex is
// `(https?)://.*@`, which is greedy and unanchored, so a line holding a
// URL and a later "@" — a second source, an address in the same
// sentence — is eaten from the first "://" to the last "@", taking the
// diagnostic with it. Excluding "/", "?", "#", whitespace and a second
// "@" bounds the match to the authority component, where userinfo is
// the only thing that can live. See DIVERGENCE 5.103.
var urlCredentials = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*)://[^/?#\s@]*@`)

// URLCredentials removes the user and password from every URL in text.
//
// This is the half of redaction that no set of known values can cover:
// a credential embedded in a `source:` URL was never a pillar value, so
// nothing ever handed it to Add, and it travels in the one place a
// failing state is certain to print — its own comment, which goes on
// into the job return, the job cache, and the logs.
//
// Every scheme is covered, not just http and https as Salt does: a
// credential in a `git+ssh://` or an `ftp://` URL is the same
// credential, and the cost of the wider match is a "<redacted>" in
// front of an "@" that was never secret.
func URLCredentials(text string) string {
	// The common case is text with no URL in it at all.
	if !strings.Contains(text, "@") {
		return text
	}
	return urlCredentials.ReplaceAllString(text, "${1}://"+URLPlaceholder+"@")
}

// Set is a collection of secret values, safe for concurrent use: values
// are added while a tree renders and read while it logs.
type Set struct {
	mu     sync.RWMutex
	values []string
}

// New returns an empty set.
func New() *Set { return &Set{} }

// Add records a secret. Values shorter than minLength are ignored, and
// so is one already held.
func (s *Set) Add(v string) {
	v = strings.TrimSpace(v)
	if len(v) < minLength {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.values {
		if existing == v {
			return
		}
	}
	s.values = append(s.values, v)
	// Longest first, so a secret that contains another is replaced
	// whole rather than leaving the tail of it in the text.
	sort.Slice(s.values, func(i, j int) bool { return len(s.values[i]) > len(s.values[j]) })
}

// AddTree walks a parsed value and records every string in it. This is
// what a decrypted pillar file is handed to: which of its values are
// secret is not knowable from here, and everything that arrived
// encrypted was encrypted for a reason.
//
// *value.Map is listed explicitly, and the import it costs is the point.
// A pillar arrives as a *value.Map and nothing else: it is what the hub
// sends and what `DecodeJSON` returns. For as long as this switch knew
// only `map[string]any`, the node handed its whole pillar over and the
// set recorded nothing at all, silently — every decrypted value stayed
// printable, and the redactor reported itself empty rather than wrong.
// Naming the type means a change to it is a compile error here instead
// of a quiet return to that. See DIVERGENCE 5.103.
func (s *Set) AddTree(v any) {
	switch t := v.(type) {
	case string:
		s.Add(t)
	case []any:
		for _, item := range t {
			s.AddTree(item)
		}
	case map[string]any:
		for _, item := range t {
			s.AddTree(item)
		}
	case *value.Map:
		for _, e := range t.Entries() {
			s.AddTree(e.Val)
		}
	}
}

// Len reports how many values are held, for a diagnostic about the
// redactor itself.
func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.values)
}

// Scrub replaces every known secret in a string, and the credentials
// inside any URL it holds.
//
// The URL pass runs whatever the set holds, including on a nil set. A
// hub with no encrypted pillar registers no values at all, and that hub
// is not the one that may print an operator's credentialed source URL —
// it is exactly the one that will.
func (s *Set) Scrub(text string) string {
	if text == "" {
		return text
	}
	text = URLCredentials(text)
	if s == nil {
		return text
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.values {
		if strings.Contains(text, v) {
			text = strings.ReplaceAll(text, v, Placeholder)
		}
	}
	return text
}

// ScrubValue scrubs the strings inside a parsed value, leaving its shape
// alone.
func (s *Set) ScrubValue(v any) any {
	switch t := v.(type) {
	case string:
		return s.Scrub(t)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = s.ScrubValue(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			out[k] = s.ScrubValue(item)
		}
		return out
	}
	return v
}
