//go:build windows

package wintask

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/windows"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileperm"
)

// ErrNotExist is returned for a task that is not there.
//
// Its own error, because "not there" is the answer to `exists` and a
// failure to `info`, and a caller has to tell it from a permission
// problem without reading the message. schtasks says so in prose and in
// a locale, so the exit code and the shape of the failure are what this
// keys off rather than the words.
var ErrNotExist = errNotExist{}

type errNotExist struct{}

func (errNotExist) Error() string { return "no such scheduled task" }

// run invokes schtasks and returns what it printed.
//
// Through the command runner rather than os/exec, so that a task
// operation gets the same clean environment, the same timeout, and the
// same process-tree kill as every other child this system starts.
func run(c *exec.Context, args ...string) (string, error) {
	res, err := c.Run(exec.Command{
		Argv:           append([]string{"schtasks.exe"}, args...),
		IgnoreExitCode: true,
	})
	if err != nil {
		return "", fmt.Errorf("running schtasks: %w", err)
	}
	out := decodeUTF16([]byte(res.Stdout))
	if res.Code != 0 {
		text := strings.TrimSpace(decodeUTF16([]byte(res.Stderr)))
		if text == "" {
			text = strings.TrimSpace(out)
		}
		// schtasks reports a missing task with 1 and a message that is
		// localised, so the words are not what this keys off: the caller
		// asked about a task by name, and a failure to find it is the
		// one failure the caller has a different answer for. Exists
		// checks the listing instead, which does not depend on this.
		return "", fmt.Errorf("schtasks: %s", firstLine(text))
	}
	return out, nil
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// normalizePath puts a task name in the form the scheduler uses.
//
// A leading backslash is the root folder and is not decoration:
// `\halite\nightly` and `halite\nightly` name the same task to
// schtasks, and comparing them as strings without this makes a state
// that reads one and declares the other never converge.
func normalizePath(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	return `\` + strings.Trim(strings.ReplaceAll(trimmed, "/", `\`), `\`)
}

// List names every task the scheduler holds, sorted.
//
// The CSV form with no header. Its first column is the task's path,
// which is a path and not a word, so it does not change with the
// machine's language — unlike the status column beside it, which does
// and which this does not read.
func List(c *exec.Context) ([]string, error) {
	out, err := run(c, "/query", "/fo", "csv", "/nh")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := splitCSV(line)
		if len(fields) == 0 || fields[0] == "" {
			continue
		}
		// A folder with no tasks prints its own name and nothing else,
		// which is a heading rather than a task.
		if strings.HasSuffix(fields[0], `\`) {
			continue
		}
		names = append(names, fields[0])
	}
	sort.Strings(names)
	return names, nil
}

// splitCSV reads one line of schtasks' quoted CSV.
//
// Written here rather than through encoding/csv because schtasks emits
// `\r\r\n` line endings, which that package reads as a bare carriage
// return inside a field and refuses.
func splitCSV(line string) []string {
	var out []string
	var field strings.Builder
	inQuotes := false
	for i := 0; i < len(line); i++ {
		switch ch := line[i]; {
		case ch == '"' && inQuotes && i+1 < len(line) && line[i+1] == '"':
			field.WriteByte('"')
			i++
		case ch == '"':
			inQuotes = !inQuotes
		case ch == ',' && !inQuotes:
			out = append(out, field.String())
			field.Reset()
		case ch == '\r':
		default:
			field.WriteByte(ch)
		}
	}
	out = append(out, field.String())
	return out
}

// Exists reports whether a task is there.
//
// Against the listing rather than by asking for the task and reading the
// failure, because schtasks reports a missing task in prose and in the
// machine's own language.
func Exists(c *exec.Context, name string) (bool, error) {
	want := normalizePath(name)
	if want == "" {
		return false, fmt.Errorf("no task was named")
	}
	names, err := List(c)
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if strings.EqualFold(normalizePath(n), want) {
			return true, nil
		}
	}
	return false, nil
}

// Info reads one task's definition.
func Info(c *exec.Context, name string) (Task, error) {
	path := normalizePath(name)
	if path == "" {
		return Task{}, fmt.Errorf("no task was named")
	}
	raw, err := run(c, "/query", "/tn", path, "/xml", "ONE")
	if err != nil {
		present, listErr := Exists(c, path)
		if listErr == nil && !present {
			return Task{}, fmt.Errorf("%s: %w", path, ErrNotExist)
		}
		return Task{}, err
	}
	parsed, err := parseXML([]byte(raw))
	if err != nil {
		return Task{}, fmt.Errorf("%s: %w", path, err)
	}
	return toTask(path, parsed, raw), nil
}

// Create registers a task from a definition, replacing one of the same
// name.
//
// Through the scheduler's own XML rather than through schtasks' flags.
// The flag form cannot express a working directory, a run level, or a
// description at all, and its `/sc` vocabulary is a lossy projection of
// the schedule types — so a task created through it could not be read
// back and compared against what was declared.
func Create(c *exec.Context, t Task) error {
	path := normalizePath(t.Path)
	if path == "" {
		return fmt.Errorf("a task needs a name")
	}
	body := t.XML
	if body == "" {
		generated, err := GenerateXML(t)
		if err != nil {
			return err
		}
		body = generated
	}

	file, err := writeDefinition(body)
	if err != nil {
		return err
	}
	defer os.Remove(file)

	// /f replaces an existing task of the same name, which is what a
	// state declaring a definition means: the task is what the file
	// says, not what it says merged with whatever was there.
	_, err = run(c, "/create", "/tn", path, "/xml", file, "/f")
	return err
}

// writeDefinition puts the XML where schtasks can read it.
//
// A file rather than standard input, because /xml takes a path and
// nothing else. It is written where only its owner can read it: a task
// definition carries a command line, and a command line carries
// whatever a state put in it.
func writeDefinition(body string) (string, error) {
	f, err := os.CreateTemp("", "halite-task-*.xml")
	if err != nil {
		return "", fmt.Errorf("writing the task definition: %w", err)
	}
	name := f.Name()
	if err := fileperm.ApplyFile(f, 0o600); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("writing the task definition: %w", err)
	}
	// UTF-16 with a byte-order mark, because that is what the
	// declaration on the document says and what the scheduler reads. A
	// UTF-8 file under a UTF-16 declaration is refused with "The task
	// XML is malformed" — a message that names neither the encoding nor
	// a line, and sends the reader looking for a syntax error that is
	// not there.
	if _, err := f.Write(encodeUTF16LE(body)); err != nil {
		f.Close()
		os.Remove(name)
		return "", fmt.Errorf("writing the task definition: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("writing the task definition: %w", err)
	}
	return name, nil
}

// Delete removes a task.
func Delete(c *exec.Context, name string) error {
	path := normalizePath(name)
	if path == "" {
		return fmt.Errorf("no task was named")
	}
	_, err := run(c, "/delete", "/tn", path, "/f")
	return err
}

// Run starts a task now, out of its schedule.
func Run(c *exec.Context, name string) error {
	_, err := run(c, "/run", "/tn", normalizePath(name))
	return err
}

// Stop ends a task that is running.
func Stop(c *exec.Context, name string) error {
	_, err := run(c, "/end", "/tn", normalizePath(name))
	return err
}

// SetEnabled turns a task on or off.
func SetEnabled(c *exec.Context, name string, on bool) error {
	flag := "/disable"
	if on {
		flag = "/enable"
	}
	_, err := run(c, "/change", "/tn", normalizePath(name), flag)
	return err
}

// ---- generating a definition ----

// GenerateXML renders a declared task as the scheduler's own XML.
//
// The schema is fixed and its namespace is part of it: a document
// without the namespace is refused by the scheduler with an error that
// does not say which element was wrong.
func GenerateXML(t Task) (string, error) {
	if t.Command == "" {
		return "", fmt.Errorf("a task needs a command to run")
	}
	trigger, err := generateTrigger(t)
	if err != nil {
		return "", err
	}

	runAs := t.RunAs
	if runAs == "" {
		// SYSTEM, because a task with no principal runs as whoever
		// registered it and a state run by a service would then produce
		// a different task from the same file run by an operator.
		runAs = "S-1-5-18"
	}
	runLevel := "LeastPrivilege"
	if strings.EqualFold(t.RunLevel, "highest") {
		runLevel = "HighestAvailable"
	}
	enabled := "true"
	if !t.Enabled {
		enabled = "false"
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-16"?>` + "\n")
	b.WriteString(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` + "\n")
	b.WriteString("  <RegistrationInfo>\n")
	if t.Description != "" {
		b.WriteString("    <Description>" + escape(t.Description) + "</Description>\n")
	}
	author := t.Author
	if author == "" {
		author = "halite"
	}
	b.WriteString("    <Author>" + escape(author) + "</Author>\n")
	b.WriteString("  </RegistrationInfo>\n")
	b.WriteString("  <Triggers>\n" + trigger + "  </Triggers>\n")
	b.WriteString("  <Principals>\n    <Principal id=\"Author\">\n")
	b.WriteString("      <UserId>" + escape(runAs) + "</UserId>\n")
	b.WriteString("      <RunLevel>" + runLevel + "</RunLevel>\n")
	b.WriteString("    </Principal>\n  </Principals>\n")
	b.WriteString("  <Settings>\n")
	b.WriteString("    <Enabled>" + enabled + "</Enabled>\n")
	b.WriteString("    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\n")
	b.WriteString("    <StartWhenAvailable>true</StartWhenAvailable>\n")
	b.WriteString("    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\n")
	b.WriteString("    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\n")
	b.WriteString("  </Settings>\n")
	b.WriteString("  <Actions Context=\"Author\">\n    <Exec>\n")
	b.WriteString("      <Command>" + escape(t.Command) + "</Command>\n")
	if t.Arguments != "" {
		b.WriteString("      <Arguments>" + escape(t.Arguments) + "</Arguments>\n")
	}
	if t.WorkingDirectory != "" {
		b.WriteString("      <WorkingDirectory>" + escape(t.WorkingDirectory) + "</WorkingDirectory>\n")
	}
	b.WriteString("    </Exec>\n  </Actions>\n</Task>\n")
	return b.String(), nil
}

// generateTrigger renders the one trigger a declared task carries.
//
// One, and stated in the vocabulary a state writes. A task with several
// triggers, or with a schedule this does not name, is declared as XML —
// which is why Task carries the XML through.
func generateTrigger(t Task) (string, error) {
	if len(t.Triggers) == 0 {
		// A task with no trigger is legal and runs only when something
		// asks it to, which is what `win_task.run` is for.
		return "", nil
	}
	if len(t.Triggers) > 1 {
		return "", fmt.Errorf("this build generates one trigger; a task with %d is declared "+
			"by giving win_task.present the scheduler's own XML", len(t.Triggers))
	}
	spec := strings.TrimSpace(t.Triggers[0])
	lower := strings.ToLower(spec)

	switch {
	case lower == "boot" || lower == "at boot":
		return "    <BootTrigger>\n      <Enabled>true</Enabled>\n    </BootTrigger>\n", nil
	case lower == "logon" || lower == "at logon":
		return "    <LogonTrigger>\n      <Enabled>true</Enabled>\n    </LogonTrigger>\n", nil
	case strings.HasPrefix(lower, "daily at "):
		clock := strings.TrimSpace(spec[len("daily at "):])
		start, err := startBoundary(clock)
		if err != nil {
			return "", err
		}
		return "    <CalendarTrigger>\n" +
			"      <StartBoundary>" + start + "</StartBoundary>\n" +
			"      <Enabled>true</Enabled>\n" +
			"      <ScheduleByDay>\n        <DaysInterval>1</DaysInterval>\n      </ScheduleByDay>\n" +
			"    </CalendarTrigger>\n", nil
	case strings.HasPrefix(lower, "once at "):
		clock := strings.TrimSpace(spec[len("once at "):])
		start, err := startBoundary(clock)
		if err != nil {
			return "", err
		}
		return "    <TimeTrigger>\n" +
			"      <StartBoundary>" + start + "</StartBoundary>\n" +
			"      <Enabled>true</Enabled>\n" +
			"    </TimeTrigger>\n", nil
	}
	return "", fmt.Errorf("%q is not a trigger this build generates; it writes "+
		"`boot`, `logon`, `daily at HH:MM` and `once at HH:MM`. "+
		"Anything else is declared by giving win_task.present the scheduler's own XML", spec)
}

// startBoundary turns a clock time into the ISO 8601 the schema takes.
//
// The date is fixed rather than today's, because a daily trigger's date
// is the day it first becomes eligible and a state that regenerated it
// with today's date would differ from the host on every run and never
// converge. 1970 is before any Windows and is what a task created this
// way should say forever.
func startBoundary(clock string) (string, error) {
	trimmed := strings.TrimSpace(clock)
	hh, mm, found := strings.Cut(trimmed, ":")
	if !found || len(hh) == 0 || len(hh) > 2 || len(mm) != 2 {
		return "", fmt.Errorf("%q is not a time of day; write it as HH:MM", clock)
	}
	for _, part := range []string{hh, mm} {
		for _, r := range part {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("%q is not a time of day; write it as HH:MM", clock)
			}
		}
	}
	if len(hh) == 1 {
		hh = "0" + hh
	}
	if hh > "23" || mm > "59" {
		return "", fmt.Errorf("%q is not a time of day", clock)
	}
	return fmt.Sprintf("1970-01-01T%s:%s:00", hh, mm), nil
}

// escape renders text safe inside an element.
func escape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// SameDefinition reports whether a host's task matches what a state
// declared, comparing only the fields the state stated.
//
// Only those. A task read back from the scheduler carries forty settings
// the declaration never mentioned, and comparing them all would make
// every state report a change forever.
func SameDefinition(declared, current Task) bool {
	if declared.Command != "" && !strings.EqualFold(declared.Command, current.Command) {
		return false
	}
	if declared.Arguments != current.Arguments {
		return false
	}
	if declared.WorkingDirectory != "" &&
		!strings.EqualFold(declared.WorkingDirectory, current.WorkingDirectory) {
		return false
	}
	if declared.Enabled != current.Enabled {
		return false
	}
	if declared.RunLevel != "" && !strings.EqualFold(declared.RunLevel, current.RunLevel) {
		return false
	}
	if declared.RunAs != "" && !sameAccount(declared.RunAs, current.RunAs) {
		return false
	}
	if len(declared.Triggers) > 0 && !sameTriggers(declared.Triggers, current.Triggers) {
		return false
	}
	return true
}

// sameAccount compares two principals.
//
// A well-known account has a name and a SID, and the scheduler stores
// whichever it was given: a state declaring `SYSTEM` against a task
// recorded as `S-1-5-18` is declaring the same account, and reporting a
// change there would never converge.
func sameAccount(a, b string) bool {
	return strings.EqualFold(canonicalAccount(a), canonicalAccount(b))
}

var accountSIDs = map[string]string{
	"system":                        "S-1-5-18",
	"nt authority\\system":          "S-1-5-18",
	"localsystem":                   "S-1-5-18",
	"local service":                 "S-1-5-19",
	"nt authority\\local service":   "S-1-5-19",
	"network service":               "S-1-5-20",
	"nt authority\\network service": "S-1-5-20",
}

// canonicalAccount renders an account as the SID the scheduler stores.
//
// The scheduler always stores a SID. A task registered with
// `<UserId>ED-PC\ed</UserId>` reads back as
// `<UserId>S-1-5-21-…-1001</UserId>`, so comparing the two as strings
// makes a state that declares an account by name report a change on
// every run and never converge. That was measured against the real
// scheduler on this host; the well-known table alone did not catch it,
// because it only covers SYSTEM and the two service accounts.
//
// The table is kept in front of the lookup so that SYSTEM resolves
// without asking the domain controller, which is the common case and the
// one that has to work on a machine with no network.
func canonicalAccount(s string) string {
	trimmed := strings.TrimSpace(s)
	if sid, ok := accountSIDs[strings.ToLower(trimmed)]; ok {
		return sid
	}
	if strings.HasPrefix(strings.ToUpper(trimmed), "S-1-") {
		return trimmed
	}
	if sid, _, _, err := windows.LookupSID("", trimmed); err == nil {
		return sid.String()
	}
	// An account this machine cannot resolve — a domain account on a
	// host that cannot reach its domain. Compared by name, which is the
	// best that can be done and is right when both sides say the same
	// thing.
	return trimmed
}

// sameTriggers compares a declared trigger against what the host holds,
// in the summary form both are rendered to.
func sameTriggers(declared, current []string) bool {
	if len(declared) != len(current) {
		return false
	}
	want := append([]string(nil), declared...)
	have := append([]string(nil), current...)
	for i := range want {
		want[i] = normalizeTrigger(want[i])
	}
	for i := range have {
		have[i] = normalizeTrigger(have[i])
	}
	sort.Strings(want)
	sort.Strings(have)
	for i := range want {
		if want[i] != have[i] {
			return false
		}
	}
	return true
}

// normalizeTrigger renders a declaration and a reading into one form, so
// that `boot` and `at boot` compare equal.
func normalizeTrigger(s string) string {
	t := strings.ToLower(strings.TrimSpace(s))
	switch t {
	case "boot", "at boot":
		return "at boot"
	case "logon", "at logon":
		return "at logon"
	}
	return t
}

// TempDir is where a generated definition is written, exposed so a test
// can see that nothing is left behind.
func TempDir() string { return filepath.Clean(os.TempDir()) }
