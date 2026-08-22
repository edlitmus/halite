// Package value is halite's data model: the nine types the YAML parser is
// allowed to construct, an ordered mapping, and the source positions that
// make a diagnostic point at the line an operator wrote.
//
// Ordering is the reason this package exists rather than a plain Go map.
// State ordering follows declaration order in the SLS file (SPEC section
// 11.4), so a mapping that loses its order loses the run order with it. A
// Go map is never used to hold parsed data.
//
// Positions are carried on mapping entries rather than on every scalar.
// That is a deliberate limit: every diagnostic the specification calls for
// anchors on a mapping key — a state ID, a module.function, an argument
// name, a pillar key — or is a parse error, which carries its own
// position. Sequence items in an SLS tree are themselves single-key
// mappings and so carry positions of their own.
package value

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Pos is a source location. A zero Pos means "not from a file".
type Pos struct {
	File string
	Line int
	Col  int
}

func (p Pos) String() string {
	switch {
	case p.File == "" && p.Line == 0:
		return "<unknown>"
	case p.File == "":
		return fmt.Sprintf("line %d, column %d", p.Line, p.Col)
	case p.Line == 0:
		return p.File
	default:
		return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
	}
}

// IsZero reports whether the position is unset.
func (p Pos) IsZero() bool { return p.File == "" && p.Line == 0 && p.Col == 0 }

// Kind names one of the nine types the parser may construct. Nothing else
// is constructible, which is what makes a YAML document unable to become
// code (SPEC section 10.1.2).
type Kind int

const (
	KindNull Kind = iota
	KindBool
	KindInt
	KindFloat
	KindString
	KindBinary
	KindTimestamp
	KindSeq
	KindMap
)

var kindNames = [...]string{"null", "bool", "int", "float", "string", "binary", "timestamp", "sequence", "mapping"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// KindOf classifies a value. Anything outside the nine types reports
// KindString only if it is a Go string; anything else is a programming
// error in a caller and is reported as such by TypeName.
func KindOf(v any) Kind {
	switch v.(type) {
	case nil:
		return KindNull
	case bool:
		return KindBool
	case int64:
		return KindInt
	case float64:
		return KindFloat
	case string:
		return KindString
	case []byte:
		return KindBinary
	case time.Time:
		return KindTimestamp
	case []any:
		return KindSeq
	case *Map:
		return KindMap
	}
	return Kind(-1)
}

// TypeName gives a human name for any value, including ones outside the
// model, for use in error messages.
func TypeName(v any) string {
	if k := KindOf(v); k >= 0 {
		return k.String()
	}
	return fmt.Sprintf("%T", v)
}

// Entry is one key/value pair in a Map, with the positions of both.
type Entry struct {
	Key    any
	Val    any
	KeyPos Pos
	ValPos Pos
}

// Map is an ordered mapping. The zero value is an empty map ready for use.
//
// Keys are scalars: string, int64, float64, bool, or nil. Mappings and
// sequences as keys are rejected by the parser (SPEC section 10.1.2), so
// every key has a stable canonical string form used for lookup.
type Map struct {
	entries []Entry
	index   map[string]int
	Pos     Pos
}

// NewMap returns an empty map with capacity hint n.
func NewMap(n int) *Map {
	return &Map{entries: make([]Entry, 0, n), index: make(map[string]int, n)}
}

// MapOf builds a map from alternating key and value arguments. It panics
// on an odd count, which can only be a bug at a call site.
func MapOf(kv ...any) *Map {
	if len(kv)%2 != 0 {
		panic("value.MapOf: odd number of arguments")
	}
	m := NewMap(len(kv) / 2)
	for i := 0; i < len(kv); i += 2 {
		m.Set(kv[i], kv[i+1])
	}
	return m
}

// CanonKey renders a scalar key in the form used for lookup. Integer and
// float keys are distinguishable from their string spellings by a type
// sigil, so that `1: a` and `"1": b` are two keys rather than a silent
// collision.
func CanonKey(k any) string {
	switch t := k.(type) {
	case string:
		return "s" + t
	case int64:
		return "i" + strconv.FormatInt(t, 10)
	case float64:
		return "f" + strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return "b" + strconv.FormatBool(t)
	case nil:
		return "n"
	default:
		return "?" + fmt.Sprint(k)
	}
}

// Len reports the number of entries.
func (m *Map) Len() int {
	if m == nil {
		return 0
	}
	return len(m.entries)
}

// Entries exposes the entries in declaration order. The slice must not be
// mutated by the caller.
func (m *Map) Entries() []Entry {
	if m == nil {
		return nil
	}
	return m.entries
}

// Keys returns the keys in declaration order.
func (m *Map) Keys() []any {
	if m == nil {
		return nil
	}
	ks := make([]any, len(m.entries))
	for i, e := range m.entries {
		ks[i] = e.Key
	}
	return ks
}

// StringKeys returns the keys in declaration order, rendered as strings.
// Non-string keys are formatted; this is for iteration in templates, where
// Jinja's own model has no notion of a typed key.
func (m *Map) StringKeys() []string {
	if m == nil {
		return nil
	}
	ks := make([]string, len(m.entries))
	for i, e := range m.entries {
		ks[i] = KeyString(e.Key)
	}
	return ks
}

// KeyString renders a key the way a template or a log line should show it.
func KeyString(k any) string {
	switch t := k.(type) {
	case string:
		return t
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return "null"
	default:
		return fmt.Sprint(k)
	}
}

// Get returns the value for a key and whether it was present.
func (m *Map) Get(k any) (any, bool) {
	if m == nil || m.index == nil {
		return nil, false
	}
	i, ok := m.index[CanonKey(k)]
	if !ok {
		return nil, false
	}
	return m.entries[i].Val, true
}

// GetString is Get for the common case of a string key.
func (m *Map) GetString(k string) (any, bool) { return m.Get(k) }

// Has reports whether a key is present.
func (m *Map) Has(k any) bool { _, ok := m.Get(k); return ok }

// Entry returns the full entry for a key, including positions.
func (m *Map) Entry(k any) (Entry, bool) {
	if m == nil || m.index == nil {
		return Entry{}, false
	}
	i, ok := m.index[CanonKey(k)]
	if !ok {
		return Entry{}, false
	}
	return m.entries[i], true
}

// Set inserts or replaces a key, preserving the position of an existing
// key on replacement so that a merge does not move a diagnostic.
func (m *Map) Set(k, v any) { m.SetAt(k, v, Pos{}, Pos{}) }

// SetAt inserts or replaces a key with explicit positions. A zero KeyPos
// on replacement leaves the existing position alone.
func (m *Map) SetAt(k, v any, keyPos, valPos Pos) {
	if m.index == nil {
		m.index = make(map[string]int, 8)
	}
	ck := CanonKey(k)
	if i, ok := m.index[ck]; ok {
		m.entries[i].Val = v
		if !valPos.IsZero() {
			m.entries[i].ValPos = valPos
		}
		if !keyPos.IsZero() {
			m.entries[i].KeyPos = keyPos
		}
		return
	}
	m.index[ck] = len(m.entries)
	m.entries = append(m.entries, Entry{Key: k, Val: v, KeyPos: keyPos, ValPos: valPos})
}

// Delete removes a key, preserving the order of the rest.
func (m *Map) Delete(k any) bool {
	if m == nil || m.index == nil {
		return false
	}
	ck := CanonKey(k)
	i, ok := m.index[ck]
	if !ok {
		return false
	}
	m.entries = append(m.entries[:i], m.entries[i+1:]...)
	delete(m.index, ck)
	for j := i; j < len(m.entries); j++ {
		m.index[CanonKey(m.entries[j].Key)] = j
	}
	return true
}

// Clone returns a shallow copy: the entry list is new, the values are
// shared. Use Deep for a copy that may be mutated at any depth.
func (m *Map) Clone() *Map {
	if m == nil {
		return nil
	}
	c := &Map{entries: make([]Entry, len(m.entries)), index: make(map[string]int, len(m.index)), Pos: m.Pos}
	copy(c.entries, m.entries)
	for k, v := range m.index {
		c.index[k] = v
	}
	return c
}

// Deep returns a deep copy of a value, so that a caller may mutate the
// result without affecting the original. Values outside the model are
// returned as they are, since they are immutable scalars by construction.
func Deep(v any) any {
	switch t := v.(type) {
	case *Map:
		if t == nil {
			return (*Map)(nil)
		}
		c := &Map{entries: make([]Entry, len(t.entries)), index: make(map[string]int, len(t.index)), Pos: t.Pos}
		for i, e := range t.entries {
			c.entries[i] = Entry{Key: e.Key, Val: Deep(e.Val), KeyPos: e.KeyPos, ValPos: e.ValPos}
		}
		for k, i := range t.index {
			c.index[k] = i
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i, e := range t {
			c[i] = Deep(e)
		}
		return c
	case []byte:
		c := make([]byte, len(t))
		copy(c, t)
		return c
	default:
		return v
	}
}

// SortedKeys returns the keys in sorted rather than declaration order, for
// the places that need a stable digest rather than the author's order.
func (m *Map) SortedKeys() []string {
	ks := m.StringKeys()
	sort.Strings(ks)
	return ks
}

// Truthy implements the truthiness rule shared by the template engine and
// the state compiler: Python semantics, so an empty string, an empty
// collection, zero, and null are false.
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case int64:
		return t != 0
	case float64:
		return t != 0 && !math.IsNaN(t)
	case string:
		return t != ""
	case []byte:
		return len(t) != 0
	case time.Time:
		return !t.IsZero()
	case []any:
		return len(t) != 0
	case *Map:
		return t.Len() != 0
	case int:
		return t != 0
	}
	return v != nil
}

// Traverse resolves a delimited path such as "a:b:c" against nested
// mappings and sequences, returning the value and whether the whole path
// resolved. It backs pillar.get, grains.get, and the `traverse` filter,
// and follows Salt's rule that a numeric component indexes a sequence.
func Traverse(root any, path, delim string) (any, bool) {
	if path == "" {
		return root, true
	}
	cur := root
	for _, part := range strings.Split(path, delim) {
		switch t := cur.(type) {
		case *Map:
			v, ok := t.Get(part)
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			if i, err := strconv.Atoi(part); err == nil {
				if i < 0 || i >= len(t) {
					return nil, false
				}
				cur = t[i]
				continue
			}
			// A non-numeric key against a list searches the mappings
			// inside it, taking the first that has the key. Salt does
			// this, and a pillar written as a list of single-key
			// mappings — which is how a tree groups an account's
			// attributes — depends on it. Refusing here returned nothing
			// and a template rendered the empty value into a state.
			found := false
			for _, item := range t {
				m, ok := item.(*Map)
				if !ok {
					continue
				}
				if v, ok := m.Get(part); ok {
					cur, found = v, true
					break
				}
			}
			if !found {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return cur, true
}
