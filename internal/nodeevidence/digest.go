package nodeevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"

	"github.com/edlitmus/halite/internal/value"
)

// Digest is a stable fingerprint of a configuration mapping.
//
// # Why a digest and not the values
//
// SPEC 25.7 asks for a record of every configuration change. A node's
// configuration is a file the node does not own -- an operator edits it,
// or a state applies it -- and it takes effect only when the agent
// starts, so what the node can honestly record is the configuration it is
// running under, once per run. A digest that changes tells an
// investigator that the file changed between two runs and when.
//
// It is a digest rather than the settings because the settings include
// secrets. A record meant to be read by somebody investigating a breach
// is the last file that should hold a bootstrap token, and a redacted
// copy would have the opposite fault: a change confined to a
// secret-bearing key would not show at all. Hashing the whole thing
// catches every change and writes none of it down.
//
// What it does not give is *what* changed. That is deliberate, and the
// pair to it is the file list on the same record: an investigator has the
// files to go and read and the times the digest moved.
//
// # Stability
//
// Keys are walked in sorted order, not the author's order, so that
// reordering a file is not a change and merging two drop-ins in either
// order is not either. Types are tagged, so the string "1" and the
// number 1 do not collide; lengths are written before contents, so that
// two adjacent keys cannot be re-cut into one.
func Digest(m *value.Map) string {
	h := sha256.New()
	writeValue(h, m)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

type sink interface {
	Write([]byte) (int, error)
}

// writeValue appends a type-tagged, length-prefixed encoding of v.
func writeValue(h sink, v any) {
	write := func(tag string, s string) {
		fmt.Fprintf(h, "%s:%d:%s;", tag, len(s), s)
	}
	switch t := v.(type) {
	case nil:
		write("nil", "")
	case *value.Map:
		fmt.Fprintf(h, "map:%d:", t.Len())
		for _, k := range t.SortedKeys() {
			write("key", k)
			val, _ := t.GetString(k)
			writeValue(h, val)
		}
		fmt.Fprint(h, ";")
	case []any:
		// A sequence keeps its order: `file_roots` in a different order
		// is a different search path, so this is a change and must read
		// as one.
		fmt.Fprintf(h, "seq:%d:", len(t))
		for _, item := range t {
			writeValue(h, item)
		}
		fmt.Fprint(h, ";")
	case string:
		write("str", t)
	case bool:
		write("bool", strconv.FormatBool(t))
	case int:
		write("int", strconv.FormatInt(int64(t), 10))
	case int64:
		write("int", strconv.FormatInt(t, 10))
	case uint64:
		write("int", strconv.FormatUint(t, 10))
	case float64:
		write("float", strconv.FormatFloat(t, 'g', -1, 64))
	case map[string]any:
		// A plain Go map reaches configuration only through a decoder
		// that lost the ordering, and it is handled so that such a value
		// digests the same as the ordered map it should have been.
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(h, "map:%d:", len(keys))
		for _, k := range keys {
			write("key", k)
			writeValue(h, t[k])
		}
		fmt.Fprint(h, ";")
	default:
		// A type this function does not know digests by its Go
		// rendering rather than being skipped. Skipping would make two
		// different configurations digest identically, which is the one
		// failure this must not have.
		write(fmt.Sprintf("%T", v), fmt.Sprint(v))
	}
}

// DigestString fingerprints one string, for a field a record holds in
// truncated form.
//
// The truncated text is what a person reads and the digest is what a
// comparison uses: a job's arguments can be larger than any sane line in
// a file, and a record that silently dropped the rest could not be
// checked against the hub's copy at all.
func DigestString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}
