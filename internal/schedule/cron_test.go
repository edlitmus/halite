package schedule

import (
	"testing"
	"time"
)

func mustCron(t *testing.T, expr string) *Cron {
	t.Helper()
	c, err := ParseCron(expr)
	if err != nil {
		t.Fatalf("%q: %v", expr, err)
	}
	return c
}

func at(t *testing.T, loc *time.Location, s string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestCronNextFireTimes(t *testing.T) {
	utc := time.UTC
	cases := []struct {
		expr, from, want string
	}{
		{"17 3 * * *", "2026-08-23 00:00", "2026-08-23 03:17"},
		{"17 3 * * *", "2026-08-23 03:17", "2026-08-24 03:17"},
		{"*/15 * * * *", "2026-08-23 00:01", "2026-08-23 00:15"},
		{"0 0 1 1 *", "2026-08-23 00:00", "2027-01-01 00:00"},
		{"@daily", "2026-08-23 12:00", "2026-08-24 00:00"},
		{"@hourly", "2026-08-23 12:30", "2026-08-23 13:00"},
		{"0 9 * * mon", "2026-08-23 00:00", "2026-08-24 09:00"},
		{"0 9 * * 1-5", "2026-08-22 00:00", "2026-08-24 09:00"},
		{"30 2 29 feb *", "2026-08-23 00:00", "2028-02-29 02:30"},
		{"0 12 1,15 * *", "2026-08-23 00:00", "2026-09-01 12:00"},
	}
	for _, c := range cases {
		got, ok := mustCron(t, c.expr).Next(at(t, utc, c.from), utc)
		if !ok {
			t.Errorf("%q from %s never fires", c.expr, c.from)
			continue
		}
		if want := at(t, utc, c.want); !got.Equal(want) {
			t.Errorf("%q from %s fired at %s, want %s", c.expr, c.from, got.Format(time.RFC3339), c.want)
		}
	}
}

// Standard cron's surprising rule: when both day fields are restricted,
// either matching is enough. A crontab moved here has to keep meaning
// what it meant.
func TestBothDayFieldsRestrictedMeansEither(t *testing.T) {
	utc := time.UTC
	// The 1st of the month, or any Monday.
	c := mustCron(t, "0 0 1 * mon")
	got, _ := c.Next(at(t, utc, "2026-08-23 00:00"), utc)
	// 2026-08-24 is a Monday, and comes before the 1st of September.
	if want := at(t, utc, "2026-08-24 00:00"); !got.Equal(want) {
		t.Errorf("fired at %s, want %s", got, want)
	}

	// With only one restricted, it is the only one that counts.
	only := mustCron(t, "0 0 1 * *")
	got, _ = only.Next(at(t, utc, "2026-08-23 00:00"), utc)
	if want := at(t, utc, "2026-09-01 00:00"); !got.Equal(want) {
		t.Errorf("fired at %s, want %s", got, want)
	}
}

// SPEC 20.1 is explicit about daylight saving because it is a common
// source of missed runs.
func TestDaylightSavingTransitions(t *testing.T) {
	// New York springs forward at 02:00 on 2026-03-08 and falls back at
	// 02:00 on 2026-11-01.
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no time zone database: %v", err)
	}

	// The skipped hour: 02:30 does not exist that day, and the job runs
	// once, at the transition.
	skipped := mustCron(t, "30 2 * * *")
	got, ok := skipped.Next(at(t, loc, "2026-03-08 00:00"), loc)
	if !ok {
		t.Fatal("a job in the skipped hour never fires")
	}
	if got.Day() != 8 || got.Month() != time.March {
		t.Fatalf("the job moved to %s", got)
	}
	if got.Hour() == 2 {
		t.Errorf("02:30 does not exist that day, and the job claims to have run at %s", got)
	}
	// And it does not run twice: the next one is the following day.
	next, _ := skipped.Next(got, loc)
	if next.Day() != 9 {
		t.Errorf("the job ran again on the same day, at %s", next)
	}

	// The repeated hour: 01:30 happens twice in absolute time, and the
	// job runs once.
	repeated := mustCron(t, "30 1 * * *")
	first, _ := repeated.Next(at(t, loc, "2026-11-01 00:00"), loc)
	if first.Day() != 1 || first.Hour() != 1 || first.Minute() != 30 {
		t.Fatalf("the job fired at %s", first)
	}
	second, _ := repeated.Next(first, loc)
	if second.Day() == 1 {
		t.Errorf("the job ran twice in the repeated hour: %s then %s", first, second)
	}
}

// The parts of cron this build does not implement are refused by name.
// A `0 0 L * *` that quietly runs on the first of the month is worse
// than one that will not load.
func TestUnsupportedCronIsRefusedByName(t *testing.T) {
	cases := map[string]string{
		"last day":      "0 0 L * *",
		"nearest":       "0 0 15W * *",
		"nth weekday":   "0 0 * * 5#3",
		"no value":      "0 0 ? * *",
		"seconds field": "*/5 * * * * *",
		"four fields":   "0 0 * *",
		"backwards":     "0 0 * * 5-1",
		"a bad step":    "*/0 * * * *",
		"out of range":  "0 25 * * *",
		"not a value":   "0 0 * * funday",
		"empty":         "",
	}
	for name, expr := range cases {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("%s (%q) was accepted", name, expr)
		}
	}
}

// `@reboot` fires once when the scheduler starts and never on a clock.
func TestRebootHasNoNextTime(t *testing.T) {
	c := mustCron(t, "@reboot")
	if !c.Reboot {
		t.Fatal("@reboot did not parse as one")
	}
	if _, ok := c.Next(time.Now(), time.UTC); ok {
		t.Error("@reboot claims a next fire time")
	}
}
