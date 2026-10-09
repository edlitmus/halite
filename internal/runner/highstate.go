package runner

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/value"
)

// Highstate renders one host's state return the way Salt's highstate
// outputter does with its defaults and colour off: the host, a block per
// state in run order, and the summary.
//
// It is a port of `_format_host` in Salt 3007.1's
// salt/output/highstate.py, and of salt/output/nested.py for the
// changes, both read from the source. Both `halite-node state ...`
// (as the host `local`, which is what `salt-call --local` prints) and
// `halite-hub run ... state.*` (as each node) go through it, so the two
// cannot drift apart again, and `TestHighstateMatchesSalt` in
// internal/saltdiff renders one of Salt's own returns with Salt's own
// outputter and with this and requires the same text. DIVERGENCE 5.245.
//
// What it does not port, because Salt's defaults leave it off or halite
// never produces it: colour; the terse, mixed, changes and filter
// `state_output` modes; `state_compress_ids`; `state_output_pct`; and
// the recursive rendering of an orchestration's changes as nested
// highstates. Warnings are wrapped by a port of Python's textwrap
// (pyFill), hyphens included.
//
// The identifiers it prints are spared from scrubbing, as everywhere a
// run is rendered: they are the schema, and the hub reads them to find
// the declaration in the tree. DIVERGENCE 5.109.
func Highstate(host string, returns *value.Map, secrets *redact.Set) string {
	type row struct {
		key   string
		ret   *value.Map
		order float64
	}
	var rows []row
	if returns != nil {
		for _, e := range returns.Entries() {
			m, ok := e.Val.(*value.Map)
			if !ok || !m.Has("result") {
				continue
			}
			order := 0.0
			if v, ok := m.Get("__run_num__"); ok {
				order, _ = number(v)
			}
			rows = append(rows, row{key: value.KeyString(e.Key), ret: m, order: order})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].order < rows[j].order })

	var lines []string
	var keep []string
	counts := map[string]int{}
	var order []string // the keys of counts as Salt meets them
	count := func(k string) {
		if _, ok := counts[k]; !ok {
			order = append(order, k)
		}
		counts[k]++
	}
	var durations, parallel []float64
	changes := 0

	for _, r := range rows {
		ret := r.ret
		result, _ := ret.Get("result")
		count(pyStr(result))

		// ret.get("duration", 0): an absent duration is a zero one.
		d, hasDuration := ret.Get("duration")
		f := 0.0
		if hasDuration {
			f, _ = durationMS(d)
		}
		if ret.Has("__parallel__") {
			parallel = append(parallel, f)
		} else {
			durations = append(durations, f)
		}

		ch, _ := ret.Get("changes")
		changed, ctext := formatChanges(ch)
		if changed {
			changes++
		}

		comps := strings.Split(r.key, "_|-")
		for len(comps) < 4 {
			comps = append(comps, "")
		}
		keep = append(keep, comps[1], comps[0]+"."+comps[3])
		if sls, ok := ret.Get("__sls__"); ok {
			keep = append(keep, fmt.Sprint(sls))
		}

		block := []string{
			"----------",
			"          ID: " + comps[1],
			"    Function: " + comps[0] + "." + comps[3],
			"      Result: " + pyStr(result),
			"     Comment: " + formatComment(ret),
		}
		if st, ok := ret.Get("start_time"); ok {
			dur := ""
			if hasDuration && pyStr(d) != "" {
				dur = pyStr(d) + " ms"
			}
			block = append(block, "     Started: "+pyStr(st), "    Duration: "+dur)
		}
		if comps[1] != comps[2] {
			block = append(block[:3], append([]string{"        Name: " + comps[2]}, block[3:]...)...)
		}
		lines = append(lines, block...)
		lines = append(lines, "     Changes:   "+ctext)

		// Salt counts a state as warned when its return has the key.
		// halite's always has it, empty when there were none, so an
		// empty list counts as no key.
		if w, ok := ret.Get("warnings"); ok && !emptyList(w) {
			count("warnings")
			var text []string
			if list, ok := w.([]any); ok {
				for _, item := range list {
					text = append(text, pyStr(item))
				}
			} else {
				text = append(text, pyStr(w))
			}
			pad := strings.Repeat(" ", 14)
			lines = append(lines, "    Warnings: "+
				strings.TrimLeft(pyFill(strings.Join(text, "\n"), 80, pad, pad), " \t\n\r\x0b\x0c"))
		}
	}

	// The summary.
	countLen := 0
	for _, k := range order {
		if n := len(strconv.Itoa(counts[k])); n > countLen {
			countLen = n
		}
	}
	width := len("Succeeded") + countLen + 2
	field := func(label string, n any) string {
		return label + ": " + padLeft(fmt.Sprint(n), width-(len(label)+2))
	}
	lines = append(lines, "\nSummary for "+host+"\n"+strings.Repeat("-", width))

	var stats []string
	if counts["None"] > 0 {
		stats = append(stats, fmt.Sprintf("unchanged=%d", counts["None"]))
	}
	if changes > 0 {
		stats = append(stats, fmt.Sprintf("changed=%d", changes))
	}
	succeeded := field("Succeeded", counts["True"]+counts["None"])
	if len(stats) > 0 {
		succeeded += " (" + strings.Join(stats, ", ") + ")"
	}
	lines = append(lines, succeeded, field("Failed", counts["False"]))
	if counts["warnings"] > 0 {
		lines = append(lines, field("Warnings", counts["warnings"]))
	}
	total := 0
	for _, k := range order {
		total += counts[k]
	}
	total -= counts["warnings"]
	lines = append(lines, strings.Repeat("-", width)+"\nTotal states run: "+padLeft(strconv.Itoa(total), width-7))

	sum := 0.0
	for _, f := range durations {
		sum += f
	}
	if len(parallel) > 0 {
		longest := parallel[0]
		for _, f := range parallel {
			longest = math.Max(longest, f)
		}
		sum += longest
	}
	unit := "ms"
	if sum > 999 {
		sum /= 1000
		unit = "s"
	}
	lines = append(lines, "Total run time: "+padLeft(strconv.FormatFloat(sum, 'f', 3, 64), width-5)+" "+unit)

	out := strings.TrimRight(host+":\n"+strings.Join(lines, "\n"), " \t\r\n") + "\n"
	return secrets.ScrubExcept(out, keep)
}

// formatChanges is Salt's _format_changes: nothing for no changes, and
// otherwise the changes through the nested outputter at indent 14.
func formatChanges(ch any) (bool, string) {
	if !truthy(ch) {
		return false, ""
	}
	if _, ok := ch.(*value.Map); !ok {
		return true, "Invalid Changes data: " + pyStr(ch)
	}
	var out []string
	saltNested(ch, 14, "", &out)
	return true, "\n" + strings.TrimRight(strings.Join(out, "\n"), " \t\r\n")
}

// formatComment is how the outputter prints a comment: stripped, with
// every line after the first indented to sit under the first.
func formatComment(ret *value.Map) string {
	c, _ := ret.Get("comment")
	if s, ok := c.(string); ok {
		return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n"+strings.Repeat(" ", 14))
	}
	// A list has no join() in Python, so Salt's attempt at one fails and
	// it prints str() of the list.
	return pyStr(c)
}

// saltNested is the display method of Salt's nested outputter,
// NestDisplay, with colour off.
func saltNested(v any, indent int, prefix string, out *[]string) {
	pad := strings.Repeat(" ", indent)
	switch t := v.(type) {
	case nil, bool:
		*out = append(*out, pad+prefix+pyStr(t))
	case int, int64, float64, uint64:
		*out = append(*out, pad+prefix+pyStr(t))
	case string:
		for i, line := range splitLines(t) {
			p := prefix
			if i > 0 {
				p = strings.Repeat(" ", len(prefix))
			}
			*out = append(*out, pad+p+line)
		}
	case []any:
		for _, item := range t {
			switch item.(type) {
			case []any:
				*out = append(*out, pad+"|_")
				saltNested(item, indent+2, "- ", out)
			case *value.Map:
				*out = append(*out, pad+"|_")
				saltNested(item, indent+2, "", out)
			default:
				saltNested(item, indent, "- ", out)
			}
		}
	case *value.Map:
		if indent > 0 {
			*out = append(*out, pad+"----------")
		}
		keys := make([]string, 0, t.Len())
		vals := map[string]any{}
		for _, e := range t.Entries() {
			k := value.KeyString(e.Key)
			keys = append(keys, k)
			vals[k] = e.Val
		}
		sort.Strings(keys)
		for _, k := range keys {
			*out = append(*out, pad+prefix+k+":")
			saltNested(vals[k], indent+4, "", out)
		}
	default:
		*out = append(*out, pad+prefix+fmt.Sprint(t))
	}
}

// pyStr is Python's str() of a value as JSON or YAML would give it to
// Salt: True, False, None, an int, a float as Python writes one.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		return pyFloat(t)
	case string:
		return t
	case []any:
		parts := make([]string, len(t))
		for i, item := range t {
			if s, ok := item.(string); ok {
				parts[i] = "'" + s + "'"
			} else {
				parts[i] = pyStr(item)
			}
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return fmt.Sprint(v)
}

// durationMS reads a duration as Salt's summary does: a number, or a
// string with " ms" after it.
func durationMS(v any) (float64, bool) {
	if f, ok := number(v); ok {
		return f, true
	}
	if s, ok := v.(string); ok {
		s, _, _ = strings.Cut(s, " ms")
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func number(v any) (float64, bool) {
	switch t := v.(type) {
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint64:
		return float64(t), true
	case float64:
		return t, true
	}
	return 0, false
}

// truthy is Python's truth value for the shapes a return holds.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case *value.Map:
		return t.Len() > 0
	case []any:
		return len(t) > 0
	}
	if f, ok := number(v); ok {
		return f != 0
	}
	return true
}

func emptyList(v any) bool {
	l, ok := v.([]any)
	return ok && len(l) == 0
}

// splitLines is Python's str.splitlines for the line breaks a return
// carries: no empty last line, and nothing at all for "".
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func padLeft(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(s)) + s
}
