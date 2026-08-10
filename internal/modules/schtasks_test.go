package modules

import (
	"strings"
	"testing"
)

func TestScheduleArgsTranslatesCronFields(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			"every 30 minutes",
			map[string]any{"minute": "*/30"},
			"/SC MINUTE /MO 30",
		},
		{
			"every 2 hours on the half hour",
			map[string]any{"minute": "30", "hour": "*/2"},
			"/SC HOURLY /MO 2 /ST 00:30",
		},
		{
			"hourly at a fixed minute",
			map[string]any{"minute": "5"},
			"/SC HOURLY /MO 1 /ST 00:05",
		},
		{
			"daily",
			map[string]any{"minute": "15", "hour": "3"},
			"/SC DAILY /ST 03:15",
		},
		{
			"weekly by number",
			map[string]any{"minute": "0", "hour": "4", "dayweek": "1"},
			"/SC WEEKLY /D MON /ST 04:00",
		},
		{
			"weekly by name",
			map[string]any{"minute": "0", "hour": "4", "dayweek": "sunday"},
			"/SC WEEKLY /D SUN /ST 04:00",
		},
		{
			"sunday as seven",
			map[string]any{"minute": "0", "hour": "4", "dayweek": "7"},
			"/SC WEEKLY /D SUN /ST 04:00",
		},
		{
			"monthly",
			map[string]any{"minute": "0", "hour": "1", "daymonth": "14"},
			"/SC MONTHLY /D 14 /ST 01:00",
		},
	}
	for _, c := range cases {
		got, err := scheduleArgs(c.args)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if joined := strings.Join(got, " "); joined != c.want {
			t.Errorf("%s: got %q, want %q", c.name, joined, c.want)
		}
	}
}

func TestScheduleArgsRefusesWhatItCannotExpress(t *testing.T) {
	// Cron's five fields are more expressive than schtasks' schedule
	// types. Anything that would have to be approximated is refused by
	// name instead.
	cases := map[string]map[string]any{
		"a month":                  {"minute": "0", "hour": "0", "month": "3"},
		"daymonth and dayweek":     {"minute": "0", "hour": "0", "daymonth": "1", "dayweek": "1"},
		"interval with an hour":    {"minute": "*/10", "hour": "3"},
		"interval with a dayweek":  {"minute": "*/10", "dayweek": "1"},
		"hour interval with a day": {"minute": "0", "hour": "*/4", "daymonth": "1"},
		"no minute at all":         {"hour": "3"},
		"minute out of range":      {"minute": "99"},
		"hour out of range":        {"minute": "0", "hour": "25"},
		"daymonth out of range":    {"minute": "0", "hour": "0", "daymonth": "40"},
		"dayweek out of range":     {"minute": "0", "hour": "0", "dayweek": "9"},
		"dayweek nonsense":         {"minute": "0", "hour": "0", "dayweek": "someday"},
		"minute list":              {"minute": "0,30"},
		"minute range":             {"minute": "0-30"},
	}
	for name, args := range cases {
		if got, err := scheduleArgs(args); err == nil {
			t.Errorf("%s: translated to %q, want a refusal", name, strings.Join(got, " "))
		}
	}
}

func TestTaskPathNamespacesHalitesTasks(t *testing.T) {
	if got := taskPath("converge"); got != `\halite\converge` {
		t.Errorf("got %q", got)
	}
}

func TestTaskCommandParsesQueryOutput(t *testing.T) {
	// Trimmed shape of `schtasks /Query /FO LIST /V`.
	output := "Folder: \\halite\r\n" +
		"HostName:      WEB1\r\n" +
		"TaskName:      \\halite\\converge\r\n" +
		"Next Run Time: 09/08/2026 20:00:00\r\n" +
		"Status:        Ready\r\n" +
		"Task To Run:   C:\\halite\\halite.exe apply\r\n" +
		"Start In:      N/A\r\n"

	command, ok := taskCommand(output)
	if !ok {
		t.Fatal("no command found")
	}
	// The value contains colons of its own, which must survive.
	if command != `C:\halite\halite.exe apply` {
		t.Errorf("got %q", command)
	}
}

func TestTaskCommandOnOutputWithoutIt(t *testing.T) {
	if _, ok := taskCommand("ERROR: The system cannot find the file specified.\r\n"); ok {
		t.Error("found a command in an error message")
	}
}
