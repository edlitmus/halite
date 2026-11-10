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

// A one-time trigger generated from only a clock time has no date, so
// GenerateXML wrote the scheduler's one fixed placeholder date, 1970 --
// right for `daily at`, where only the clock matters, and wrong here,
// where the StartBoundary is the one moment the trigger ever fires.
// Confirmed directly on this host: a task registered with
// `<StartBoundary>1970-01-01T03:30:00</StartBoundary>` on a TimeTrigger
// reported "Next Run Time: N/A" to `schtasks /query /v` and was never
// scheduled. `once at HH:MM` is refused rather than generating that
// (issue 608 item 5, lead L9).
func TestOnceAtIsRefusedRatherThanGeneratingATriggerThatNeverFires(t *testing.T) {
	_, err := GenerateXML(Task{Path: `\x`, Command: "cmd.exe",
		Triggers: []string{"once at 03:30"}})
	if err == nil {
		t.Fatal("a one-time trigger with no date was accepted")
	}
	for _, want := range []string{"date", "never runs", "xml"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// The real evidence behind the refusal above: a TimeTrigger whose
// StartBoundary is the fixed placeholder date "daily at" uses is
// accepted by the scheduler and then never runs. This creates a real
// task under this account (no administrator rights needed) to show
// the scheduler's own answer, not a prediction of it.
func TestARealTimeTriggerDatedNineteenSeventyNeverRuns(t *testing.T) {
	c := ctx(t)
	const path = `\halite-test\dated-1970`
	t.Cleanup(func() { _ = Delete(c, path) })

	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	xml := `<?xml version="1.0" encoding="UTF-16"?>` +
		`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` +
		`<Triggers><TimeTrigger><StartBoundary>1970-01-01T03:30:00</StartBoundary>` +
		`<Enabled>true</Enabled></TimeTrigger></Triggers>` +
		`<Principals><Principal id="Author"><UserId>` + me.Username + `</UserId>` +
		`<LogonType>InteractiveToken</LogonType></Principal></Principals>` +
		`<Settings><Enabled>true</Enabled></Settings>` +
		`<Actions Context="Author"><Exec><Command>cmd.exe</Command>` +
		`<Arguments>/c exit 0</Arguments></Exec></Actions></Task>`
	if err := Create(c, Task{Path: path, XML: xml}); err != nil {
		if strings.Contains(err.Error(), "denied") || strings.Contains(err.Error(), "Access") {
			t.Skip("creating a scheduled task needs administrator rights, and this process does not have them")
		}
		t.Fatalf("creating: %v", err)
	}
	raw, err := run(c, "/query", "/tn", path, "/v", "/fo", "list")
	if err != nil {
		t.Fatalf("querying: %v", err)
	}
	if !strings.Contains(raw, "Next Run Time:") {
		t.Fatalf("the verbose listing does not have a Next Run Time line:\n%s", raw)
	}
	nextRun := ""
	for _, line := range strings.Split(raw, "\n") {
		if name, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) == "Next Run Time" {
			nextRun = strings.TrimSpace(value)
		}
	}
	if nextRun != "N/A" {
		t.Errorf("Next Run Time = %q, want N/A: the scheduler now runs a trigger dated 1970", nextRun)
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

// The parser has to survive the document schtasks actually prints: an
// encoding declaration it does not honour, and doubled carriage
// returns.
//
// This is schtasks' own output, captured on this host (issue 608 item
// 5, lead L17, which found the fixture here built by hand instead):
// `Create` with `Task{Command: "cmd.exe", Enabled: false, RunAs:
// "S-1-5-18"}`, then `/query /tn ... /xml ONE`, redirected the same way
// `run` redirects it. Shortened to the elements this test and
// `toTask` read; the rest is the shape of a real document, not a
// guess at one.
func TestTheParserSurvivesWhatSchtasksActuallyPrints(t *testing.T) {
	raw := "<?xml version=\"1.0\" encoding=\"UTF-16\"?>\r\r\n" +
		"<Task version=\"1.2\" xmlns=\"http://schemas.microsoft.com/windows/2004/02/mit/task\">\r\r\n" +
		"  <RegistrationInfo>\r\r\n" +
		"    <Author>halite</Author>\r\r\n" +
		"    <URI>\\halite-capture-test</URI>\r\r\n" +
		"  </RegistrationInfo>\r\r\n" +
		"  <Principals>\r\r\n" +
		"    <Principal id=\"Author\">\r\r\n" +
		"      <UserId>S-1-5-18</UserId>\r\r\n" +
		"      <LogonType>InteractiveToken</LogonType>\r\r\n" +
		"    </Principal>\r\r\n" +
		"  </Principals>\r\r\n" +
		"  <Settings>\r\r\n" +
		"    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>\r\r\n" +
		"    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>\r\r\n" +
		"    <Enabled>false</Enabled>\r\r\n" +
		"    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>\r\r\n" +
		"    <StartWhenAvailable>true</StartWhenAvailable>\r\r\n" +
		"    <IdleSettings>\r\r\n" +
		"      <StopOnIdleEnd>true</StopOnIdleEnd>\r\r\n" +
		"      <RestartOnIdle>false</RestartOnIdle>\r\r\n" +
		"    </IdleSettings>\r\r\n" +
		"  </Settings>\r\r\n" +
		"  <Triggers />\r\r\n" +
		"  <Actions Context=\"Author\">\r\r\n" +
		"    <Exec>\r\r\n" +
		"      <Command>cmd.exe</Command>\r\r\n" +
		"    </Exec>\r\r\n" +
		"  </Actions>\r\r\n" +
		"</Task>"
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

// SameDefinition never compared Description at all, declared or not, so
// a description change made through a state was silently never applied
// (issue 608 item 5, lead L6).
func TestDescriptionIsComparedWhenDeclared(t *testing.T) {
	current := Task{Command: "cmd.exe", Description: "old"}
	if SameDefinition(Task{Command: "cmd.exe", Description: "new"}, current) {
		t.Error("a different description compared equal")
	}
	if !SameDefinition(Task{Command: "cmd.exe", Description: "old"}, current) {
		t.Error("the same description compared unequal")
	}
	// Consistent with every other optional field here: omitting it is
	// not declaring that it must be empty.
	if !SameDefinition(Task{Command: "cmd.exe"}, current) {
		t.Error("omitting the description reported a change it did not ask about")
	}
}

// A daily trigger declared with an unpadded hour, "3:30" rather than
// "03:30", has to converge against the scheduler's own padded reading
// of the same trigger, or win_task.present replaced the task on every
// run (issue 608 item 5, lead L4).
func TestADailyTriggerWithAnUnpaddedHourConverges(t *testing.T) {
	c := ctx(t)
	const path = `\halite-test\unpadded-hour`
	t.Cleanup(func() { _ = Delete(c, path) })

	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	want := Task{
		Path: path, Command: `C:\Windows\System32\cmd.exe`, Arguments: "/c exit 0",
		RunAs: me.Username, Enabled: true,
		Triggers: []string{"daily at 3:30"},
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
	if len(got.Triggers) != 1 || got.Triggers[0] != "daily at 03:30" {
		t.Fatalf("triggers = %v, want the scheduler's own padded reading [daily at 03:30]", got.Triggers)
	}
	if !SameDefinition(want, got) {
		t.Errorf("an unpadded hour in the declaration never matched the scheduler's padded reading:\n  want %+v\n  got  %+v", want, got)
	}
}

// A declaration given as XML names no URI -- nothing in the schema
// requires one and nothing a state author writes would think to -- but
// the scheduler adds one to RegistrationInfo on registration, naming the
// task's own path. Confirmed against a real, already-registered task on
// this host (`\Microsoft\Windows\Time Synchronization\SynchronizeTime`),
// which carries a `<URI>` though nothing says its own shipped definition
// ever did. SameXML has to see past that or win_task.present given raw
// XML never converges (issue 608 item 5, lead L8).
//
// The two documents here differ only in the URI -- a real Create/Info
// round trip differs in more than that (element order, an added
// LogonType, a default Enabled left out), which SameXML is documented to
// treat as a real change and this is not testing.
func TestSameXMLIgnoresAnAddedURI(t *testing.T) {
	declared := `<?xml version="1.0" encoding="UTF-16"?>` +
		`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` +
		`<RegistrationInfo><Author>halite</Author></RegistrationInfo>` +
		`<Actions><Exec><Command>cmd.exe</Command></Exec></Actions></Task>`
	current := `<?xml version="1.0" encoding="UTF-16"?>` +
		`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">` +
		`<RegistrationInfo><Author>halite</Author><URI>\halite\x</URI></RegistrationInfo>` +
		`<Actions><Exec><Command>cmd.exe</Command></Exec></Actions></Task>`
	if !SameXML(declared, current) {
		t.Error("a document with no URI never matched one with an added URI and nothing else different")
	}
	// Still a real difference everywhere else.
	if SameXML(declared, strings.Replace(current, "cmd.exe", "other.exe", 1)) {
		t.Error("a real difference was hidden along with the URI")
	}
}

// schtasks writes non-ASCII text in the OEM code page once its output
// is redirected rather than going to a console, not UTF-8 -- confirmed
// on this host (OEM code page 437): 'é' comes back as the single byte
// 0x82. Fed straight to encoding/xml, as the unchanged code did, that
// byte is not valid UTF-8 on its own and the parse failed outright, so
// a task whose Description, Author or path held a non-ASCII character
// could not be read back at all (issue 608 item 5, lead L7).
//
// This assumes OEM code page 437, which is what an English-locale
// Windows -- including this host and GitHub's windows-2022 runners --
// uses; a host with a different OEM code page would encode 'é'
// differently and this fixture would need its own byte.
func TestANonASCIIDescriptionSurvivesTheOEMCodePage(t *testing.T) {
	raw := []byte("<Task><RegistrationInfo><Description>caf\x82</Description></RegistrationInfo>" +
		"<Actions><Exec><Command>cmd.exe</Command></Exec></Actions></Task>")
	parsed, err := parseXML(raw)
	if err != nil {
		t.Fatalf("a cp437-encoded description could not be read: %v", err)
	}
	got := toTask(`\x`, parsed, string(raw))
	if got.Description != "café" {
		t.Errorf("description = %q, want %q", got.Description, "café")
	}
}
