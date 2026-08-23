// Package schedule is the node's scheduler: SPEC section 20's jobs that
// run on a clock rather than because a hub said so.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cron is a parsed five-field expression.
//
// Written directly rather than taken from a library, for the reason
// SPEC 4.2 gives about dependencies -- and because the parts of cron
// this deliberately does not implement have to be refused by name
// rather than misinterpreted. A `0 0 L * *` that quietly runs on the
// first of the month is worse than one that will not load.
type Cron struct {
	Minute  fieldSet
	Hour    fieldSet
	Day     fieldSet
	Month   fieldSet
	Weekday fieldSet
	// DayRestricted and WeekdayRestricted record whether each was
	// written as something other than `*`, because standard cron
	// combines the two with OR when both are and with AND otherwise.
	DayRestricted     bool
	WeekdayRestricted bool
	// Reboot marks `@reboot`, which fires once when the scheduler
	// starts and never again.
	Reboot bool
	Expr   string
}

// fieldSet is the set of values one field matches.
type fieldSet map[int]bool

func (f fieldSet) has(v int) bool { return f[v] }

// shorthands are the `@` forms, expanded before parsing.
var shorthands = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var weekdayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// ParseCron reads a five-field expression or an `@` shorthand.
func ParseCron(expr string) (*Cron, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, fmt.Errorf("a cron expression is five fields")
	}
	if strings.EqualFold(trimmed, "@reboot") {
		return &Cron{Reboot: true, Expr: trimmed}, nil
	}
	if expanded, ok := shorthands[strings.ToLower(trimmed)]; ok {
		trimmed = expanded
	}

	fields := strings.Fields(trimmed)
	if len(fields) == 6 {
		return nil, fmt.Errorf(
			"%q has six fields; this build's cron has no seconds field, and a "+
				"schedule finer than a minute is `seconds:` or `every:`", expr)
	}
	if len(fields) != 5 {
		return nil, fmt.Errorf("%q has %d fields; a cron expression has five", expr, len(fields))
	}
	for _, f := range fields {
		if err := refuseUnsupported(f, expr); err != nil {
			return nil, err
		}
	}

	c := &Cron{Expr: expr}
	var err error
	if c.Minute, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("%q: minute: %w", expr, err)
	}
	if c.Hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("%q: hour: %w", expr, err)
	}
	if c.Day, err = parseField(fields[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("%q: day of month: %w", expr, err)
	}
	if c.Month, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("%q: month: %w", expr, err)
	}
	if c.Weekday, err = parseField(fields[4], 0, 7, weekdayNames); err != nil {
		return nil, fmt.Errorf("%q: day of week: %w", expr, err)
	}
	// Seven is Sunday in every cron that accepts it, and zero is too.
	if c.Weekday[7] {
		c.Weekday[0] = true
		delete(c.Weekday, 7)
	}
	c.DayRestricted = strings.TrimSpace(fields[2]) != "*"
	c.WeekdayRestricted = strings.TrimSpace(fields[4]) != "*"
	return c, nil
}

// refuseUnsupported names the cron extensions this build does not have,
// rather than reading them as something else.
func refuseUnsupported(field, expr string) error {
	for _, bad := range []struct {
		token string
		what  string
	}{
		{"L", "`L` (last day)"},
		{"W", "`W` (nearest weekday)"},
		{"#", "`#` (nth weekday)"},
		{"?", "`?` (no specific value)"},
	} {
		if strings.Contains(strings.ToUpper(field), bad.token) {
			return fmt.Errorf("%q uses %s, which this build's cron does not implement", expr, bad.what)
		}
	}
	return nil
}

// parseField reads one field: `*`, a value, a range, a list, or any of
// those with a step.
func parseField(field string, min, max int, names map[string]int) (fieldSet, error) {
	out := fieldSet{}
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("an empty item in %q", field)
		}
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			n, err := strconv.Atoi(strings.TrimSpace(part[i+1:]))
			if err != nil || n < 1 {
				return nil, fmt.Errorf("%q is not a step", part[i+1:])
			}
			step = n
			part = strings.TrimSpace(part[:i])
		}

		low, high := min, max
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			bounds := strings.SplitN(part, "-", 2)
			var err error
			if low, err = fieldValue(bounds[0], min, max, names); err != nil {
				return nil, err
			}
			if high, err = fieldValue(bounds[1], min, max, names); err != nil {
				return nil, err
			}
			if low > high {
				return nil, fmt.Errorf("%q counts backwards", part)
			}
		default:
			v, err := fieldValue(part, min, max, names)
			if err != nil {
				return nil, err
			}
			low, high = v, v
			if step > 1 {
				// `5/15` means "from 5, every 15", which is what cron
				// implementations that accept it do.
				high = max
			}
		}
		for v := low; v <= high; v += step {
			out[v] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%q matches nothing", field)
	}
	return out, nil
}

func fieldValue(s string, min, max int, names map[string]int) (int, error) {
	s = strings.TrimSpace(s)
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a value for this field", s)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("%d is outside %d-%d", v, min, max)
	}
	return v, nil
}

// matchesDay applies standard cron's rule for the two day fields: when
// both are restricted the job runs if either matches, and otherwise
// both have to.
//
// It is a surprising rule and it is the one every cron implements, so a
// crontab moved here has to keep meaning what it meant.
func (c *Cron) matchesDay(day int, weekday time.Weekday) bool {
	dayOK := c.Day.has(day)
	weekdayOK := c.Weekday.has(int(weekday))
	if c.DayRestricted && c.WeekdayRestricted {
		return dayOK || weekdayOK
	}
	return dayOK && weekdayOK
}

// Next is the first minute at or after `after` that the expression
// matches, in the given location.
//
// The walk is in wall-clock fields rather than in absolute time, which
// is what gives SPEC 20.1's two daylight-saving rules for free: an hour
// that is repeated appears once in wall-clock terms, so a job in it runs
// once rather than twice, and an hour that is skipped is detected when
// the constructed time comes back as something other than what was
// asked for.
func (c *Cron) Next(after time.Time, loc *time.Location) (time.Time, bool) {
	if c.Reboot {
		return time.Time{}, false
	}
	if loc == nil {
		loc = time.Local
	}
	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	year, month, day := t.Date()
	hour, minute := t.Hour(), t.Minute()

	// Five years of candidates is past any expression that will ever
	// match; one that does not is a mistake worth reporting as one.
	const limit = 5 * 366 * 24 * 60
	for i := 0; i < limit; i++ {
		if !c.Month.has(int(month)) {
			year, month, day = nextMonth(year, month)
			hour, minute = 0, 0
			continue
		}
		probe := time.Date(year, month, day, 0, 0, 0, 0, loc)
		if probe.Day() != day || probe.Month() != month {
			// A day that does not exist in this month.
			year, month, day = nextMonth(year, month)
			hour, minute = 0, 0
			continue
		}
		if !c.matchesDay(day, probe.Weekday()) {
			year, month, day = nextDay(year, month, day)
			hour, minute = 0, 0
			continue
		}
		if !c.Hour.has(hour) {
			hour++
			minute = 0
			if hour > 23 {
				year, month, day = nextDay(year, month, day)
				hour, minute = 0, 0
			}
			continue
		}
		if !c.Minute.has(minute) {
			minute++
			if minute > 59 {
				minute = 0
				hour++
				if hour > 23 {
					year, month, day = nextDay(year, month, day)
					hour = 0
				}
			}
			continue
		}

		fire := time.Date(year, month, day, hour, minute, 0, 0, loc)
		if fire.Hour() != hour || fire.Minute() != minute {
			// This wall-clock time does not exist: a spring-forward
			// transition skipped it. Go resolves such a time to one
			// side of the gap without saying which, so the instant is
			// computed rather than read off the result. SPEC 20.1 says
			// the job runs once, at the transition.
			if t, ok := transitionAfter(year, month, day, hour, minute, loc); ok && t.After(after) {
				return t, true
			}
			year, month, day, hour, minute = stepMinute(year, month, day, hour, minute)
			continue
		}
		if !fire.After(after) {
			year, month, day, hour, minute = stepMinute(year, month, day, hour, minute)
			continue
		}
		return fire, true
	}
	return time.Time{}, false
}

// stepMinute advances the wall clock by one minute.
func stepMinute(year int, month time.Month, day, hour, minute int) (int, time.Month, int, int, int) {
	minute++
	if minute > 59 {
		minute = 0
		hour++
	}
	if hour > 23 {
		hour = 0
		year, month, day = nextDay(year, month, day)
	}
	return year, month, day, hour, minute
}

// transitionAfter finds the instant a skipped wall-clock time becomes,
// which is the moment the offset changed.
//
// The wall clock jumps -- 01:59 is followed by 03:00 -- so the first
// instant of the day whose wall clock is past the requested one is the
// transition itself. Scanning a day of minutes is only ever done on a
// transition day, and only for a job scheduled inside the gap.
func transitionAfter(year int, month time.Month, day, hour, minute int, loc *time.Location) (time.Time, bool) {
	want := hour*60 + minute
	start := time.Date(year, month, day, 0, 0, 0, 0, loc)
	for i := 0; i <= 24*60; i++ {
		cand := start.Add(time.Duration(i) * time.Minute)
		if cand.Day() != day {
			return time.Time{}, false
		}
		if cand.Hour()*60+cand.Minute() > want {
			return cand, true
		}
	}
	return time.Time{}, false
}

func nextMonth(year int, month time.Month) (int, time.Month, int) {
	if month == time.December {
		return year + 1, time.January, 1
	}
	return year, month + 1, 1
}

func nextDay(year int, month time.Month, day int) (int, time.Month, int) {
	t := time.Date(year, month, day, 12, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	y, m, d := t.Date()
	return y, m, d
}
