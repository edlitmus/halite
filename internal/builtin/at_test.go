package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// atqSampleFixture is built from the printf format string this project
// extracted with `strings -a /usr/bin/atq` on a real FreeBSD 15.1 host
// (package FreeBSD-at-15.1): `%s\t%-16s%c%s\t%ld`, preceded by the
// header string `Date\t\t\t\tOwner\t\tQueue\tJob#`. It was not captured
// from a live run -- this account is refused by `at` on that host, and
// root was not available while this module was written -- so the doc
// comment on atParseQueueList and the live test in live_at_test.go are
// where this is checked against the tool actually running.
const atqSampleFixture = "Date\t\t\t\tOwner\t\tQueue\tJob#\n" +
	"Sat Sep 12 16:05:00 2026\ted              a\t5\n" +
	"Sat Sep 12 17:00:00 2026\troot            b\t12\n"

func TestAtqParsesTheJobNumberAsTheLastColumn(t *testing.T) {
	jobs, err := atParseQueueList(atqSampleFixture)
	if err != nil {
		t.Fatalf("atParseQueueList: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("parsed %d jobs, want 2: %+v", len(jobs), jobs)
	}
	if jobs[0].Job != 5 {
		t.Errorf("first row's job number = %d, want 5 -- got it from the wrong column", jobs[0].Job)
	}
	if jobs[1].Job != 12 {
		t.Errorf("second row's job number = %d, want 12 -- got it from the wrong column", jobs[1].Job)
	}
}

func TestAtqSkipsTheHeaderRow(t *testing.T) {
	jobs, err := atParseQueueList(atqSampleFixture)
	if err != nil {
		t.Fatalf("atParseQueueList: %v", err)
	}
	for _, j := range jobs {
		if strings.Contains(j.Raw, "Job#") {
			t.Errorf("the header row was parsed as a job: %+v", j)
		}
	}
}

func TestAtqToleratesSpacesInsteadOfTabs(t *testing.T) {
	// Not every implementation is guaranteed to use exactly the tab
	// layout captured on this host; the job number is trusted because it
	// is the last whitespace-separated field, whatever separates it from
	// the rest.
	jobs, err := atParseQueueList("Date  Owner  Queue  Job#\nSat Sep 12 16:05:00 2026 ed a 7\n")
	if err != nil {
		t.Fatalf("atParseQueueList: %v", err)
	}
	if len(jobs) != 1 || jobs[0].Job != 7 {
		t.Errorf("jobs = %+v, want one job numbered 7", jobs)
	}
}

func TestAtqTreatsAnEmptyQueueAsNoJobsNotAnError(t *testing.T) {
	jobs, err := atParseQueueList("Date\t\t\t\tOwner\t\tQueue\tJob#\n")
	if err != nil {
		t.Fatalf("an empty queue was reported as an error: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("jobs = %+v, want none", jobs)
	}
}

func TestAtqRefusesARowWithNoTrailingNumber(t *testing.T) {
	_, err := atParseQueueList("Date\t\t\t\tOwner\t\tQueue\tJob#\nsomething went wrong here\n")
	if err == nil {
		t.Error("a row with no job number at the end was accepted")
	}
}

func TestAtParseScheduledJobNumberReadsTheConfirmation(t *testing.T) {
	job, ok := atParseScheduledJobNumber("Job 5 will be executed using /bin/sh\n")
	if !ok || job != 5 {
		t.Errorf("atParseScheduledJobNumber = %d, %v, want 5, true", job, ok)
	}
	if _, ok := atParseScheduledJobNumber("you do not have permission to use this program\n"); ok {
		t.Error("a permission refusal was read as a job confirmation")
	}
	if _, ok := atParseScheduledJobNumber(""); ok {
		t.Error("empty text was read as a job confirmation")
	}
}

func TestAtCheckPlatformKnowsOnlyLinuxAndFreeBSD(t *testing.T) {
	for _, goos := range []string{"linux", "freebsd"} {
		if err := atCheckPlatform(goos); err != nil {
			t.Errorf("atCheckPlatform(%q) = %v, want nil", goos, err)
		}
	}
	if err := atCheckPlatform("plan9"); err == nil {
		t.Error("an unknown platform was accepted")
	}
}

func TestAtScheduleArgvPassesTheTimeSpecAsSeparateWords(t *testing.T) {
	got, err := atScheduleArgv("freebsd", "", "now + 5 minutes")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"at", "now", "+", "5", "minutes"}
	if !equalStrings(got, want) {
		t.Errorf("atScheduleArgv = %v, want %v", got, want)
	}
}

func TestAtScheduleArgvAddsTheQueueFlag(t *testing.T) {
	got, err := atScheduleArgv("linux", "b", "teatime")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"at", "-q", "b", "teatime"}
	if !equalStrings(got, want) {
		t.Errorf("atScheduleArgv = %v, want %v", got, want)
	}
}

func TestAtScheduleArgvRefusesAnEmptyTimeSpec(t *testing.T) {
	if _, err := atScheduleArgv("linux", "", "   "); err == nil {
		t.Error("a blank time spec was accepted")
	}
}

func TestAtQueueListArgvIsAtDashLWithAnOptionalQueue(t *testing.T) {
	got, err := atQueueListArgv("freebsd", "")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(got, []string{"at", "-l"}) {
		t.Errorf("atQueueListArgv(no queue) = %v", got)
	}
	got, err = atQueueListArgv("linux", "c")
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(got, []string{"at", "-l", "-q", "c"}) {
		t.Errorf("atQueueListArgv(queue c) = %v", got)
	}
}

func TestAtRemoveArgvIsAtrmWithTheJobNumber(t *testing.T) {
	got, err := atRemoveArgv("freebsd", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(got, []string{"atrm", "42"}) {
		t.Errorf("atRemoveArgv = %v", got)
	}
}

func TestAtShowArgvIsAtDashC(t *testing.T) {
	got, err := atShowArgv("linux", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(got, []string{"at", "-c", "42"}) {
		t.Errorf("atShowArgv = %v", got)
	}
}

func TestAtScriptWithIdentifierPutsTheMarkerOnItsOwnFirstLine(t *testing.T) {
	script := atScriptWithIdentifier("backup-job", "/usr/local/bin/backup.sh")
	lines := strings.Split(script, "\n")
	if lines[0] != "#HALITE_AT_IDENTIFIER: backup-job" {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.Contains(script, "/usr/local/bin/backup.sh") {
		t.Errorf("the command is missing from the script: %q", script)
	}
}

// atTestCtx builds a Context whose commands are scripted and whose
// `Which` never touches the real PATH.
func atTestCtx(responses map[string]exec.Result) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
		Lookup: func(name string) string { return "/usr/bin/" + name },
	}
}

// stateSucceeded reads a states.Result the way the state compiler does:
// Result is a *bool because nil means "test mode, this would change
// something", so a plain success is a non-nil pointer to true.
func stateSucceeded(r states.Result) bool {
	return r.Result != nil && *r.Result
}

func TestAtFindByIdentifierMatchesTheMarkerLine(t *testing.T) {
	listKey := (exec.Command{Argv: []string{"at", "-l"}}).String()
	showKey := (exec.Command{Argv: []string{"at", "-c", "5"}}).String()
	c := atTestCtx(map[string]exec.Result{
		listKey: {Stdout: atqSampleFixture},
		showKey: {Stdout: "#!/bin/sh\n#HALITE_AT_IDENTIFIER: nightly-backup\ncd /\nbackup.sh\n"},
	})
	job, found, err := atFindByIdentifier(c, "nightly-backup")
	if err != nil {
		t.Fatalf("atFindByIdentifier: %v", err)
	}
	if !found || job != 5 {
		t.Errorf("atFindByIdentifier = %d, %v, want 5, true", job, found)
	}
}

func TestAtFindByIdentifierReportsNotFoundRatherThanError(t *testing.T) {
	listKey := (exec.Command{Argv: []string{"at", "-l"}}).String()
	c := atTestCtx(map[string]exec.Result{listKey: {Stdout: atqSampleFixture}})
	_, found, err := atFindByIdentifier(c, "no-such-identifier")
	if err != nil {
		t.Fatalf("atFindByIdentifier: %v", err)
	}
	if found {
		t.Error("an identifier that matches no job's script was reported as found")
	}
}

func TestAtPresentSkipsWhenAlreadyQueued(t *testing.T) {
	listKey := (exec.Command{Argv: []string{"at", "-l"}}).String()
	showKey := (exec.Command{Argv: []string{"at", "-c", "5"}}).String()
	c := atTestCtx(map[string]exec.Result{
		listKey: {Stdout: atqSampleFixture},
		showKey: {Stdout: "#HALITE_AT_IDENTIFIER: nightly-backup\nbackup.sh\n"},
	})
	result, err := atPresent(c, value.MapOf("name", "backup.sh", "timespec", "now + 1 hour", "identifier", "nightly-backup"))
	if err != nil {
		t.Fatalf("atPresent: %v", err)
	}
	if !stateSucceeded(result) || result.Changes.Len() != 0 {
		t.Errorf("atPresent on an already-queued job = %+v, want true with no changes", result)
	}
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(ran, "at -q") || ran == "at" || strings.Contains(ran, "now") {
			t.Errorf("atPresent scheduled a new job when one already matched: %q", ran)
		}
	}
}

func TestAtPresentSchedulesANewJobAndReportsItsNumber(t *testing.T) {
	listKey := (exec.Command{Argv: []string{"at", "-l"}}).String()
	scheduleArgv, err := atScheduleArgv(runtime.GOOS, "", "now + 1 hour")
	if err != nil {
		t.Fatalf("this build's atScheduleArgv does not know %s: %v", runtime.GOOS, err)
	}
	scheduleKey := (exec.Command{Argv: scheduleArgv}).String()
	c := atTestCtx(map[string]exec.Result{
		listKey:     {Stdout: "Date\t\t\t\tOwner\t\tQueue\tJob#\n"},
		scheduleKey: {Stderr: "Job 9 will be executed using /bin/sh\n"},
	})
	result, err := atPresent(c, value.MapOf("name", "backup.sh", "timespec", "now + 1 hour"))
	if err != nil {
		t.Fatalf("atPresent: %v", err)
	}
	if !stateSucceeded(result) || result.Changes.Len() == 0 {
		t.Errorf("scheduling a new job = %+v, want true with changes recorded", result)
	}
	if !strings.Contains(result.Comment, "9") {
		t.Errorf("the comment does not name the job number it scheduled: %q", result.Comment)
	}
}

func TestAtPresentRejectsAnEmptyTimespec(t *testing.T) {
	c := atTestCtx(nil)
	result, err := atPresent(c, value.MapOf("name", "backup.sh", "timespec", ""))
	if err != nil {
		t.Fatalf("atPresent: %v", err)
	}
	if stateSucceeded(result) {
		t.Error("an empty time spec was accepted as success")
	}
}

func TestAtAbsentRemovesTheMatchingJob(t *testing.T) {
	listKey := (exec.Command{Argv: []string{"at", "-l"}}).String()
	showKey := (exec.Command{Argv: []string{"at", "-c", "5"}}).String()
	rmKey := (exec.Command{Argv: []string{"atrm", "5"}}).String()
	c := atTestCtx(map[string]exec.Result{
		listKey: {Stdout: atqSampleFixture},
		showKey: {Stdout: "#HALITE_AT_IDENTIFIER: nightly-backup\nbackup.sh\n"},
		rmKey:   {},
	})
	result, err := atAbsent(c, value.MapOf("name", "backup.sh", "identifier", "nightly-backup"))
	if err != nil {
		t.Fatalf("atAbsent: %v", err)
	}
	if !stateSucceeded(result) || result.Changes.Len() == 0 {
		t.Errorf("removing a matched job = %+v, want true with changes recorded", result)
	}
	found := false
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if ran == rmKey {
			found = true
		}
	}
	if !found {
		t.Errorf("atAbsent did not run %q", rmKey)
	}
}

func TestAtAbsentIsTrueWhenNothingMatches(t *testing.T) {
	listKey := (exec.Command{Argv: []string{"at", "-l"}}).String()
	c := atTestCtx(map[string]exec.Result{listKey: {Stdout: atqSampleFixture}})
	result, err := atAbsent(c, value.MapOf("name", "no-such-job"))
	if err != nil {
		t.Fatalf("atAbsent: %v", err)
	}
	if !stateSucceeded(result) || result.Changes.Len() != 0 {
		t.Errorf("atAbsent on nothing queued = %+v, want true with no changes", result)
	}
}

func TestAtAtrmSkipsAJobThatIsNotQueued(t *testing.T) {
	listKey := (exec.Command{Argv: []string{"at", "-l"}}).String()
	c := atTestCtx(map[string]exec.Result{listKey: {Stdout: atqSampleFixture}})
	out, err := atAtrmFn(c, value.MapOf("job", int64(999)))
	if err != nil {
		t.Fatalf("atAtrmFn: %v", err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("removing a job that is not queued reported a change: %v", out)
	}
	rmKey := (exec.Command{Argv: []string{"atrm", "999"}}).String()
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if ran == rmKey {
			t.Error("atrm was run against a job that was never queued")
		}
	}
}

func TestAtAtRejectsAnEmptyCommand(t *testing.T) {
	c := atTestCtx(nil)
	if _, err := atAtFn(c, value.MapOf("timespec", "now")); err == nil {
		t.Error("an empty command was accepted")
	}
}

func TestAtAtInTestModeSchedulesNothing(t *testing.T) {
	c := atTestCtx(nil)
	c.Test = true
	out, err := atAtFn(c, value.MapOf("timespec", "now + 1 hour", "cmd", "backup.sh"))
	if err != nil {
		t.Fatalf("atAtFn: %v", err)
	}
	comment, _ := out.(*value.Map).GetString("comment")
	if !strings.Contains(comment.(string), "Nothing was changed: this was a test run.") {
		t.Errorf("a test run's comment does not say it changed nothing: %v", comment)
	}
	if len(c.Runner.(*exec.RecordingRunner).RanCommands()) != 0 {
		t.Errorf("a test run executed commands: %v", c.Runner.(*exec.RecordingRunner).RanCommands())
	}
}

func TestAtRefusesWithoutTheTool(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if _, err := atAtFn(c, value.MapOf("timespec", "now", "cmd", "true")); err == nil || !strings.Contains(err.Error(), "at") {
		t.Errorf("atAtFn without at on the node = %v, want an error naming at", err)
	}
}

func TestAtRefusesOnAPlatformItIsNotDeclaredFor(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "freebsd" {
		t.Skip("this is about the platforms at is not declared for")
	}
	_, err := New().Exec.Call(&exec.Context{}, "at.atq", value.NewMap(0))
	if err == nil || !strings.Contains(err.Error(), "this node is "+runtime.GOOS) {
		t.Errorf("at.atq did not refuse by platform: %v", err)
	}
}
