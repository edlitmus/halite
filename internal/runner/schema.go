package runner

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/version"
)

// Returns renders the run in Salt's per-state return shape.
//
// The shape is kept exactly, including the state_|-id_|-name_|-function
// key, because every dashboard, returner, and report in an estate parses
// it. SPEC section 11.8.
func (r *RunResult) Returns() *value.Map {
	out := value.NewMap(len(r.Results))
	for _, res := range r.Results {
		// The key of SPEC 11.8 is `state_|-id_|-name_|-fun`. Only the
		// *name* is data: it is whatever the state was pointed at, and
		// for a `cmd.run` that is the command, secrets and all. The
		// module, the ID and the function are the schema an operator
		// reads the key by, and scrubbing them made a run of
		// diagnostics that could not say which states they were about.
		// DIVERGENCE 5.109.
		ch := res.Chunk
		// The ID keeps its pillar values and loses its credentials, for
		// the reason redact.ScrubExcept gives: this estate declares a
		// state whose ID *is* a credentialed URL.
		//
		// The name is scrubbed only when it is its own value. A state
		// that names nothing takes its ID as its name, and scrubbing
		// the copy while sparing the original hides nothing and leaves
		// a key no dashboard can read. `Nested` draws the same line
		// when it decides whether to print a Name at all.
		name := redact.URLCredentials(ch.Name)
		if ch.Name != ch.ID {
			name = r.Secrets.Scrub(ch.Name)
		}
		key := fmt.Sprintf("%s_|-%s_|-%s_|-%s",
			ch.State, redact.URLCredentials(ch.ID), name, ch.Fun)
		out.Set(key, scrubReturn(r.Secrets, res.Return()))
	}
	return out
}

// scrubReturn removes known secrets from a rendered return.
//
// The inner keys are left alone — `comment`, `changes`, and the rest
// are the schema, not data — and so now are the values of the three
// fields that are *also* schema: `__id__`, `__sls__` and `__run_num__`
// address the declaration in the tree rather than describing the node.
// `name` is not among them and is scrubbed, because for a `cmd.run` it
// is the command. DIVERGENCE 5.109.
//
// The outer key is handled by the caller, because it carries the
// state's name.
func scrubReturn(secrets *redact.Set, m *value.Map) *value.Map {
	// No early return on an empty set. Scrub also strips the credentials
	// out of a URL, which no set ever holds, and a hub with no encrypted
	// pillar holds nothing at all — so the one shortcut that looks free
	// is the one that lets an operator's `source:` URL through.
	id, _ := m.Get("__id__")
	for _, e := range m.Entries() {
		// `name` is data, except when it is the ID wearing another
		// field's name -- see the key above.
		if schemaField(e.Key) || (value.KeyString(e.Key) == "name" && e.Val == id) {
			// Spared the value set, not the credential scanner.
			if str, ok := e.Val.(string); ok {
				m.Set(e.Key, redact.URLCredentials(str))
			}
			continue
		}
		switch e.Val.(type) {
		case string, *value.Map, []any:
			m.Set(e.Key, scrubData(secrets, e.Val))
		}
	}
	return m
}

// scrubData removes known secrets from a value inside a return: the
// strings, and the keys of every mapping.
//
// Keys too, because below the return's own fields a key is data. A state
// that keys its changes by what it changed -- host.present by the
// address, file.managed by a path -- put that value in a key, and a key
// was never scrubbed: a secret there printed in full while the same
// value in the comment was masked. DIVERGENCE 5.252.
//
// A new mapping rather than the one given, so that a key that changes
// can take its place in the same order. Two keys that scrub to the same
// text keep both entries, the later numbered: a mapping that silently
// lost one would report less than happened.
func scrubData(secrets *redact.Set, v any) any {
	switch t := v.(type) {
	case string:
		return secrets.Scrub(t)
	case *value.Map:
		out := value.NewMap(t.Len())
		for _, e := range t.Entries() {
			key := e.Key
			if s, ok := key.(string); ok {
				if scrubbed := secrets.Scrub(s); scrubbed != s {
					key = unusedKey(out, scrubbed)
				}
			}
			out.SetAt(key, scrubData(secrets, e.Val), e.KeyPos, e.ValPos)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = scrubData(secrets, item)
		}
		return out
	}
	return v
}

// unusedKey is key, or key with " (2)", " (3)"... when out already has it.
func unusedKey(out *value.Map, key string) string {
	if _, taken := out.Get(key); !taken {
		return key
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s (%d)", key, n)
		if _, taken := out.Get(candidate); !taken {
			return candidate
		}
	}
}

// schemaField names the return fields that address the declaration
// rather than describing the node, and are therefore not scrubbed.
//
// `name` is deliberately absent: it is the one field whose value is
// whatever the state was pointed at.
func schemaField(key any) bool {
	switch value.KeyString(key) {
	case "__id__", "__sls__", "__run_num__":
		return true
	}
	return false
}

// Return renders one state's result.
func (s *StateResult) Return() *value.Map {
	m := value.NewMap(12)
	m.Set("__id__", s.Chunk.ID)
	m.Set("__sls__", s.Chunk.SLS)
	m.Set("__run_num__", int64(s.RunNum))
	m.Set("name", s.Result.Name)
	if s.Result.Name == "" {
		m.Set("name", s.Chunk.Name)
	}

	// A nil result is test mode's "would change" and must survive as null
	// rather than collapsing to false.
	if s.Result.Result == nil {
		m.Set("result", nil)
	} else {
		m.Set("result", *s.Result.Result)
	}

	changes := s.Result.Changes
	if changes == nil {
		changes = value.NewMap(0)
	}
	m.Set("changes", changes)
	m.Set("comment", s.Result.Comment)
	m.Set("duration", saltDuration(s.Duration))
	m.Set("start_time", saltStartTime(s.StartTime))

	warnings := make([]any, len(s.Result.Warnings))
	for i, w := range s.Result.Warnings {
		warnings[i] = w
	}
	m.Set("warnings", warnings)
	return m
}

// JobReturn wraps the run in the job return envelope of SPEC section 9.4,
// which is what a returner and the job cache store.
type JobReturn struct {
	JID       string
	NodeID    string
	Fun       string
	FunArgs   []string
	StartTime time.Time
	Out       string
}

// Envelope renders the job return.
func (r *RunResult) Envelope(j JobReturn) *value.Map {
	args := make([]any, len(j.FunArgs))
	for i, a := range j.FunArgs {
		args[i] = a
	}
	out := j.Out
	if out == "" {
		out = "highstate"
	}
	return value.MapOf(
		"jid", j.JID,
		"id", j.NodeID,
		"fun", j.Fun,
		"fun_args", args,
		"success", !r.Failed(),
		"retcode", int64(r.RetCode()),
		"return", r.Returns(),
		"out", out,
		"start_time", j.StartTime.UTC().Format("2006-01-02T15:04:05.000000Z"),
		"duration_ms", int64(r.Duration/time.Millisecond),
		"node_version", version.Version,
		"schema", "halite.ret/1",
	)
}

// Summary counts the run for the line an operator reads at the end.
//
// Succeeded, WouldHave, and Failed partition the run and sum to Total.
// Changed and Skipped are subsets that cut across them: a state may
// succeed having changed something, and a state held back by a requisite
// or a gate reports whichever result the requisite or gate produced.
// Printing them as though all five were buckets makes the line add up to
// more than the run.
type Summary struct {
	Succeeded int
	Failed    int
	Changed   int
	WouldHave int
	Skipped   int
	Total     int
	Duration  time.Duration
}

// Summarise counts the results.
func (r *RunResult) Summarise() Summary {
	s := Summary{Total: len(r.Results), Duration: r.Duration}
	for _, res := range r.Results {
		switch {
		case res.Result.Failed():
			s.Failed++
		case res.Result.Result == nil:
			s.WouldHave++
		default:
			s.Succeeded++
		}
		if res.Result.HasChanges() && res.Result.Result != nil {
			s.Changed++
		}
		if res.Skipped {
			s.Skipped++
		}
	}
	return s
}

// String renders the summary line.
func (s Summary) String() string {
	parts := []string{
		fmt.Sprintf("Succeeded: %d", s.Succeeded),
	}
	if s.Changed > 0 {
		parts[0] += fmt.Sprintf(" (changed=%d)", s.Changed)
	}
	if s.WouldHave > 0 {
		parts = append(parts, fmt.Sprintf("Would change: %d", s.WouldHave))
	}
	parts = append(parts, fmt.Sprintf("Failed: %d", s.Failed))
	total := fmt.Sprintf("Total: %d", s.Total)
	if s.Skipped > 0 {
		// Written against the total rather than beside the other counts,
		// because a skipped state is already inside one of them and a
		// reader who adds the line up should get the run.
		total += fmt.Sprintf(" (%d held back by a requisite or a gate)", s.Skipped)
	}
	parts = append(parts, total)
	parts = append(parts, fmt.Sprintf("Duration: %s", s.Duration.Round(time.Millisecond)))
	return strings.Join(parts, "  ")
}

// Nested renders the run as `salt-call --local` prints it: Salt's
// highstate output, under the host `local`. See Highstate.
func (r *RunResult) Nested(colour bool) string {
	return Highstate("local", r.Returns(), r.Secrets)
}

// saltDuration is a state's duration as Salt's state.py computes it:
// whole microseconds, divided by 1000.0. So 293.725µs is 0.293 and not
// 0.293725, and a return from halite reads like one from Salt.
func saltDuration(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// saltStartTime is a state's start as Salt's `time().isoformat()`
// writes it, which leaves the fraction off when it is zero.
func saltStartTime(t time.Time) string {
	if t.Nanosecond()/1000 == 0 {
		return t.Format("15:04:05")
	}
	return t.Format("15:04:05.000000")
}

// pyFloat writes a duration as Salt's outputter does, through Python's
// str(): the shortest form that reads back as the same number, with a
// ".0" on a whole one -- 0.293, 12.0. A value that is not a float, from
// a return some other writer made, is printed as it came.
func pyFloat(v any) string {
	f, ok := v.(float64)
	if !ok {
		return fmt.Sprint(v)
	}
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	// repr() switches to an exponent below 1e-4 and from 1e16 up, where
	// Go's shortest form switches at 1e21 and from six digits.
	if f != 0 {
		if exp := math.Floor(math.Log10(math.Abs(f))); exp < -4 || exp >= 16 {
			return strconv.FormatFloat(f, 'e', -1, 64)
		}
	}
	out := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(out, ".") {
		out += ".0"
	}
	return out
}
