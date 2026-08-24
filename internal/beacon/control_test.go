package beacon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// controlEngine is an engine over one real beacon name, so that the
// registry check has something to accept.
func controlEngine(t *testing.T) (*Engine, *recorder) {
	t.Helper()
	reg := NewRegistry()
	reg.Add(
		Module{Name: "diskusage", Doc: "a beacon for a test", Fn: func(*exec.Context, *Instance) ([]Event, error) {
			return []Event{{Suffix: "var", Data: map[string]any{"n": 1}}}, nil
		}},
		Module{Name: "load", Doc: "another", Fn: func(*exec.Context, *Instance) ([]Event, error) {
			return nil, nil
		}},
		Module{Name: "inotify", Doc: "not built", Pending: "the phase that admits x/sys"},
	)
	rec := newRecorder()
	return &Engine{
		Registry:  reg,
		Instances: []*Instance{},
		Tick:      5 * time.Millisecond,
		Context:   func() *exec.Context { return &exec.Context{Ctx: context.Background()} },
		Send:      rec.Send,
	}, rec
}

// A beacon configuration that can only be changed by restarting the
// node is one nobody changes during an incident, which is when the
// reason to change it usually arrives.
func TestABeaconCanBeAddedToARunningNode(t *testing.T) {
	e, rec := controlEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// Nothing configured, so nothing fires.
	time.Sleep(50 * time.Millisecond)
	if n := rec.count(); n != 0 {
		t.Fatalf("%d events from an empty engine", n)
	}

	config := value.MapOf("beacon_data", []any{
		value.MapOf("/", "1%"),
		value.MapOf("interval", int64(1)),
		value.MapOf("coalesce_window", int64(0)),
	})
	if err := e.Add("diskusage", config); err != nil {
		t.Fatal(err)
	}
	waitFor(t, rec, 1, 3*time.Second)

	// And it is in the listing, with what it was set to.
	listed := e.List()
	entry, ok := listed.Get("diskusage")
	if !ok {
		t.Fatalf("the listing is %v", listed.StringKeys())
	}
	m, _ := entry.(*value.Map)
	if enabled, _ := m.Get("enabled"); enabled != true {
		t.Errorf("the added beacon is %v", entry)
	}
	if cfg, _ := m.Get("config"); cfg == nil {
		t.Error("the listing does not say what the beacon was set to")
	}
}

// A name this build does not have, or one declared and not built, is
// refused at the point of asking rather than by going quiet.
func TestAddingAnUnknownBeaconIsRefused(t *testing.T) {
	e, _ := controlEngine(t)
	if err := e.Add("nosuchbeacon", value.NewMap(0)); err == nil {
		t.Error("an unknown beacon was accepted")
	}
	err := e.Add("inotify", value.NewMap(0))
	if err == nil {
		t.Fatal("a declared-and-unbuilt beacon was accepted")
	}
	if !strings.Contains(err.Error(), "not built") {
		t.Errorf("the refusal says %q", err)
	}
	if err := e.Add("", value.NewMap(0)); err == nil {
		t.Error("a beacon with no name was accepted")
	}
}

// Adding twice is a mistake worth naming: an operator who meant to
// change one would otherwise get two.
func TestAddingTheSameBeaconTwiceIsRefused(t *testing.T) {
	e, _ := controlEngine(t)
	if err := e.Add("load", value.NewMap(0)); err != nil {
		t.Fatal(err)
	}
	err := e.Add("load", value.NewMap(0))
	if err == nil {
		t.Fatal("the same beacon was added twice")
	}
	if !strings.Contains(err.Error(), "modify") {
		t.Errorf("the refusal should name what to use instead: %q", err)
	}
}

// Disabling holds a beacon without forgetting it, which is what makes
// it safe to reach for during an incident.
func TestDisablingHoldsWithoutForgetting(t *testing.T) {
	e, rec := controlEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	config := value.MapOf("beacon_data", []any{
		value.MapOf("/", "1%"),
		value.MapOf("interval", int64(1)),
		value.MapOf("coalesce_window", int64(0)),
	})
	if err := e.Add("diskusage", config); err != nil {
		t.Fatal(err)
	}
	waitFor(t, rec, 1, 3*time.Second)

	if err := e.SetEnabled("diskusage", false); err != nil {
		t.Fatal(err)
	}
	settled := rec.count()
	time.Sleep(80 * time.Millisecond)
	if rec.count() != settled {
		t.Error("a disabled beacon kept firing")
	}
	// Still there, and it says it is off.
	entry, ok := e.List().Get("diskusage")
	if !ok {
		t.Fatal("a disabled beacon was forgotten")
	}
	if enabled, _ := entry.(*value.Map).Get("enabled"); enabled != false {
		t.Errorf("a disabled beacon reports %v", enabled)
	}

	// And turning it back on restores exactly what was there.
	if err := e.SetEnabled("diskusage", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, rec, settled+1, 3*time.Second)
}

// `beacons.disable` with no name holds every beacon at once.
func TestDisablingAllHoldsEveryBeacon(t *testing.T) {
	e, rec := controlEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	config := value.MapOf("beacon_data", []any{
		value.MapOf("/", "1%"),
		value.MapOf("interval", int64(1)),
		value.MapOf("coalesce_window", int64(0)),
	})
	if err := e.Add("diskusage", config); err != nil {
		t.Fatal(err)
	}
	waitFor(t, rec, 1, 3*time.Second)

	if err := e.SetEnabled("", false); err != nil {
		t.Fatal(err)
	}
	settled := rec.count()
	time.Sleep(80 * time.Millisecond)
	if rec.count() != settled {
		t.Error("a paused engine kept firing")
	}
	if err := e.SetEnabled("", true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, rec, settled+1, 3*time.Second)
}

// Modifying a beacon that was turned off must not turn it back on: an
// operator fixing a threshold during an incident did not ask for that.
func TestModifyingKeepsADisabledBeaconDisabled(t *testing.T) {
	e, _ := controlEngine(t)
	if err := e.Add("load", value.NewMap(0)); err != nil {
		t.Fatal(err)
	}
	if err := e.SetEnabled("load", false); err != nil {
		t.Fatal(err)
	}
	if err := e.Modify("load", value.MapOf("beacon_data", []any{
		value.MapOf("interval", int64(30)),
	})); err != nil {
		t.Fatal(err)
	}
	entry, _ := e.List().Get("load")
	if enabled, _ := entry.(*value.Map).Get("enabled"); enabled != false {
		t.Error("modifying a disabled beacon turned it back on")
	}

	// Unless the change says so.
	if err := e.Modify("load", value.MapOf("beacon_data", []any{
		value.MapOf("enabled", true),
	})); err != nil {
		t.Fatal(err)
	}
	entry, _ = e.List().Get("load")
	if enabled, _ := entry.(*value.Map).Get("enabled"); enabled != true {
		t.Error("a change that said `enabled` did not take")
	}
}

// Delete and reset take beacons away; a name that is not there is an
// error rather than a silent success.
func TestDeleteAndReset(t *testing.T) {
	e, _ := controlEngine(t)
	if err := e.Add("load", value.NewMap(0)); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete("nosuchbeacon"); err == nil {
		t.Error("deleting a beacon that is not configured reported success")
	}
	if err := e.Delete("load"); err != nil {
		t.Fatal(err)
	}
	if e.List().Len() != 0 {
		t.Error("the deleted beacon is still listed")
	}

	if err := e.Add("load", value.NewMap(0)); err != nil {
		t.Fatal(err)
	}
	if err := e.Reset(); err != nil {
		t.Fatal(err)
	}
	if e.List().Len() != 0 {
		t.Error("reset left something behind")
	}
}

// What `beacons.save` writes has to be a file the loader reads and a
// person can edit, which means the same list form the configuration
// uses.
func TestASnapshotRoundTripsThroughTheParser(t *testing.T) {
	e, _ := controlEngine(t)
	err := e.Add("diskusage", value.MapOf("beacon_data", []any{
		value.MapOf("/", "85%"),
		value.MapOf("/var", "90%"),
		value.MapOf("interval", int64(60)),
		value.MapOf("onchangeonly", true),
		value.MapOf("disable_during_state_run", true),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetEnabled("diskusage", false); err != nil {
		t.Fatal(err)
	}

	back, err := Parse(e.Snapshot())
	if err != nil {
		t.Fatalf("the snapshot does not parse: %v", err)
	}
	if len(back) != 1 {
		t.Fatalf("the snapshot parsed to %d beacons", len(back))
	}
	in := back[0]
	if in.Name != "diskusage" || in.Interval != time.Minute {
		t.Errorf("round-tripped as %+v", in)
	}
	if !in.OnChangeOnly || !in.DisableDuringStateRun || !in.Disabled {
		t.Errorf("the controls did not survive: %+v", in)
	}
	if got := in.StringArg("/var", ""); got != "90%" {
		t.Errorf("the thresholds did not survive: %q", got)
	}
}
