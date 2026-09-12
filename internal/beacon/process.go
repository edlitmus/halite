package beacon

import (
	"fmt"
	"sort"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The two process beacons of SPEC 16.2, `proc` and `ps`.
//
// Both were registered as pending "a later phase, with a portable
// reader for it". The reader is the `ps` execution module (DIVERGENCE
// 5.61), and these are functions over it: a beacon in this build calls
// the node's own execution modules, which is what makes one portable
// for free and what keeps the process table read in exactly one place.
//
// # Why two beacons and not one
//
// SPEC gives them different jobs and the difference is the one an
// operator cares about. `proc` answers "is it there", which is a
// question about a thing that should be running and a page when it is
// not. `ps` answers "is it behaving", which is a question about a thing
// that is running and a page when it eats the machine. Salt has both
// names and uses them for nearly the same thing; here the names mean
// what SPEC's inventory says they mean.
//
// # Matching is by pattern, and that is a decision
//
// Every key is a regular expression matched against the process name,
// or against the whole command line when `full` is set. A daemon is
// often several processes -- a supervisor and its workers -- and a
// beacon that could only name one of them would be answering a
// different question from the one asked.

// procPresence fires on a process being there, or not being there.
//
// The configured form mirrors Salt's, which an existing tree already
// has:
//
//	beacons:
//	  proc:
//	    - processes:
//	        sshd: running
//	        oldthing: stopped
//
// A value of `running` emits only when at least one process matches,
// `stopped` only when none does, and anything else -- including an
// empty value -- emits the reading every poll and leaves the deciding
// to `onchangeonly`, which is how the `service` beacon beside this one
// behaves.
func procPresence(c *exec.Context, in *Instance) ([]Event, error) {
	patterns, err := processPatterns(in, "proc")
	if err != nil {
		return nil, err
	}
	full := truthyArg(in, "full")

	var out []Event
	for _, p := range patterns {
		pids, err := pgrep(c, p.pattern, full)
		if err != nil {
			out = append(out, Event{Suffix: p.name, Data: map[string]any{
				"process": p.name, "error": err.Error(),
			}})
			continue
		}
		running := len(pids) > 0

		// A wanted state is a filter on whether to speak at all, which
		// is what makes `stopped` useful: the event is the absence.
		switch strings.ToLower(strings.TrimSpace(p.want)) {
		case "running":
			if !running {
				continue
			}
		case "stopped", "dead", "absent":
			if running {
				continue
			}
		}

		data := map[string]any{"process": p.name, "running": running}
		if running {
			data["pids"] = pids
			data["count"] = int64(len(pids))
		}
		out = append(out, Event{Suffix: p.name, Data: data})
	}
	return out, nil
}

// psThreshold fires when a matched process crosses a resource
// threshold.
//
//	beacons:
//	  ps:
//	    - processes:
//	        nginx:
//	          cpu_percent: ['>', 80]
//	          rss_kb: ['>', 500000]
//
// The comparison form is the one the `load` beacon already uses, so an
// operator writing a second beacon is not learning a second grammar.
// The fields are the process table's own, which is what keeps this
// honest: a threshold is compared against the number `ps.psaux`
// reported and nothing is derived on the way.
func psThreshold(c *exec.Context, in *Instance) ([]Event, error) {
	patterns, err := processPatterns(in, "ps")
	if err != nil {
		return nil, err
	}
	full := truthyArg(in, "full")

	var out []Event
	for _, p := range patterns {
		thresholds, ok := p.raw.(*value.Map)
		if !ok || thresholds.Len() == 0 {
			return nil, fmt.Errorf("the ps beacon needs at least one threshold under %q, "+
				"as `cpu_percent: ['>', 80]`", p.name)
		}
		rows, err := psaux(c, p.pattern, full)
		if err != nil {
			out = append(out, Event{Suffix: p.name, Data: map[string]any{
				"process": p.name, "error": err.Error(),
			}})
			continue
		}

		for _, row := range rows {
			for _, t := range thresholds.Entries() {
				field := value.KeyString(t.Key)
				op, want, err := comparison(t.Val)
				if err != nil {
					return nil, fmt.Errorf("%s: %s: %w", p.name, field, err)
				}
				got, ok := numberOf(row, field)
				if !ok {
					return nil, fmt.Errorf("%s: the process table has no %q; it has %s",
						p.name, field, strings.Join(row.StringKeys(), ", "))
				}
				if !compare(op, got, want) {
					continue
				}
				pid, _ := row.Get("pid")
				command, _ := row.Get("command")
				out = append(out, Event{Suffix: p.name, Data: map[string]any{
					"process": p.name, "pid": pid, "command": value.KeyString(command),
					"field": field, "value": got, "comparison": op, "threshold": want,
				}})
			}
		}
	}
	return out, nil
}

// processPattern is one configured entry.
type processPattern struct {
	// name is the key as written, which is what an event is tagged
	// with, and pattern is what is matched. They are the same string;
	// they are named apart because one is an identifier and the other
	// is a regular expression, and reading them as the same thing is
	// how a tag ends up holding a `.*`.
	name    string
	pattern string
	// want is the desired state, for `proc`.
	want string
	// raw is the value as configured, for `ps`'s thresholds.
	raw any
}

// processPatterns reads the `processes` mapping both beacons take.
func processPatterns(in *Instance, beacon string) ([]processPattern, error) {
	raw, ok := in.Arg("processes")
	if !ok {
		return nil, fmt.Errorf("the %s beacon needs `processes`", beacon)
	}
	list, ok := raw.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("`processes` is a mapping of a pattern to what to watch for")
	}
	if list.Len() == 0 {
		// An empty mapping is a beacon that watches nothing, which is
		// far more often a truncated configuration than an intention.
		return nil, fmt.Errorf("the %s beacon's `processes` is empty", beacon)
	}

	out := make([]processPattern, 0, list.Len())
	for _, e := range list.Entries() {
		name := value.KeyString(e.Key)
		out = append(out, processPattern{
			name:    name,
			pattern: name,
			want:    value.KeyString(e.Val),
			raw:     e.Val,
		})
	}
	return out, nil
}

// pgrep asks the node's own `ps` module.
func pgrep(c *exec.Context, pattern string, full bool) ([]any, error) {
	args := value.NewMap(2)
	args.Set("pattern", pattern)
	args.Set("full", full)
	got, err := c.Call("ps.pgrep", args)
	if err != nil {
		return nil, err
	}
	pids, ok := got.([]any)
	if !ok {
		return nil, fmt.Errorf("ps.pgrep answered with a %T", got)
	}
	return pids, nil
}

// psaux asks for the matching rows, sorted so that two polls of an
// unchanged machine produce identical events and `onchangeonly` can do
// its job.
func psaux(c *exec.Context, pattern string, full bool) ([]*value.Map, error) {
	args := value.NewMap(2)
	args.Set("pattern", pattern)
	args.Set("full", full)
	got, err := c.Call("ps.psaux", args)
	if err != nil {
		return nil, err
	}
	list, ok := got.([]any)
	if !ok {
		return nil, fmt.Errorf("ps.psaux answered with a %T", got)
	}
	out := make([]*value.Map, 0, len(list))
	for _, raw := range list {
		if m, ok := raw.(*value.Map); ok {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := numberOf(out[i], "pid")
		b, _ := numberOf(out[j], "pid")
		return a < b
	})
	return out, nil
}

// truthyArg reads an optional boolean.
func truthyArg(in *Instance, name string) bool {
	v, ok := in.Arg(name)
	if !ok {
		return false
	}
	return value.Truthy(v)
}
