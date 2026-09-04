package builtin

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// fakeWatcher stands in for a running beacon or schedule engine.
//
// It holds a set the way both real engines do and records what was
// saved, which is the half these states add over calling the execution
// module by hand: a declaration the node forgets on restart is not a
// declaration.
type fakeWatcher struct {
	set   *value.Map
	saved int
}

func newFakeWatcher() *fakeWatcher { return &fakeWatcher{set: value.NewMap(4)} }

func (f *fakeWatcher) Add(name string, config *value.Map) error {
	f.set.Set(name, config)
	return nil
}

func (f *fakeWatcher) Modify(name string, config *value.Map) error {
	f.set.Set(name, config)
	return nil
}

func (f *fakeWatcher) Delete(name string) error {
	next := value.NewMap(f.set.Len())
	for _, e := range f.set.Entries() {
		if value.KeyString(e.Key) != name {
			next.Set(value.KeyString(e.Key), e.Val)
		}
	}
	f.set = next
	return nil
}

func (f *fakeWatcher) Snapshot() *value.Map { return f.set }
func (f *fakeWatcher) List() *value.Map     { return f.set }

func (f *fakeWatcher) SetEnabled(string, bool) error        { return nil }
func (f *fakeWatcher) Reset() error                         { return nil }
func (f *fakeWatcher) RunJob(context.Context, string) error { return nil }
func (f *fakeWatcher) NextFireTime(string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

// watcherCtx builds a context running both engines, with a SaveConfig
// that counts.
func watcherCtx(t *testing.T, test bool) (*exec.Context, *fakeWatcher) {
	t.Helper()
	f := newFakeWatcher()
	c := newCtx(test)
	c.Beacons = f
	c.Schedule = f
	c.SaveConfig = func(kind string, config *value.Map) (string, error) {
		f.saved++
		return "/etc/halite/" + kind + ".d/halite.yaml", nil
	}
	return c, f
}

// Both states are the same state twice, so both are exercised the same
// way rather than one being taken on trust.
var watcherCases = []struct {
	module  string
	dataArg string
	config  *value.Map
	changed *value.Map
}{
	{
		module: "beacon", dataArg: "beacon_data",
		config: value.MapOf("files", value.MapOf("/etc/nginx/nginx.conf",
			value.MapOf("mask", []any{"modify"})), "interval", int64(5)),
		changed: value.MapOf("files", value.MapOf("/etc/nginx/nginx.conf",
			value.MapOf("mask", []any{"modify", "delete"})), "interval", int64(5)),
	},
	{
		module: "schedule", dataArg: "job",
		config:  value.MapOf("function", "state.apply", "seconds", int64(3600)),
		changed: value.MapOf("function", "state.apply", "seconds", int64(900)),
	},
}

// A declaration adds what is missing, then converges: the second run
// reports nothing.
func TestAWatcherStateAddsAndThenConverges(t *testing.T) {
	for _, tc := range watcherCases {
		t.Run(tc.module, func(t *testing.T) {
			r := New()
			c, f := watcherCtx(t, false)
			args := value.MapOf("name", "nightly", tc.dataArg, tc.config)

			first, err := r.States.Call(c, tc.module+".present", args)
			if err != nil {
				t.Fatal(err)
			}
			if !first.Succeeded() {
				t.Fatalf("the state failed: %+v", first)
			}
			if !first.HasChanges() {
				t.Error("adding one reported no change")
			}
			if _, held := f.set.Get("nightly"); !held {
				t.Fatal("nothing was added to the engine")
			}
			if f.saved != 1 {
				t.Errorf("saved %d times; a declaration the node forgets on restart is not one", f.saved)
			}

			second, err := r.States.Call(c, tc.module+".present", args)
			if err != nil {
				t.Fatal(err)
			}
			if second.HasChanges() {
				t.Errorf("the second run reported changes: %+v", second.Changes)
			}
			if !strings.Contains(second.Comment, "already") {
				t.Errorf("comment = %q", second.Comment)
			}
			if f.saved != 1 {
				t.Errorf("a run that changed nothing wrote the configuration anyway")
			}
		})
	}
}

// A declaration that differs modifies rather than adding a second entry.
func TestAWatcherStateModifiesWhatIsAlreadyThere(t *testing.T) {
	for _, tc := range watcherCases {
		t.Run(tc.module, func(t *testing.T) {
			r := New()
			c, f := watcherCtx(t, false)

			if _, err := r.States.Call(c, tc.module+".present",
				value.MapOf("name", "nightly", tc.dataArg, tc.config)); err != nil {
				t.Fatal(err)
			}
			res, err := r.States.Call(c, tc.module+".present",
				value.MapOf("name", "nightly", tc.dataArg, tc.changed))
			if err != nil {
				t.Fatal(err)
			}
			if !res.HasChanges() {
				t.Fatal("a different configuration reported no change")
			}
			if !strings.Contains(res.Comment, "changed") {
				t.Errorf("comment = %q; it should say it was changed, not added", res.Comment)
			}
			if f.set.Len() != 1 {
				t.Errorf("the engine holds %d entries; a change must not add a second", f.set.Len())
			}
		})
	}
}

// The Salt form writes the configuration's keys directly on the state,
// and that has to reach the engine as the configuration.
//
// The state's own arguments are not part of it: a beacon configured with
// `save: true` because the state took one would be a beacon carrying a
// key the state meant for itself.
func TestAWatcherStateTakesTheConfigurationAsLooseKeys(t *testing.T) {
	r := New()
	c, f := watcherCtx(t, false)

	res, err := r.States.Call(c, "beacon.present", value.MapOf(
		"name", "diskusage",
		"/", "85%",
		"interval", int64(60),
		"save", true,
	))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("the state failed: %+v", res)
	}
	held, _ := f.set.Get("diskusage")
	config, ok := held.(*value.Map)
	if !ok {
		t.Fatalf("the engine holds %#v", held)
	}
	if v, _ := config.Get("/"); v != "85%" {
		t.Errorf("the mount point did not reach the engine: %v", config)
	}
	if v, _ := config.Get("interval"); v != int64(60) {
		t.Errorf("the interval did not reach the engine: %v", config)
	}
	for _, own := range []string{"name", "save"} {
		if _, leaked := config.Get(own); leaked {
			t.Errorf("the state's own %q argument reached the beacon's configuration: %v", own, config)
		}
	}
}

// Only the keys the declaration stated are compared. An engine fills in
// defaults, and comparing those against a declaration that never
// mentioned them would report a change on every run.
func TestOnlyTheDeclaredKeysAreCompared(t *testing.T) {
	r := New()
	c, f := watcherCtx(t, false)

	// What the engine holds carries more than the declaration will.
	f.set.Set("nightly", value.MapOf(
		"function", "state.apply", "seconds", int64(3600),
		"maxrunning", int64(1), "enabled", true))

	res, err := r.States.Call(c, "schedule.present", value.MapOf(
		"name", "nightly", "job", value.MapOf("function", "state.apply", "seconds", int64(3600))))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("a partial declaration reported a change it did not ask about: %+v", res.Changes)
	}
}

// Removing converges too, and persists, which the previous
// `schedule.absent` did not: a job removed and not written came back on
// the next restart.
func TestAWatcherStateRemovesAndThenConverges(t *testing.T) {
	for _, tc := range watcherCases {
		t.Run(tc.module, func(t *testing.T) {
			r := New()
			c, f := watcherCtx(t, false)
			f.set.Set("nightly", tc.config)

			first, err := r.States.Call(c, tc.module+".absent", value.MapOf("name", "nightly"))
			if err != nil {
				t.Fatal(err)
			}
			if !first.HasChanges() {
				t.Error("removing one reported no change")
			}
			if f.set.Len() != 0 {
				t.Errorf("the engine still holds %d entries", f.set.Len())
			}
			if f.saved != 1 {
				t.Errorf("the removal was not written, so it comes back on restart")
			}

			second, err := r.States.Call(c, tc.module+".absent", value.MapOf("name", "nightly"))
			if err != nil {
				t.Fatal(err)
			}
			if second.HasChanges() {
				t.Errorf("removing what is already gone reported changes: %+v", second.Changes)
			}
			if f.saved != 1 {
				t.Error("a run that changed nothing wrote the configuration anyway")
			}
		})
	}
}

// Test mode predicts and changes nothing, including writing nothing.
func TestAWatcherStateInTestModeChangesNothing(t *testing.T) {
	for _, tc := range watcherCases {
		t.Run(tc.module, func(t *testing.T) {
			r := New()
			c, f := watcherCtx(t, true)

			res, err := r.States.Call(c, tc.module+".present",
				value.MapOf("name", "nightly", tc.dataArg, tc.config))
			if err != nil {
				t.Fatal(err)
			}
			if res.Result != nil {
				t.Errorf("test mode should predict, not decide: %+v", res)
			}
			if !res.HasChanges() {
				t.Error("test mode predicted no change for something that is not there")
			}
			if f.set.Len() != 0 {
				t.Error("test mode changed the engine")
			}
			if f.saved != 0 {
				t.Error("test mode wrote the configuration")
			}
		})
	}
}

// `save: False` is for a declaration meant to last only as long as this
// boot, and it has to actually not write.
func TestSaveFalseDoesNotWrite(t *testing.T) {
	r := New()
	c, f := watcherCtx(t, false)

	res, err := r.States.Call(c, "beacon.present", value.MapOf(
		"name", "inotify", "beacon_data", value.MapOf("interval", int64(5)), "save", false))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatal("nothing changed")
	}
	if f.saved != 0 {
		t.Error("save: False wrote the configuration anyway")
	}
	if _, held := f.set.Get("inotify"); !held {
		t.Error("save: False also skipped the change itself")
	}
}

// A node that is not running these is told so, and the state fails
// rather than reporting a success that changed nothing.
//
// A highstate passing over a node that is not watching anything is worse
// than one that fails: the second gets looked at.
func TestAWatcherStateOnANodeThatRunsNoneFails(t *testing.T) {
	r := New()
	for _, tc := range watcherCases {
		for _, fn := range []string{".present", ".absent"} {
			// `absent` takes no configuration, so it is not given one.
			args := value.MapOf("name", "nightly")
			if fn == ".present" {
				args.Set(tc.dataArg, tc.config)
			}
			res, err := r.States.Call(newCtx(false), tc.module+fn, args)
			if err != nil {
				t.Fatal(err)
			}
			if res.Succeeded() {
				t.Errorf("%s%s succeeded on a node running none", tc.module, fn)
			}
			if !strings.Contains(res.Comment, "node configuration") {
				t.Errorf("%s%s does not say where they are started: %q", tc.module, fn, res.Comment)
			}
		}
	}
}

// A declaration with no configuration at all is refused, naming both
// spellings, rather than registering something empty.
func TestAWatcherStateNeedsAConfiguration(t *testing.T) {
	r := New()
	c, _ := watcherCtx(t, false)
	res, err := r.States.Call(c, "beacon.present", value.MapOf("name", "inotify"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatal("a beacon with no configuration was registered")
	}
	if !strings.Contains(res.Comment, "beacon_data") {
		t.Errorf("the refusal does not name the explicit spelling: %q", res.Comment)
	}
}

// A node with nowhere to write says so rather than reporting a clean
// success — the change happened, and the warning is what carries the
// part that did not.
func TestAWatcherStateWarnsWhenItCannotWrite(t *testing.T) {
	r := New()
	f := newFakeWatcher()
	c := newCtx(false)
	c.Beacons = f
	// No SaveConfig: a one-shot command line has nowhere to write.

	res, err := r.States.Call(c, "beacon.present",
		value.MapOf("name", "inotify", "beacon_data", value.MapOf("interval", int64(5))))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("the change was made, so the state should not fail: %+v", res)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("nothing warned that the change is lost on restart")
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "restart") {
		t.Errorf("the warning does not say what is lost: %v", res.Warnings)
	}
}
