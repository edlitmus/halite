package runner

import (
	"fmt"
	"sort"
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
		// The key of SPEC 11.8 is `state_|-id_|-name_|-fun`, and the
		// name is whatever the state was pointed at — which for a
		// `cmd.run` is the command, secrets and all. It is scrubbed like
		// any other text; every occurrence of one value becomes the same
		// placeholder, so a dashboard parsing the key still parses it.
		out.Set(r.Secrets.Scrub(res.Chunk.Key()), scrubReturn(r.Secrets, res.Return()))
	}
	return out
}

// scrubReturn removes known secrets from a rendered return. The inner
// keys are left alone — `comment`, `changes`, and the rest are the
// schema, not data — while every value is scrubbed. The outer key is
// handled by the caller, because it carries the state's name.
func scrubReturn(secrets *redact.Set, m *value.Map) *value.Map {
	if secrets == nil || secrets.Len() == 0 {
		return m
	}
	for _, e := range m.Entries() {
		switch t := e.Val.(type) {
		case string:
			m.Set(e.Key, secrets.Scrub(t))
		case *value.Map:
			m.Set(e.Key, scrubReturn(secrets, t))
		case []any:
			out := make([]any, len(t))
			for i, item := range t {
				if sub, ok := item.(*value.Map); ok {
					out[i] = scrubReturn(secrets, sub)
					continue
				}
				if str, ok := item.(string); ok {
					out[i] = secrets.Scrub(str)
					continue
				}
				out[i] = item
			}
			m.Set(e.Key, out)
		}
	}
	return m
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
	// Salt reports duration in milliseconds as a float.
	m.Set("duration", float64(s.Duration)/float64(time.Millisecond))
	m.Set("start_time", s.StartTime.Format("15:04:05.000000"))

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

// Nested renders the run the way `--out=nested` does: one block per state,
// in run order, with changes indented beneath.
func (r *RunResult) Nested(colour bool) string {
	var b strings.Builder
	for _, res := range r.Results {
		ch := res.Chunk
		fmt.Fprintf(&b, "----------\n")
		fmt.Fprintf(&b, "          ID: %s\n", ch.ID)
		fmt.Fprintf(&b, "    Function: %s\n", ch.Func())
		if ch.Name != ch.ID {
			fmt.Fprintf(&b, "        Name: %s\n", ch.Name)
		}
		fmt.Fprintf(&b, "      Result: %s\n", res.Result.ResultString())
		fmt.Fprintf(&b, "     Comment: %s\n", res.Result.Comment)
		fmt.Fprintf(&b, "     Started: %s\n", res.StartTime.Format("15:04:05.000000"))
		fmt.Fprintf(&b, "    Duration: %.3f ms\n", float64(res.Duration)/float64(time.Millisecond))
		if res.Result.HasChanges() {
			b.WriteString("     Changes:\n")
			writeChanges(&b, res.Result.Changes, "              ")
		}
		for _, w := range res.Result.Warnings {
			fmt.Fprintf(&b, "     Warning: %s\n", w)
		}
	}
	fmt.Fprintf(&b, "\nSummary\n----------\n%s\n", r.Summarise())
	// The whole rendering rather than each field: a comment, a change, a
	// warning, and whatever line is added to this function next all go
	// through one call that nobody has to remember.
	return r.Secrets.Scrub(b.String())
}

func writeChanges(b *strings.Builder, m *value.Map, indent string) {
	for _, e := range m.Entries() {
		key := value.KeyString(e.Key)
		switch t := e.Val.(type) {
		case *value.Map:
			old, hasOld := t.Get("old")
			nw, hasNew := t.Get("new")
			if hasOld || hasNew {
				fmt.Fprintf(b, "%s%s:\n", indent, key)
				fmt.Fprintf(b, "%s    from: %s\n", indent, renderScalar(old))
				fmt.Fprintf(b, "%s      to: %s\n", indent, renderScalar(nw))
				continue
			}
			fmt.Fprintf(b, "%s%s:\n", indent, key)
			writeChanges(b, t, indent+"    ")
		default:
			fmt.Fprintf(b, "%s%s: %s\n", indent, key, renderScalar(e.Val))
		}
	}
}

func renderScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "(absent)"
	case string:
		if t == "" {
			return `""`
		}
		if strings.Contains(t, "\n") {
			lines := strings.Split(strings.TrimRight(t, "\n"), "\n")
			if len(lines) > 6 {
				lines = append(lines[:6], fmt.Sprintf("... %d more lines", len(lines)-6))
			}
			return "|\n        " + strings.Join(lines, "\n        ")
		}
		return t
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			parts[i] = value.KeyString(item)
		}
		sort.Strings(parts)
		return strings.Join(parts, ", ")
	}
	return value.KeyString(v)
}
