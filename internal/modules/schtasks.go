package modules

import (
	"fmt"
	"strconv"
	"strings"
)

// Windows has no crontab. cron.present and cron.absent drive schtasks(1)
// there instead, under a \halite\ folder so halite's tasks are separable
// from everyone else's.
//
// The translation from cron fields to schtasks flags is the part worth
// getting right, so it lives here as pure logic with its own tests. Cron's
// five fields are more expressive than schtasks' schedule types, so the
// combinations that do not map are refused by name rather than silently
// approximated.

// taskPath is the scheduled task an identifier maps to.
func taskPath(identifier string) string {
	return `\halite\` + identifier
}

// scheduleArgs translates cron fields into schtasks scheduling flags.
func scheduleArgs(args map[string]any) ([]string, error) {
	minute := Str(args, "minute", "*")
	hour := Str(args, "hour", "*")
	daymonth := Str(args, "daymonth", "*")
	month := Str(args, "month", "*")
	dayweek := Str(args, "dayweek", "*")

	if month != "*" {
		return nil, fmt.Errorf("month is not supported by scheduled tasks; use daymonth or a wrapper script")
	}
	if daymonth != "*" && dayweek != "*" {
		return nil, fmt.Errorf("daymonth and dayweek cannot both be set for a scheduled task")
	}

	// Every N minutes.
	if every, ok := interval(minute); ok {
		if hour != "*" || daymonth != "*" || dayweek != "*" {
			return nil, fmt.Errorf("a */N minute schedule cannot be combined with hour, daymonth, or dayweek")
		}
		return []string{"/SC", "MINUTE", "/MO", strconv.Itoa(every)}, nil
	}
	// Every N hours, at a fixed minute.
	if every, ok := interval(hour); ok {
		if daymonth != "*" || dayweek != "*" {
			return nil, fmt.Errorf("a */N hour schedule cannot be combined with daymonth or dayweek")
		}
		at, err := startTime("0", minute)
		if err != nil {
			return nil, err
		}
		return []string{"/SC", "HOURLY", "/MO", strconv.Itoa(every), "/ST", at}, nil
	}

	if minute == "*" {
		return nil, fmt.Errorf("a scheduled task needs a minute, or a */N interval")
	}
	if hour == "*" {
		// Once an hour, at a fixed minute.
		if daymonth != "*" || dayweek != "*" {
			return nil, fmt.Errorf("an hourly schedule cannot be combined with daymonth or dayweek")
		}
		at, err := startTime("0", minute)
		if err != nil {
			return nil, err
		}
		return []string{"/SC", "HOURLY", "/MO", "1", "/ST", at}, nil
	}

	at, err := startTime(hour, minute)
	if err != nil {
		return nil, err
	}
	switch {
	case dayweek != "*":
		day, err := weekday(dayweek)
		if err != nil {
			return nil, err
		}
		return []string{"/SC", "WEEKLY", "/D", day, "/ST", at}, nil
	case daymonth != "*":
		day, err := numberInRange(daymonth, 1, 31, "daymonth")
		if err != nil {
			return nil, err
		}
		return []string{"/SC", "MONTHLY", "/D", strconv.Itoa(day), "/ST", at}, nil
	default:
		return []string{"/SC", "DAILY", "/ST", at}, nil
	}
}

// interval reads a "*/N" field.
func interval(field string) (int, bool) {
	rest, ok := strings.CutPrefix(field, "*/")
	if !ok {
		return 0, false
	}
	every, err := strconv.Atoi(rest)
	if err != nil || every < 1 {
		return 0, false
	}
	return every, true
}

// startTime renders an hour and minute as schtasks' HH:MM.
func startTime(hour, minute string) (string, error) {
	h, err := numberInRange(hour, 0, 23, "hour")
	if err != nil {
		return "", err
	}
	m, err := numberInRange(minute, 0, 59, "minute")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%02d:%02d", h, m), nil
}

func numberInRange(field string, low, high int, name string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a number a scheduled task can use", name, field)
	}
	if value < low || value > high {
		return 0, fmt.Errorf("%s %d is out of range %d-%d", name, value, low, high)
	}
	return value, nil
}

// weekday maps a cron day-of-week to the abbreviation schtasks wants.
func weekday(field string) (string, error) {
	names := []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}
	if number, err := strconv.Atoi(strings.TrimSpace(field)); err == nil {
		if number == 7 {
			number = 0 // both 0 and 7 mean Sunday in cron
		}
		if number < 0 || number > 6 {
			return "", fmt.Errorf("dayweek %d is out of range 0-7", number)
		}
		return names[number], nil
	}
	upper := strings.ToUpper(strings.TrimSpace(field))
	for _, name := range names {
		if upper == name || strings.HasPrefix(upper, name) {
			return name, nil
		}
	}
	return "", fmt.Errorf("dayweek %q is not a day", field)
}

// taskCommand reads the command a scheduled task runs, so an unchanged
// state does not recreate it. It parses `schtasks /Query /FO LIST /V`,
// whose "Task To Run:" line is the command.
func taskCommand(output string) (string, bool) {
	for _, line := range strings.Split(output, "\n") {
		label, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(label), "Task To Run") {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

// queryTask returns the command a task runs, and whether it exists.
func queryTask(identifier string) (command string, exists bool) {
	out, _, rc, err := run("schtasks", "/Query", "/TN", taskPath(identifier), "/FO", "LIST", "/V")
	if err != nil || rc != 0 {
		return "", false
	}
	command, _ = taskCommand(out)
	return command, true
}

// createTask creates or replaces a scheduled task.
func createTask(identifier, command string, schedule []string, user string) error {
	argv := append([]string{"/Create", "/F", "/TN", taskPath(identifier), "/TR", command}, schedule...)
	if user != "" {
		argv = append(argv, "/RU", user)
	}
	_, errOut, rc, err := run("schtasks", argv...)
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("schtasks exited %d: %s", rc, strings.TrimSpace(errOut))
	}
	return nil
}

func deleteTask(identifier string) error {
	_, errOut, rc, err := run("schtasks", "/Delete", "/TN", taskPath(identifier), "/F")
	if err != nil {
		return err
	}
	if rc != 0 {
		return fmt.Errorf("schtasks exited %d: %s", rc, strings.TrimSpace(errOut))
	}
	return nil
}

// cronPresentWindows is cron.present against the task scheduler.
func cronPresentWindows(c *Ctx, id string, args map[string]any) Result {
	if !has("schtasks") {
		return resFail("schtasks not found")
	}
	identifier := Str(args, "identifier", id)
	command := Str(args, "name", id)
	schedule, err := scheduleArgs(args)
	if err != nil {
		return resFail("%v", err)
	}

	current, exists := queryTask(identifier)
	if exists && current == command {
		// Only the command is compared: schtasks does not report a schedule
		// in a form worth parsing back. Changing just the schedule needs a
		// cron.absent first — see docs/states.md.
		return resOK(fmt.Sprintf("scheduled task %s is present", taskPath(identifier)))
	}
	if c.Test {
		if exists {
			return resWould(fmt.Sprintf("scheduled task %s would be updated", taskPath(identifier)))
		}
		return resWould(fmt.Sprintf("scheduled task %s would be created", taskPath(identifier)))
	}
	if err := createTask(identifier, command, schedule, Str(args, "user", "")); err != nil {
		return resFail("%v", err)
	}
	changes := map[string]string{"new": command + " (" + strings.Join(schedule, " ") + ")"}
	if exists {
		changes["old"] = current
		return resChanged("scheduled task updated", changes)
	}
	return resChanged("scheduled task created", changes)
}

// cronAbsentWindows is cron.absent against the task scheduler.
func cronAbsentWindows(c *Ctx, id string, args map[string]any) Result {
	if !has("schtasks") {
		return resFail("schtasks not found")
	}
	identifier := Str(args, "identifier", id)
	if _, exists := queryTask(identifier); !exists {
		return resOK(fmt.Sprintf("scheduled task %s already absent", taskPath(identifier)))
	}
	if c.Test {
		return resWould(fmt.Sprintf("scheduled task %s would be removed", taskPath(identifier)))
	}
	if err := deleteTask(identifier); err != nil {
		return resFail("%v", err)
	}
	return resChanged("scheduled task removed", map[string]string{"removed": taskPath(identifier)})
}
