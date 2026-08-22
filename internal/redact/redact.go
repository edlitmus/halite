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
	"sort"
	"strings"
	"sync"
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

// Scrub replaces every known secret in a string.
func (s *Set) Scrub(text string) string {
	if s == nil || text == "" {
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
	if s == nil {
		return v
	}
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
