package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// Job is one scheduled job, as SPEC 20.1 defines it.
type Job struct {
	Name string
	// Function is what runs, such as `state.apply`.
	Function string
	Args     []string
	Kwargs   *value.Map

	// The timing forms. Exactly one of them decides when the job runs.
	Cron     *Cron
	Interval time.Duration
	When     []time.Time
	Once     time.Time

	// The bounds.
	After time.Time
	Until time.Time
	// Range fires only inside a window, or only outside it with Invert.
	RangeStart, RangeEnd time.Time
	RangeInvert          bool
	// SkipDuring is a window the job never fires in.
	SkipStart, SkipEnd time.Time
	// SkipExplicit are exact times the job never fires at.
	SkipExplicit []time.Time

	// The modifiers.
	Splay      time.Duration
	SplayStart time.Duration
	MaxRunning int
	ReturnJob  bool
	RunOnStart bool
	Catchup    bool
	Enabled    bool
	Offset     time.Duration
	Metadata   *value.Map
	Location   *time.Location

	// LastRun is when it last fired, which an interval measures from
	// and which `catchup` compares against.
	LastRun time.Time
}

// timingNames are the keys that decide when a job runs, for a message
// that can say a definition has none or several.
var timingNames = []string{"cron", "every", "seconds", "minutes", "hours", "days", "when", "once"}

// Parse reads the `schedule` configuration of SPEC 20.1.
func Parse(v any, def *time.Location) ([]*Job, error) {
	if v == nil {
		return nil, nil
	}
	m, ok := v.(*value.Map)
	if !ok {
		return nil, fmt.Errorf("`schedule` is a mapping of job name to its definition")
	}
	var out []*Job
	for _, e := range m.Entries() {
		name := value.KeyString(e.Key)
		body, ok := e.Val.(*value.Map)
		if !ok {
			return nil, fmt.Errorf("schedule %s: the definition is a mapping, not %s",
				name, value.TypeName(e.Val))
		}
		job, err := parseJob(name, body, def)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func parseJob(name string, body *value.Map, def *time.Location) (*Job, error) {
	j := &Job{
		Name:       name,
		Enabled:    true,
		ReturnJob:  true,
		MaxRunning: 1,
		Location:   def,
	}
	if j.Location == nil {
		j.Location = time.Local
	}

	// The time zone first: every other time in the definition is read
	// in it, and reading `when: 03:00` in one zone and then changing to
	// another would give a job that fires at a time nobody wrote.
	if raw, ok := body.Get("timezone"); ok && raw != nil {
		loc, err := time.LoadLocation(value.KeyString(raw))
		if err != nil {
			return nil, fmt.Errorf("schedule %s: timezone: %w", name, err)
		}
		j.Location = loc
	}

	fail := func(key string, err error) error {
		return fmt.Errorf("schedule %s: %s: %w", name, key, err)
	}

	timings := 0
	for _, e := range body.Entries() {
		key := value.KeyString(e.Key)
		v := e.Val
		switch key {
		case "timezone":
		case "function", "func":
			j.Function = value.KeyString(v)
		case "args":
			list, ok := v.([]any)
			if !ok {
				return nil, fail(key, fmt.Errorf("is a list"))
			}
			for _, a := range list {
				j.Args = append(j.Args, value.KeyString(a))
			}
		case "kwargs":
			m, ok := v.(*value.Map)
			if !ok {
				return nil, fail(key, fmt.Errorf("is a mapping"))
			}
			j.Kwargs = m
		case "cron":
			c, err := ParseCron(value.KeyString(v))
			if err != nil {
				return nil, fmt.Errorf("schedule %s: %w", name, err)
			}
			j.Cron = c
			timings++
		case "every":
			d, err := asDuration(v)
			if err != nil {
				return nil, fail(key, err)
			}
			j.Interval = d
			timings++
		case "seconds", "minutes", "hours", "days":
			n, err := asFloat(v)
			if err != nil {
				return nil, fail(key, err)
			}
			j.Interval += time.Duration(n * float64(unitOf(key)))
			timings++
		case "when":
			times, err := parseTimes(v, j.Location, body)
			if err != nil {
				return nil, fail(key, err)
			}
			j.When = times
			timings++
		case "once":
			times, err := parseTimes(v, j.Location, body)
			if err != nil {
				return nil, fail(key, err)
			}
			if len(times) != 1 {
				return nil, fail(key, fmt.Errorf("is one time"))
			}
			j.Once = times[0]
			timings++
		case "once_fmt":
			// Read where `once` and `when` are parsed.
		case "after":
			t, err := parseOneTime(v, j.Location, body)
			if err != nil {
				return nil, fail(key, err)
			}
			j.After = t
		case "until":
			t, err := parseOneTime(v, j.Location, body)
			if err != nil {
				return nil, fail(key, err)
			}
			j.Until = t
		case "range":
			start, end, invert, err := parseRange(v, j.Location, body)
			if err != nil {
				return nil, fail(key, err)
			}
			j.RangeStart, j.RangeEnd, j.RangeInvert = start, end, invert
		case "skip_during_range":
			start, end, _, err := parseRange(v, j.Location, body)
			if err != nil {
				return nil, fail(key, err)
			}
			j.SkipStart, j.SkipEnd = start, end
		case "skip_explicit":
			times, err := parseTimes(v, j.Location, body)
			if err != nil {
				return nil, fail(key, err)
			}
			j.SkipExplicit = times
		case "splay":
			start, end, err := parseSplay(v)
			if err != nil {
				return nil, fail(key, err)
			}
			j.SplayStart, j.Splay = start, end
		case "maxrunning":
			n, err := asFloat(v)
			if err != nil {
				return nil, fail(key, err)
			}
			j.MaxRunning = int(n)
		case "return_job":
			j.ReturnJob = value.Truthy(v)
		case "run_on_start":
			j.RunOnStart = value.Truthy(v)
		case "catchup":
			j.Catchup = value.Truthy(v)
		case "enabled":
			j.Enabled = value.Truthy(v)
		case "offset":
			d, err := asDuration(v)
			if err != nil {
				return nil, fail(key, err)
			}
			j.Offset = d
		case "metadata":
			m, ok := v.(*value.Map)
			if !ok {
				return nil, fail(key, fmt.Errorf("is a mapping"))
			}
			j.Metadata = m
		case "jid_include":
			// Salt's, and it means nothing here: every job this
			// scheduler runs is recorded under a jid either way.
		default:
			return nil, fmt.Errorf("schedule %s: %q is not a schedule setting", name, key)
		}
	}

	if j.Function == "" {
		return nil, fmt.Errorf("schedule %s names no `function` to run", name)
	}
	if timings == 0 {
		return nil, fmt.Errorf("schedule %s says when it runs with none of %s",
			name, strings.Join(timingNames, ", "))
	}
	if timings > 1 && !(j.Interval > 0 && j.Cron == nil && len(j.When) == 0 && j.Once.IsZero()) {
		// `seconds` and `minutes` together are one interval, which Salt
		// allows and which reads well. Two different *forms* are a
		// definition that cannot be answered.
		return nil, fmt.Errorf("schedule %s gives more than one of %s; a job runs on one of them",
			name, strings.Join(timingNames, ", "))
	}
	if j.MaxRunning < 1 {
		j.MaxRunning = 1
	}
	return j, nil
}

func unitOf(key string) time.Duration {
	switch key {
	case "seconds":
		return time.Second
	case "minutes":
		return time.Minute
	case "hours":
		return time.Hour
	case "days":
		return 24 * time.Hour
	}
	return time.Second
}

// Next is when the job should run after `now`, or false if it never
// will again.
//
// The bounds are applied after the timing form has produced a
// candidate, and a candidate the bounds refuse moves the search on
// rather than ending it: `range` and `skip_during_range` name windows a
// recurring job passes through, not the end of it.
func (j *Job) Next(now time.Time) (time.Time, bool) {
	if !j.Enabled {
		return time.Time{}, false
	}
	candidate := now
	for i := 0; i < 4096; i++ {
		next, ok := j.nextCandidate(candidate)
		if !ok {
			return time.Time{}, false
		}
		if j.Offset != 0 {
			next = next.Add(j.Offset)
		}
		if !j.Until.IsZero() && next.After(j.Until) {
			return time.Time{}, false
		}
		if !j.After.IsZero() && next.Before(j.After) {
			candidate = j.After.Add(-time.Nanosecond)
			continue
		}
		if j.blocked(next) {
			candidate = next
			continue
		}
		return next, true
	}
	return time.Time{}, false
}

// nextCandidate applies whichever timing form the job uses.
func (j *Job) nextCandidate(after time.Time) (time.Time, bool) {
	switch {
	case j.Cron != nil:
		if j.Cron.Reboot {
			return time.Time{}, false
		}
		return j.Cron.Next(after, j.Location)
	case !j.Once.IsZero():
		if j.Once.After(after) {
			return j.Once, true
		}
		return time.Time{}, false
	case len(j.When) > 0:
		for _, t := range j.When {
			if t.After(after) {
				return t, true
			}
		}
		return time.Time{}, false
	case j.Interval > 0:
		from := j.LastRun
		if from.IsZero() || from.Before(after) {
			from = after
		}
		return from.Add(j.Interval), true
	}
	return time.Time{}, false
}

// blocked reports whether a window refuses this time.
func (j *Job) blocked(t time.Time) bool {
	if !j.RangeStart.IsZero() && !j.RangeEnd.IsZero() {
		inside := !t.Before(j.RangeStart) && !t.After(j.RangeEnd)
		if inside == j.RangeInvert {
			return true
		}
	}
	if !j.SkipStart.IsZero() && !j.SkipEnd.IsZero() {
		if !t.Before(j.SkipStart) && !t.After(j.SkipEnd) {
			return true
		}
	}
	for _, skip := range j.SkipExplicit {
		if t.Equal(skip) {
			return true
		}
	}
	return false
}

// SplayFor is how long this firing waits before it runs, so that a
// thousand nodes with the same schedule do not arrive at once.
func (j *Job) SplayFor(rand func(int64) int64) time.Duration {
	if j.Splay <= 0 {
		return 0
	}
	low, high := j.SplayStart, j.Splay
	if high <= low {
		low, high = 0, j.Splay
	}
	span := int64(high - low)
	if span <= 0 {
		return low
	}
	return low + time.Duration(rand(span))
}

// ---- reading times ----

// timeLayouts are what a schedule may write a time as.
//
// Salt parses these with dateutil, which accepts a great deal and
// guesses. Guessing about a time is how a job runs an hour out, so this
// takes a list of forms and refuses anything else by name.
var timeLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04",
	"2006-01-02",
	"15:04:05",
	"15:04",
	"3:04pm",
	"3:04PM",
}

func parseTimes(v any, loc *time.Location, body *value.Map) ([]time.Time, error) {
	var raw []any
	switch t := v.(type) {
	case []any:
		raw = t
	default:
		raw = []any{v}
	}
	out := make([]time.Time, 0, len(raw))
	for _, item := range raw {
		t, err := parseOneTime(item, loc, body)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out, nil
}

func parseOneTime(v any, loc *time.Location, body *value.Map) (time.Time, error) {
	s := strings.TrimSpace(value.KeyString(v))
	if s == "" {
		return time.Time{}, fmt.Errorf("is empty")
	}

	layouts := timeLayouts
	if raw, ok := body.Get("once_fmt"); ok && raw != nil {
		layout, err := layoutFromStrftime(value.KeyString(raw))
		if err != nil {
			return time.Time{}, err
		}
		layouts = append([]string{layout}, layouts...)
	}

	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, s, loc)
		if err != nil {
			continue
		}
		if t.Year() == 0 {
			// A time of day with no date means today, and tomorrow
			// once today's has passed. The engine advances it; here it
			// is anchored to today so the comparison has a date.
			now := time.Now().In(loc)
			t = time.Date(now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), t.Second(), 0, loc)
			if !t.After(now) {
				t = t.AddDate(0, 0, 1)
			}
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%q is not a time this build reads; it takes %s",
		s, strings.Join(timeLayouts, ", "))
}

// layoutFromStrftime translates the directives `once_fmt` is written
// with. Salt's is Python's strftime, and an estate that has one has
// written it that way.
func layoutFromStrftime(fmtStr string) (string, error) {
	replacements := map[byte]string{
		'Y': "2006", 'y': "06", 'm': "01", 'd': "02",
		'H': "15", 'M': "04", 'S': "05", 'z': "-0700",
		'b': "Jan", 'B': "January", 'a': "Mon", 'A': "Monday",
		'p': "PM", 'I': "03", '%': "%",
	}
	var b strings.Builder
	for i := 0; i < len(fmtStr); i++ {
		if fmtStr[i] != '%' {
			b.WriteByte(fmtStr[i])
			continue
		}
		if i+1 >= len(fmtStr) {
			return "", fmt.Errorf("once_fmt ends in a bare %%")
		}
		i++
		layout, ok := replacements[fmtStr[i]]
		if !ok {
			return "", fmt.Errorf("once_fmt: %%%c is not a directive this build reads", fmtStr[i])
		}
		b.WriteString(layout)
	}
	return b.String(), nil
}

func parseRange(v any, loc *time.Location, body *value.Map) (time.Time, time.Time, bool, error) {
	m, ok := v.(*value.Map)
	if !ok {
		return time.Time{}, time.Time{}, false, fmt.Errorf("is a mapping with `start` and `end`")
	}
	rawStart, hasStart := m.Get("start")
	rawEnd, hasEnd := m.Get("end")
	if !hasStart || !hasEnd {
		return time.Time{}, time.Time{}, false, fmt.Errorf("needs both `start` and `end`")
	}
	start, err := parseOneTime(rawStart, loc, body)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	end, err := parseOneTime(rawEnd, loc, body)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	invert := false
	if raw, ok := m.Get("invert"); ok {
		invert = value.Truthy(raw)
	}
	return start, end, invert, nil
}

// parseSplay reads an integer of seconds or a `start`/`end` range.
func parseSplay(v any) (time.Duration, time.Duration, error) {
	if m, ok := v.(*value.Map); ok {
		start, err := durationField(m, "start")
		if err != nil {
			return 0, 0, err
		}
		end, err := durationField(m, "end")
		if err != nil {
			return 0, 0, err
		}
		return start, end, nil
	}
	d, err := asDuration(v)
	if err != nil {
		return 0, 0, err
	}
	return 0, d, nil
}

func durationField(m *value.Map, key string) (time.Duration, error) {
	raw, ok := m.Get(key)
	if !ok {
		return 0, fmt.Errorf("needs `%s`", key)
	}
	return asDuration(raw)
}

// asDuration reads a Go duration string or a bare number of seconds,
// which is what `splay: 900` means.
func asDuration(v any) (time.Duration, error) {
	switch t := v.(type) {
	case int64:
		return time.Duration(t) * time.Second, nil
	case float64:
		return time.Duration(t * float64(time.Second)), nil
	case string:
		if d, err := time.ParseDuration(t); err == nil {
			return d, nil
		}
		if n, err := strconv.ParseFloat(t, 64); err == nil {
			return time.Duration(n * float64(time.Second)), nil
		}
		return 0, fmt.Errorf("%q is not a duration", t)
	}
	return 0, fmt.Errorf("%s is not a duration", value.TypeName(v))
}

func asFloat(v any) (float64, error) {
	switch t := v.(type) {
	case int64:
		return float64(t), nil
	case float64:
		return t, nil
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("%s is not a number", value.TypeName(v))
}
