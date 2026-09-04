//go:build windows

package wintask

import (
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// ctx is a context that really runs commands, which these need: a task
// scheduler that is never asked anything proves nothing about the task
// scheduler.
func ctx(t *testing.T) *exec.Context {
	t.Helper()
	return &exec.Context{Runner: &exec.OSRunner{}}
}

// The listing reads the machine's real tasks, and reads them from the
// column that is a path rather than the one that is a word.
func TestListReadsTheMachinesTasks(t *testing.T) {
	names, err := List(ctx(t))
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(names) < 10 {
		t.Fatalf("%d tasks; a working Windows has many more", len(names))
	}
	for _, n := range names {
		if !strings.HasPrefix(n, `\`) {
			t.Errorf("%q is not a task path; the wrong column was read", n)
			break
		}
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("the listing is not sorted: %q before %q", names[i-1], names[i])
		}
	}
}

// A task that ships with Windows is read back with its definition.
//
// The XML declares an encoding it does not use — schtasks says UTF-16
// and writes single-byte text when its output is redirected — so this
// is also the test that the declaration is rewritten rather than
// trusted.
func TestInfoReadsAShippedTask(t *testing.T) {
	c := ctx(t)
	const name = `\Microsoft\Windows\Time Synchronization\SynchronizeTime`

	present, err := Exists(c, name)
	if err != nil {
		t.Fatal(err)
	}
	if !present {
		t.Skip("this Windows does not have the time synchronisation task")
	}

	got, err := Info(c, name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	if got.Path != name {
		t.Errorf("path = %q", got.Path)
	}
	if got.Command == "" {
		t.Error("no command; every task with an Exec action has one")
	}
	if got.RunAs == "" {
		t.Error("no principal; every task runs as something")
	}
	if got.XML == "" {
		t.Error("the definition was not carried through")
	}
	if !strings.Contains(got.XML, "<Task") {
		t.Errorf("the definition does not look like a task: %.80s", got.XML)
	}
}

// "Not there" is its own answer, and it does not come from reading
// schtasks' prose — which is in the machine's own language.
func TestAMissingTaskIsItsOwnError(t *testing.T) {
	c := ctx(t)
	present, err := Exists(c, `\halite-no-such-task`)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("a task that does not exist was found")
	}
	_, err = Info(c, `\halite-no-such-task`)
	if !errors.Is(err, ErrNotExist) {
		t.Errorf("reading a missing task gave %v, want ErrNotExist", err)
	}
}

// Creating, reading back, and deleting is the whole of what a state
// does, so it is exercised end to end against the real scheduler.
//
// Under a folder of its own so that nothing this test writes can be
// mistaken for a task the machine wanted.
func TestATaskIsCreatedReadBackAndDeleted(t *testing.T) {
	c := ctx(t)
	const path = `\halite-test\probe`
	t.Cleanup(func() { _ = Delete(c, path) })

	// The account running the test, not SYSTEM: registering a task that
	// runs as SYSTEM needs administrator rights, and a test that skips
	// on every developer machine is a test that never runs. Everything
	// this exercises is the same either way.
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	want := Task{
		Path:             path,
		Command:          `C:\Windows\System32\cmd.exe`,
		Arguments:        "/c exit 0",
		WorkingDirectory: `C:\Windows\Temp`,
		RunAs:            me.Username,
		RunLevel:         "limited",
		Enabled:          true,
		Description:      "written by halite's tests",
		Triggers:         []string{"daily at 03:30"},
	}
	if err := Create(c, want); err != nil {
		if strings.Contains(err.Error(), "denied") || strings.Contains(err.Error(), "Access") {
			t.Skip("creating a scheduled task needs administrator rights, and this process does not have them")
		}
		t.Fatalf("creating: %v", err)
	}

	got, err := Info(c, path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if !strings.EqualFold(got.Command, want.Command) {
		t.Errorf("command = %q, want %q", got.Command, want.Command)
	}
	if got.Arguments != want.Arguments {
		t.Errorf("arguments = %q, want %q", got.Arguments, want.Arguments)
	}
	if !strings.EqualFold(got.WorkingDirectory, want.WorkingDirectory) {
		t.Errorf("working directory = %q", got.WorkingDirectory)
	}
	if got.RunLevel != "limited" {
		t.Errorf("run level = %q", got.RunLevel)
	}
	if !got.Enabled {
		t.Error("the task was created disabled")
	}
	if got.Description != want.Description {
		t.Errorf("description = %q", got.Description)
	}
	if len(got.Triggers) != 1 || got.Triggers[0] != "daily at 03:30" {
		t.Errorf("triggers = %v, want [daily at 03:30]", got.Triggers)
	}

	// What was declared and what came back have to compare equal, or a
	// state would report a change on every run.
	if !SameDefinition(want, got) {
		t.Errorf("the task read back does not match what was declared:\n  want %+v\n  got  %+v", want, got)
	}

	// Disabling is visible in the definition.
	if err := SetEnabled(c, path, false); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	after, err := Info(c, path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Enabled {
		t.Error("the task is still enabled after being disabled")
	}
	if SameDefinition(want, after) {
		t.Error("a disabled task compares equal to a declaration that says enabled")
	}

	if err := Delete(c, path); err != nil {
		t.Fatalf("deleting: %v", err)
	}
	present, err := Exists(c, path)
	if err != nil {
		t.Fatal(err)
	}
	if present {
		t.Error("the task survived being deleted")
	}

	// Nothing is left in the temporary directory: a definition carries
	// whatever command line a state put in it.
	leftovers, _ := filepath.Glob(filepath.Join(os.TempDir(), "halite-task-*.xml"))
	if len(leftovers) > 0 {
		t.Errorf("definitions were left behind: %v", leftovers)
	}
}

// The generated XML is what the scheduler takes, and its fixed parts are
// fixed for a reason.
func TestGeneratedXMLCarriesWhatTheSchedulerNeeds(t *testing.T) {
	got, err := GenerateXML(Task{
		Path: `\x`, Command: "cmd.exe", Enabled: true,
		Triggers: []string{"daily at 03:00"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The namespace is part of the schema; without it the scheduler
	// refuses the document with an error that names no element.
	if !strings.Contains(got, "schemas.microsoft.com/windows/2004/02/mit/task") {
		t.Error("the generated definition has no namespace")
	}
	if !strings.Contains(got, "<Command>cmd.exe</Command>") {
		t.Errorf("the command is not in the definition:\n%s", got)
	}
	// The date on a daily trigger is fixed rather than today's: a state
	// that regenerated it with today's date would differ from the host
	// every run and never converge.
	if !strings.Contains(got, "<StartBoundary>1970-01-01T03:00:00</StartBoundary>") {
		t.Errorf("the start boundary is not the fixed one:\n%s", got)
	}
	// A task with no principal runs as whoever registered it, so a state
	// run by a service would produce a different task from the same file
	// run by an operator.
	if !strings.Contains(got, "<UserId>S-1-5-18</UserId>") {
		t.Errorf("the default principal is not SYSTEM:\n%s", got)
	}

	// Text that would break the document is escaped.
	escaped, err := GenerateXML(Task{
		Path: `\x`, Command: "cmd.exe", Arguments: `/c echo "a" & echo <b>`,
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(escaped, "<b>") {
		t.Errorf("an argument was not escaped:\n%s", escaped)
	}
	if !strings.Contains(escaped, "&amp;") {
		t.Errorf("an ampersand was not escaped:\n%s", escaped)
	}
}

// A trigger this build cannot generate is refused by name, saying what
// it can write and how to say the rest.
func TestATriggerThatCannotBeGeneratedIsRefused(t *testing.T) {
	_, err := GenerateXML(Task{Path: `\x`, Command: "cmd.exe",
		Triggers: []string{"every third tuesday"}})
	if err == nil {
		t.Fatal("an unsupported trigger was accepted")
	}
	for _, want := range []string{"daily at", "boot", "XML"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	// And a malformed time is refused as a time.
	if _, err := GenerateXML(Task{Path: `\x`, Command: "c", Triggers: []string{"daily at 3pm"}}); err == nil {
		t.Error("a time that is not HH:MM was accepted")
	}
	if _, err := GenerateXML(Task{Path: `\x`, Command: "c", Triggers: []string{"daily at 25:00"}}); err == nil {
		t.Error("an impossible hour was accepted")
	}
}

// A task name means the same thing with or without its leading
// backslash, so a state that declares one and reads the other has to
// converge.
func TestATaskPathIsNormalized(t *testing.T) {
	for _, in := range []string{`halite\x`, `\halite\x`, `/halite/x`, ` \halite\x `} {
		if got := normalizePath(in); got != `\halite\x` {
			t.Errorf("normalizePath(%q) = %q", in, got)
		}
	}
}

// A well-known account has a name and a SID and the scheduler stores
// whichever it was given, so a declaration in one form must match a
// reading in the other.
func TestWellKnownAccountsCompareEqualByEitherName(t *testing.T) {
	for _, pair := range [][2]string{
		{"SYSTEM", "S-1-5-18"},
		{`NT AUTHORITY\SYSTEM`, "S-1-5-18"},
		{"Local Service", "S-1-5-19"},
	} {
		if !sameAccount(pair[0], pair[1]) {
			t.Errorf("%q and %q should be one account", pair[0], pair[1])
		}
	}
	if sameAccount("SYSTEM", "S-1-5-19") {
		t.Error("two different accounts compared equal")
	}
}

// The parser has to survive the document schtasks actually prints:
// an encoding declaration it does not honour, and doubled carriage
// returns.
func TestTheParserSurvivesWhatSchtasksActuallyPrints(t *testing.T) {
	raw := "<?xml version=\"1.0\" encoding=\"UTF-16\"?>\r\r\n" +
		"<Task version=\"1.2\" xmlns=\"http://schemas.microsoft.com/windows/2004/02/mit/task\">\r\r\n" +
		"  <Settings><Enabled>false</Enabled></Settings>\r\r\n" +
		"  <Actions><Exec><Command>cmd.exe</Command></Exec></Actions>\r\r\n" +
		"</Task>\r\r\n"
	parsed, err := parseXML([]byte(raw))
	if err != nil {
		t.Fatalf("the document schtasks prints could not be read: %v", err)
	}
	got := toTask(`\x`, parsed, raw)
	if got.Command != "cmd.exe" {
		t.Errorf("command = %q", got.Command)
	}
	if got.Enabled {
		t.Error("a disabled task was read as enabled")
	}

	// And a document that really is UTF-16, which is what the scheduler
	// writes to a file.
	utf16Doc := []byte{0xFF, 0xFE}
	for _, r := range "<Task><Actions><Exec><Command>x.exe</Command></Exec></Actions></Task>" {
		utf16Doc = append(utf16Doc, byte(r), 0)
	}
	parsed, err = parseXML(utf16Doc)
	if err != nil {
		t.Fatalf("a UTF-16 document could not be read: %v", err)
	}
	if got := toTask(`\x`, parsed, ""); got.Command != "x.exe" {
		t.Errorf("command from UTF-16 = %q", got.Command)
	}
}

// Only the fields a state stated are compared. A task read back carries
// forty settings the declaration never mentioned, and comparing them all
// would make every state report a change forever.
func TestOnlyWhatWasDeclaredIsCompared(t *testing.T) {
	current := Task{
		Command: "cmd.exe", Arguments: "/c x", WorkingDirectory: `C:\tmp`,
		RunAs: "S-1-5-18", RunLevel: "highest", Enabled: true,
		Description: "something the host has", Author: "somebody else",
		Triggers: []string{"at boot"},
	}
	// A declaration that mentions the command and nothing else matches.
	if !SameDefinition(Task{Command: "cmd.exe", Arguments: "/c x", Enabled: true}, current) {
		t.Error("a partial declaration reported a change it did not ask about")
	}
	// One that mentions a field and disagrees does not.
	if SameDefinition(Task{Command: "other.exe", Enabled: true}, current) {
		t.Error("a different command compared equal")
	}
	if SameDefinition(Task{Command: "cmd.exe", Arguments: "/c x", Enabled: false}, current) {
		t.Error("a different enabled state compared equal")
	}
	// `boot` and `at boot` are one trigger.
	if !SameDefinition(Task{Command: "cmd.exe", Arguments: "/c x", Enabled: true,
		Triggers: []string{"boot"}}, current) {
		t.Error("`boot` and `at boot` should be one trigger")
	}
}
