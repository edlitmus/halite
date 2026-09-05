package builtin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Five functions a real estate's tree reached for, one migration report
// line each.
func TestTheSmallGapsAreRegistered(t *testing.T) {
	r := New()
	for _, name := range []string{"grains.set", "file.get_user", "file.rename"} {
		if _, ok := r.Exec.Signatures().Lookup(name); !ok {
			t.Errorf("%s is not an execution function", name)
		}
	}
	for _, name := range []string{"event.send", "schedule.absent"} {
		if _, ok := r.States.Signatures().Lookup(name); !ok {
			t.Errorf("%s is not a state function", name)
		}
	}
	// event.send is both in Salt, and was an execution function here
	// already. Adding the state must not have taken the other away.
	if _, ok := r.Exec.Signatures().Lookup("event.send"); !ok {
		t.Error("event.send is no longer an execution function")
	}
}

func TestFileRenameMovesAPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	dst := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(src, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := fileRename(&exec.Context{}, value.MapOf("src", src, "dst", dst)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(src); !os.IsNotExist(err) {
		t.Error("the source is still there")
	}
	if _, err := os.Lstat(dst); err != nil {
		t.Errorf("the destination is missing: %v", err)
	}
}

func TestFileRenameInTestModeMovesNothing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(src, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fileRename(&exec.Context{Test: true},
		value.MapOf("src", src, "dst", filepath.Join(dir, "b.txt"))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(src); err != nil {
		t.Error("test mode moved the file")
	}
}

func TestFileGetUserNamesTheOwner(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := fileGetUser(&exec.Context{}, value.MapOf("path", path))
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := got.(string); s == "" {
		t.Errorf("got %v, want an owner name or a numeric id", got)
	}
}

// An event is a thing that happened, not a condition to hold, so there
// is nothing for a second run to find already true. It reports a change
// every time, which is what Salt's state does.
func TestEventSendAlwaysReportsAChange(t *testing.T) {
	sent := 0
	c := &exec.Context{Events: eventSenderFunc(func(tag string, data map[string]any) error {
		sent++
		return nil
	})}

	for i := 0; i < 2; i++ {
		res, err := eventSendState(c, value.MapOf("name", "app/deployed"))
		if err != nil {
			t.Fatal(err)
		}
		if !res.Succeeded() || !res.HasChanges() {
			t.Fatalf("run %d did not report a change: %+v", i, res)
		}
	}
	if sent != 2 {
		t.Errorf("the event was sent %d times, want 2", sent)
	}
}

// A one-shot command line has no hub, and the event would go nowhere.
// Saying so beats reporting a success nobody received.
func TestEventSendWithNoHubSaysSo(t *testing.T) {
	res, err := eventSendState(&exec.Context{}, value.MapOf("name", "app/deployed"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Error("sending an event with no hub should fail")
	}
}

func TestScheduleAbsentConvergesWhenTheJobIsGone(t *testing.T) {
	c := newCtx(false)
	c.Schedule = emptySchedule{}
	res, err := New().States.Call(c, "schedule.absent", value.MapOf("name", "nightly"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || res.HasChanges() {
		t.Errorf("removing a job that is not there should converge: %+v", res)
	}
}

// eventSenderFunc is an exec.EventSender that records what it was given.
type eventSenderFunc func(tag string, data map[string]any) error

func (f eventSenderFunc) Send(tag string, data map[string]any) error { return f(tag, data) }

// emptySchedule is a ScheduleControl with no jobs, for the converged
// case: the only method that matters here is List.
type emptySchedule struct{}

func (emptySchedule) List() *value.Map                     { return value.NewMap(0) }
func (emptySchedule) Add(string, *value.Map) error         { return nil }
func (emptySchedule) Modify(string, *value.Map) error      { return nil }
func (emptySchedule) Delete(string) error                  { return nil }
func (emptySchedule) SetEnabled(string, bool) error        { return nil }
func (emptySchedule) RunJob(context.Context, string) error { return nil }
func (emptySchedule) Snapshot() *value.Map                 { return value.NewMap(0) }
func (emptySchedule) NextFireTime(string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
