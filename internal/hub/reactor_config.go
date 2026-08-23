package hub

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/value"
)

// ReactorEntry is one tag glob and the reaction files it runs, with the
// controls SPEC 18.2 puts on it.
type ReactorEntry struct {
	// Tag is the glob an event is matched against.
	Tag string
	// SLS are the reaction files, as paths or `salt://` URIs.
	SLS []string
	// Principal is the identity the reaction runs as. SPEC 18.3: a
	// reactor is subject to the RBAC policy exactly like a human
	// caller, which is the departure from Salt -- there the reactor
	// runs with full control-plane privilege, so a node that can fire
	// the right event can cause arbitrary fleet-wide execution.
	Principal string

	// Debounce collapses a burst into one reaction after quiescence.
	Debounce time.Duration
	// DedupeWindow drops a repeat of the same key inside the window.
	DedupeWindow time.Duration
	// DedupeKey is a dotted path into the event payload. Empty uses the
	// tag, which is what "the same event twice" means when the payload
	// carries nothing to tell two apart.
	DedupeKey string
	// RateLimit is a token bucket in events per second, so one noisy
	// source cannot starve the rest. Zero is no limit.
	RateLimit float64
	// RateBurst is how many may arrive at once. Zero takes the limit.
	RateBurst int
}

// DefaultReactorPrincipal is what a reactor entry runs as when it names
// no principal.
//
// A name rather than a blank: the policy denies by default, so an
// unnamed reactor is authorized for nothing until someone writes a
// binding for this principal and says what it may do. That is the
// intended shape -- a reactor that silently inherited the hub's
// authority is the Salt behaviour SPEC 18.3 exists to remove.
const DefaultReactorPrincipal = "reactor:default"

// ParseReactors reads the `reactor` configuration of SPEC 18.1.
//
// Two forms are accepted. Salt's is a list of single-key mappings from
// tag glob to file list, and an existing configuration uses it. The
// extended form is a mapping with `tag` and `sls` keys, which is how an
// entry carries the SPEC 18.2 controls and its principal -- there is
// nowhere to put them in the first form.
func ParseReactors(v any) ([]ReactorEntry, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("`reactor` is a list of tag globs and the files they run")
	}

	var out []ReactorEntry
	for i, item := range list {
		m, ok := item.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("reactor entry %d is not a mapping", i+1)
		}
		entry, err := parseReactorEntry(m, i+1)
		if err != nil {
			return nil, err
		}
		if err := eventbus.ValidTag(strings.ReplaceAll(entry.Tag, "*", "x")); err != nil {
			return nil, fmt.Errorf("reactor entry %d: %w", i+1, err)
		}
		out = append(out, entry)
	}
	return out, nil
}

func parseReactorEntry(m *value.Map, n int) (ReactorEntry, error) {
	// The extended form declares its tag. The Salt form is a mapping
	// with exactly one key, which is the tag.
	if _, extended := m.Get("tag"); !extended {
		if m.Len() != 1 {
			return ReactorEntry{}, fmt.Errorf(
				"reactor entry %d has %d keys; Salt's form is one tag glob to a list of files, "+
					"and an entry with options is a mapping with `tag` and `sls`", n, m.Len())
		}
		e := m.Entries()[0]
		files, err := reactorFiles(e.Val, n)
		if err != nil {
			return ReactorEntry{}, err
		}
		return ReactorEntry{
			Tag:       value.KeyString(e.Key),
			SLS:       files,
			Principal: DefaultReactorPrincipal,
		}, nil
	}

	entry := ReactorEntry{Principal: DefaultReactorPrincipal}
	tag, _ := m.Get("tag")
	entry.Tag = value.KeyString(tag)
	if entry.Tag == "" {
		return ReactorEntry{}, fmt.Errorf("reactor entry %d has an empty `tag`", n)
	}
	raw, ok := m.Get("sls")
	if !ok {
		return ReactorEntry{}, fmt.Errorf("reactor entry %d (%s) names no `sls` to run", n, entry.Tag)
	}
	files, err := reactorFiles(raw, n)
	if err != nil {
		return ReactorEntry{}, err
	}
	entry.SLS = files

	if p, ok := m.Get("principal"); ok && value.KeyString(p) != "" {
		entry.Principal = value.KeyString(p)
	}
	for _, field := range []struct {
		key string
		set func(time.Duration)
	}{
		{"debounce", func(d time.Duration) { entry.Debounce = d }},
		{"dedupe_window", func(d time.Duration) { entry.DedupeWindow = d }},
	} {
		raw, ok := m.Get(field.key)
		if !ok || raw == nil {
			continue
		}
		d, err := parseReactorDuration(raw)
		if err != nil {
			return ReactorEntry{}, fmt.Errorf("reactor entry %d (%s): %s: %w", n, entry.Tag, field.key, err)
		}
		field.set(d)
	}
	if k, ok := m.Get("dedupe_key"); ok {
		entry.DedupeKey = value.KeyString(k)
	}
	if raw, ok := m.Get("rate_limit"); ok && raw != nil {
		rate, burst, err := parseRateLimit(raw)
		if err != nil {
			return ReactorEntry{}, fmt.Errorf("reactor entry %d (%s): rate_limit: %w", n, entry.Tag, err)
		}
		entry.RateLimit, entry.RateBurst = rate, burst
	}
	return entry, nil
}

// reactorFiles reads the file list, accepting one file written as a
// bare string.
func reactorFiles(v any, n int) ([]string, error) {
	switch t := v.(type) {
	case string:
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s := value.KeyString(item)
			if s == "" {
				return nil, fmt.Errorf("reactor entry %d lists an empty file name", n)
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("reactor entry %d lists no files", n)
		}
		return out, nil
	}
	return nil, fmt.Errorf("reactor entry %d: the files are a list, not %s", n, value.TypeName(v))
}

func parseReactorDuration(v any) (time.Duration, error) {
	switch t := v.(type) {
	case int64:
		return time.Duration(t) * time.Second, nil
	case float64:
		return time.Duration(t * float64(time.Second)), nil
	case string:
		d, err := time.ParseDuration(t)
		if err != nil {
			return 0, fmt.Errorf("%q is not a duration", t)
		}
		return d, nil
	}
	return 0, fmt.Errorf("%s is not a duration", value.TypeName(v))
}

// parseRateLimit reads `10/s`, `10`, or `600/m`.
func parseRateLimit(v any) (float64, int, error) {
	spec := strings.TrimSpace(value.KeyString(v))
	if spec == "" {
		return 0, 0, nil
	}
	per := 1.0
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		unit := strings.TrimSpace(spec[i+1:])
		switch unit {
		case "s", "sec", "second":
			per = 1
		case "m", "min", "minute":
			per = 60
		case "h", "hour":
			per = 3600
		default:
			return 0, 0, fmt.Errorf("%q is not a period; try s, m, or h", unit)
		}
		spec = strings.TrimSpace(spec[:i])
	}
	n, err := strconv.ParseFloat(spec, 64)
	if err != nil || n <= 0 {
		return 0, 0, fmt.Errorf("%q is not a positive number of events", spec)
	}
	rate := n / per
	burst := int(n)
	if burst < 1 {
		burst = 1
	}
	return rate, burst, nil
}

// Matching returns the entries whose glob matches a tag, in
// configuration order so that a reaction list reads the way it is
// written.
func Matching(entries []ReactorEntry, tag string) []ReactorEntry {
	var out []ReactorEntry
	for _, e := range entries {
		if eventbus.MatchTag(e.Tag, tag) {
			out = append(out, e)
		}
	}
	return out
}

// ReactorTags lists the distinct globs, which is what the bus is asked
// to filter on so an idle estate does not wake a worker per event.
func ReactorTags(entries []ReactorEntry) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if seen[e.Tag] {
			continue
		}
		seen[e.Tag] = true
		out = append(out, e.Tag)
	}
	sort.Strings(out)
	return out
}
