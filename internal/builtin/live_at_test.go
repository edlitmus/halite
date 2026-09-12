package builtin

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `at`, driven for real against the real `at`/`atq`/`atrm`.
//
// # Why this exists
//
// Everything in at.go about `atq`'s row format was derived from the
// format string embedded in the compiled `/usr/bin/atq` binary
// (`strings -a`), not from a captured run: this account is refused by
// `at` on the host this was written on -- "you do not have permission
// to use this program", because neither /etc/at.allow nor
// /etc/at.deny exists and this implementation's default with both
// absent is superuser-only. This is the leg that runs the derivation
// against the tool itself rather than against its strings table, the
// way `live_quota_ufs_test.go` is the leg that runs `quotaSetArgv`'s
// BSD branch for the first time.
//
// # It is gated, and the job never fires
//
// `HALITE_SYSTEM_LIVE=1` and root, the same gate as this project's other
// `_test.go` legs that mutate real system state. The job is queued far
// enough out ("now + 1 hour") that it cannot run before this test
// removes it again, and the cleanup removes it whether or not the test
// that queued it passed.
func liveAtSetup(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this queue and remove a real at job")
	}
	if os.Geteuid() != 0 {
		t.Skip("this host refuses `at` to anything but root, and queuing/removing a job both need it")
	}
	c := &exec.Context{}
	for _, tool := range []string{"at", "atq", "atrm"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`", tool)
		}
	}
	return c
}

// liveAtRemoveIfQueued is the cleanup every test here registers: remove
// the job this test queued, tolerating it already being gone (it may
// have been removed by the assertions the test itself makes).
func liveAtRemoveIfQueued(t *testing.T, c *exec.Context, job int64) {
	t.Helper()
	jobs, err := atListJobs(c, "")
	if err != nil {
		t.Logf("cleanup: the queue could not be read to check for job %d: %v", job, err)
		return
	}
	if !atJobQueued(jobs, job) {
		return
	}
	if _, err := c.Run(exec.Command{Argv: []string{"atrm", strconv.FormatInt(job, 10)}, IgnoreExitCode: true}); err != nil {
		t.Logf("cleanup: job %d could not be removed, which leaves it queued: %v", job, err)
	}
}

// TestLiveAtSchedulesListsAndRemovesARealJob runs `at.at`, `at.atq`,
// `at.atc` and `at.atrm` against the real tools and checks each one
// against a fresh, independent read rather than trusting the module's
// own answer for the step before it.
func TestLiveAtSchedulesListsAndRemovesARealJob(t *testing.T) {
	c := liveAtSetup(t)
	r := New()

	const marker = "echo halite-live-at-test"
	on := value.MapOf("timespec", "now + 1 hour", "cmd", marker)
	out, err := r.Exec.Call(c, "at.at", on)
	if err != nil {
		t.Fatalf("at.at through the real at: %v", err)
	}
	m := out.(*value.Map)
	jobAny, ok := m.GetString("job")
	if !ok {
		t.Fatalf("at.at did not report a job number: %v", out)
	}
	job := jobAny.(int64)
	t.Cleanup(func() { liveAtRemoveIfQueued(t, c, job) })

	// Independent read #1: atq, parsed by this module's own reader,
	// lists the job this module says it just queued.
	listed, err := r.Exec.Call(c, "at.atq", value.NewMap(0))
	if err != nil {
		t.Fatalf("at.atq: %v", err)
	}
	found := false
	for _, entry := range listed.([]any) {
		if j, _ := entry.(*value.Map).GetString("job"); j == job {
			found = true
		}
	}
	if !found {
		t.Fatalf("at.at reported job %d but at.atq does not list it: %v", job, listed)
	}

	// Independent read #2: the job's own script, read back through
	// `at -c`, contains the command this test asked to run.
	script, err := r.Exec.Call(c, "at.atc", value.MapOf("job", job))
	if err != nil {
		t.Fatalf("at.atc: %v", err)
	}
	if !strings.Contains(script.(string), marker) {
		t.Fatalf("job %d's script does not contain the command it was given: %q", job, script)
	}

	// Removal, checked against atq again rather than against atrm's own
	// silence.
	rm, err := r.Exec.Call(c, "at.atrm", value.MapOf("job", job))
	if err != nil {
		t.Fatalf("at.atrm: %v", err)
	}
	if changed, _ := rm.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("removing a job that was queued reported no change: %v", rm)
	}
	listed, err = r.Exec.Call(c, "at.atq", value.NewMap(0))
	if err != nil {
		t.Fatalf("at.atq after removal: %v", err)
	}
	for _, entry := range listed.([]any) {
		if j, _ := entry.(*value.Map).GetString("job"); j == job {
			t.Fatalf("job %d was reported removed but atq still lists it: %v", job, listed)
		}
	}

	// Idempotence: removing it again changes nothing, because it is
	// already gone.
	again, err := r.Exec.Call(c, "at.atrm", value.MapOf("job", job))
	if err != nil {
		t.Fatalf("the second at.atrm: %v", err)
	}
	if changed, _ := again.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("removing an already-removed job reported a change: %v", again)
	}
}

// TestLiveAtPresentAndAbsentRoundTripAnIdentifiedJob proves the marker
// trick borrowed from cron.go actually survives a real `at -c`: that a
// job written with `#HALITE_AT_IDENTIFIER: ...` as its first line is
// found again by `at.present` on a second run (no duplicate job), and
// removed by `at.absent`.
func TestLiveAtPresentAndAbsentRoundTripAnIdentifiedJob(t *testing.T) {
	c := liveAtSetup(t)
	r := New()

	const identifier = "halite-live-at-present-test"
	present := value.MapOf("name", "echo halite-live-at-present", "timespec", "now + 1 hour", "identifier", identifier)

	_, err := r.States.Call(c, "at.present", present)
	if err != nil {
		t.Fatalf("at.present: %v", err)
	}
	job, found, err := atFindByIdentifier(c, identifier)
	if err != nil {
		t.Fatalf("atFindByIdentifier after at.present: %v", err)
	}
	if !found {
		t.Fatal("at.present reported success but no queued job carries its identifier")
	}
	t.Cleanup(func() { liveAtRemoveIfQueued(t, c, job) })

	// Running it again must not queue a second job: this is what makes
	// the state usable from a tree that runs nightly.
	_, err = r.States.Call(c, "at.present", present)
	if err != nil {
		t.Fatalf("the second at.present: %v", err)
	}
	jobs, err := atListJobs(c, "")
	if err != nil {
		t.Fatalf("atListJobs: %v", err)
	}
	matches := 0
	for _, j := range jobs {
		script, err := atShowScript(c, j.Job)
		if err != nil {
			continue
		}
		if strings.Contains(script, "#HALITE_AT_IDENTIFIER: "+identifier) {
			matches++
		}
	}
	if matches != 1 {
		t.Errorf("running at.present twice left %d jobs carrying %q, want 1", matches, identifier)
	}

	absent := value.MapOf("identifier", identifier)
	_, err = r.States.Call(c, "at.absent", absent)
	if err != nil {
		t.Fatalf("at.absent: %v", err)
	}
	_, found, err = atFindByIdentifier(c, identifier)
	if err != nil {
		t.Fatalf("atFindByIdentifier after at.absent: %v", err)
	}
	if found {
		t.Error("at.absent reported success but a job still carries its identifier")
	}
}
