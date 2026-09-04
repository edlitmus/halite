package wintask

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"unicode/utf16"
)

// Task is a scheduled task, in the fields a state declares.
//
// A subset of the schema on purpose. The full Task Scheduler format has
// upwards of sixty elements, most of which an estate never sets, and a
// module that claimed to manage all of them would be claiming to
// round-trip settings it never reads. What is here is what a tree
// declares; XML carries the rest untouched for a caller that needs it.
type Task struct {
	// Path is the task's full name, including its folder:
	// `\halite\nightly`. The leading backslash is the root folder and is
	// not optional in the scheduler's own vocabulary.
	Path string
	// Command and Arguments are what the task runs.
	Command   string
	Arguments string
	// WorkingDirectory is where it runs. Empty is the scheduler's
	// default, which is %windir%\system32 and rarely what was meant.
	WorkingDirectory string
	// RunAs is the account, as the scheduler records it: a name, or a
	// well-known SID such as S-1-5-18 for SYSTEM.
	RunAs string
	// RunLevel is "limited" or "highest".
	RunLevel string
	// Enabled is whether the scheduler will run it.
	Enabled bool
	// Description and Author are what the console shows.
	Description string
	Author      string
	// Triggers describe when it runs, one line each, in the schema's own
	// vocabulary — `daily at 03:00`, `at logon`, `boot`.
	Triggers []string
	// XML is the definition as the scheduler holds it, untouched. A
	// caller that needs an element this type does not model reads it
	// here, and a state that wants an exact definition writes it back.
	XML string
}

// ---- the subset of the schema this package reads ----
//
// Named types rather than a generic tree, because the fields a state
// declares have to be found reliably and a generic walk would find
// `Command` inside an `Exec` and inside a `ComHandler` alike.

type taskXML struct {
	XMLName          xml.Name             `xml:"Task"`
	RegistrationInfo taskRegistrationInfo `xml:"RegistrationInfo"`
	Triggers         taskTriggers         `xml:"Triggers"`
	Principals       taskPrincipals       `xml:"Principals"`
	Settings         taskSettings         `xml:"Settings"`
	Actions          taskActions          `xml:"Actions"`
}

type taskRegistrationInfo struct {
	Description string `xml:"Description"`
	Author      string `xml:"Author"`
	URI         string `xml:"URI"`
}

type taskPrincipals struct {
	Principal []taskPrincipal `xml:"Principal"`
}

type taskPrincipal struct {
	UserID    string `xml:"UserId"`
	GroupID   string `xml:"GroupId"`
	RunLevel  string `xml:"RunLevel"`
	LogonType string `xml:"LogonType"`
}

type taskSettings struct {
	Enabled *bool `xml:"Enabled"`
}

type taskActions struct {
	Exec []taskExec `xml:"Exec"`
}

type taskExec struct {
	Command          string `xml:"Command"`
	Arguments        string `xml:"Arguments"`
	WorkingDirectory string `xml:"WorkingDirectory"`
}

type taskTriggers struct {
	Boot     []taskTrigger     `xml:"BootTrigger"`
	Logon    []taskTrigger     `xml:"LogonTrigger"`
	Time     []taskTrigger     `xml:"TimeTrigger"`
	Calendar []calendarTrigger `xml:"CalendarTrigger"`
	Idle     []taskTrigger     `xml:"IdleTrigger"`
	Event    []taskTrigger     `xml:"EventTrigger"`
	Session  []taskTrigger     `xml:"SessionStateChangeTrigger"`
	Registr  []taskTrigger     `xml:"RegistrationTrigger"`
}

type taskTrigger struct {
	StartBoundary string `xml:"StartBoundary"`
	Enabled       *bool  `xml:"Enabled"`
}

type calendarTrigger struct {
	StartBoundary  string        `xml:"StartBoundary"`
	Enabled        *bool         `xml:"Enabled"`
	ScheduleByDay  *scheduleDays `xml:"ScheduleByDay"`
	ScheduleByWeek *scheduleDays `xml:"ScheduleByWeek"`
	ScheduleByMnth *scheduleDays `xml:"ScheduleByMonth"`
}

type scheduleDays struct {
	DaysInterval  string `xml:"DaysInterval"`
	WeeksInterval string `xml:"WeeksInterval"`
}

// parseXML reads a task definition.
//
// schtasks declares `encoding="UTF-16"` on every document it prints and
// then, when its output is redirected rather than going to a console,
// writes single-byte text. Go's parser believes the declaration and
// fails on the first character. The declaration is rewritten rather than
// trusted, and a document that really is UTF-16 — which is what the
// scheduler writes to a file — is decoded first.
func parseXML(raw []byte) (*taskXML, error) {
	text := decodeUTF16(raw)
	var t taskXML
	dec := xml.NewDecoder(strings.NewReader(stripEncoding(text)))
	// A document that names some other encoding is passed through
	// unchanged rather than refused: the bytes are already runes by the
	// time they reach here.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	if err := dec.Decode(&t); err != nil {
		return nil, fmt.Errorf("reading the task definition: %w", err)
	}
	return &t, nil
}

// decodeUTF16 converts a UTF-16 document to UTF-8 when it is one.
//
// Decided by the byte-order mark rather than by the declaration, because
// the declaration is the thing that lies.
func decodeUTF16(raw []byte) string {
	if len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE {
		return decodeUTF16LE(raw[2:])
	}
	if len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF {
		return decodeUTF16BE(raw[2:])
	}
	// No mark. A UTF-16 document with no mark still has a zero byte
	// after its first ASCII character, which UTF-8 never does.
	if len(raw) >= 2 && raw[0] != 0 && raw[1] == 0 {
		return decodeUTF16LE(raw)
	}
	return string(raw)
}

func decodeUTF16LE(raw []byte) string {
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(units))
}

func decodeUTF16BE(raw []byte) string {
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])<<8|uint16(raw[i+1]))
	}
	return string(utf16.Decode(units))
}

// stripEncoding removes the encoding attribute from the declaration, so
// that the parser reads the runes it was given rather than the ones the
// document claims.
func stripEncoding(text string) string {
	start := strings.Index(text, "<?xml")
	if start < 0 {
		return text
	}
	end := strings.Index(text[start:], "?>")
	if end < 0 {
		return text
	}
	end += start + 2
	decl := text[start:end]
	if !strings.Contains(decl, "encoding") {
		return text
	}
	return text[:start] + `<?xml version="1.0"?>` + text[end:]
}

// toTask flattens a parsed definition into the fields a state declares.
func toTask(path string, parsed *taskXML, raw string) Task {
	out := Task{
		Path:        path,
		Description: parsed.RegistrationInfo.Description,
		Author:      parsed.RegistrationInfo.Author,
		Enabled:     true,
		XML:         raw,
	}
	if parsed.Settings.Enabled != nil {
		out.Enabled = *parsed.Settings.Enabled
	}
	if len(parsed.Actions.Exec) > 0 {
		// The first action. A task may hold several, and a state that
		// declares one command is describing a task with one; a caller
		// that needs the rest reads the XML.
		out.Command = parsed.Actions.Exec[0].Command
		out.Arguments = parsed.Actions.Exec[0].Arguments
		out.WorkingDirectory = parsed.Actions.Exec[0].WorkingDirectory
	}
	if len(parsed.Principals.Principal) > 0 {
		p := parsed.Principals.Principal[0]
		out.RunAs = p.UserID
		if out.RunAs == "" {
			out.RunAs = p.GroupID
		}
		out.RunLevel = "limited"
		if strings.EqualFold(p.RunLevel, "HighestAvailable") {
			out.RunLevel = "highest"
		}
	}
	out.Triggers = describeTriggers(parsed.Triggers)
	return out
}

// describeTriggers renders the triggers as one line each.
//
// A summary rather than a model. Reproducing the schedule types
// faithfully would mean modelling the whole schema, and what a caller
// asking `win_task.info` wants is to recognise the task — "daily at
// 03:00" — not to reconstruct it. A state that needs the exact schedule
// declares it as XML.
func describeTriggers(t taskTriggers) []string {
	var out []string
	for range t.Boot {
		out = append(out, "at boot")
	}
	for range t.Logon {
		out = append(out, "at logon")
	}
	for range t.Registr {
		out = append(out, "when registered")
	}
	for range t.Idle {
		out = append(out, "when idle")
	}
	for range t.Event {
		out = append(out, "on an event")
	}
	for range t.Session {
		out = append(out, "on a session change")
	}
	for _, x := range t.Time {
		out = append(out, "once at "+timeOf(x.StartBoundary))
	}
	for _, c := range t.Calendar {
		switch {
		case c.ScheduleByDay != nil:
			every := c.ScheduleByDay.DaysInterval
			if every == "" || every == "1" {
				out = append(out, "daily at "+timeOf(c.StartBoundary))
			} else {
				out = append(out, fmt.Sprintf("every %s days at %s", every, timeOf(c.StartBoundary)))
			}
		case c.ScheduleByWeek != nil:
			out = append(out, "weekly at "+timeOf(c.StartBoundary))
		case c.ScheduleByMnth != nil:
			out = append(out, "monthly at "+timeOf(c.StartBoundary))
		default:
			out = append(out, "on a calendar schedule")
		}
	}
	return out
}

// timeOf takes the clock time out of an ISO 8601 start boundary.
func timeOf(boundary string) string {
	_, clock, found := strings.Cut(boundary, "T")
	if !found {
		return boundary
	}
	if len(clock) >= 5 {
		return clock[:5]
	}
	return clock
}

// encodeUTF16LE renders a document as UTF-16 with a byte-order mark.
//
// The scheduler reads a definition file in the encoding its declaration
// names, and schtasks writes `encoding="UTF-16"` on everything it
// prints. A UTF-8 file under that declaration is refused with "The task
// XML is malformed" — a message that names neither the encoding nor a
// line number, and sends the reader looking for a syntax error that is
// not there.
func encodeUTF16LE(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, 0, len(units)*2+2)
	out = append(out, 0xFF, 0xFE)
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

// SameXML reports whether two definitions are the same document.
//
// Compared after normalising whitespace and the encoding declaration,
// because the scheduler rewrites both: a definition handed to it comes
// back reindented, with `\r\r\n` line endings, and with its own
// declaration. Comparing the bytes would make a state that declares XML
// report a change on every run.
//
// Not a semantic comparison. Two documents that differ only in element
// order are the same task to the scheduler and different here, which is
// the safe direction: it reports a change that is not one rather than
// missing one that is.
func SameXML(a, b string) bool {
	return normalizeXML(a) == normalizeXML(b)
}

func normalizeXML(s string) string {
	text := stripEncoding(s)
	var b strings.Builder
	space := false
	for _, r := range text {
		switch r {
		case ' ', '\t', '\r', '\n':
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return strings.ReplaceAll(b.String(), "> <", "><")
}
