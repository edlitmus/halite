package builtin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/beacon"
	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The `proc` and `ps` beacons, against the real process table.
//
// They live here rather than in internal/beacon because a beacon in
// this build is a function over the node's own execution modules, and
// the registry that holds those is this package. The wiring below is
// the node's own: a dispatcher over the execution registry, which is
// exactly what `cmd/halite-node` hands a beacon at runtime.
//
// Every process matched here is one the test started and marked, for
// the reason live_ps_test.go gives: a pattern loose enough to match
// somebody's editor will eventually be given to something that kills.

// beaconDispatcher is the node's wiring, in miniature.
type beaconDispatcher struct{ r *hexec.Registry }

func (d beaconDispatcher) Call(c *hexec.Context, name string, args *value.Map) (any, error) {
	return d.r.Call(c, name, args)
}

func (d beaconDispatcher) CallPositional(c *hexec.Context, name string, args []any, kwargs *value.Map) (any, error) {
	return nil, fmt.Errorf("the beacon tests call by keyword")
}

func (d beaconDispatcher) Has(name string) bool { return d.r.Has(name) }

func beaconContext() *hexec.Context {
	return &hexec.Context{Dispatch: beaconDispatcher{New().Exec}}
}

// fire runs one beacon with the arguments given.
func fire(t *testing.T, name string, args *value.Map) []beacon.Event {
	t.Helper()
	mod, ok := beacon.New().Lookup(name)
	if !ok {
		t.Fatalf("no %s beacon is registered", name)
	}
	if mod.Fn == nil {
		t.Fatalf("the %s beacon is registered as pending: %s", name, mod.Pending)
	}
	events, err := mod.Fn(beaconContext(), &beacon.Instance{Name: name, Args: args})
	if err != nil {
		t.Fatalf("%s beacon: %v", name, err)
	}
	return events
}

func TestLiveProcBeaconSeesAProcessArriveAndSeesOneMissing(t *testing.T) {
	psSkipUnlessUnix(t)
	marker := psMarker(t)
	pid := startMarkedChild(t, marker)

	// Present, and asked about without a wanted state: the reading is
	// reported every poll and `onchangeonly` decides whether it is an
	// event, which is how the `service` beacon beside this one behaves.
	events := fire(t, "proc", value.MapOf(
		"processes", value.MapOf(marker, ""),
		"full", true,
	))
	if len(events) != 1 {
		t.Fatalf("the proc beacon fired %d times, want 1: %v", len(events), events)
	}
	if got := events[0].Data["running"]; got != true {
		t.Errorf("the event says running=%v for a process that is running", got)
	}
	if got := events[0].Suffix; got != marker {
		t.Errorf("the event is tagged %q, want the configured name", got)
	}
	pids, _ := events[0].Data["pids"].([]any)
	if !psContainsPID(pids, pid) {
		t.Errorf("the event carries %v, which does not include %d", pids, pid)
	}

	// `running` speaks only when it is; `stopped` only when it is not.
	if events := fire(t, "proc", value.MapOf(
		"processes", value.MapOf(marker, "running"), "full", true)); len(events) != 1 {
		t.Errorf("`running` said nothing about a process that is running")
	}
	if events := fire(t, "proc", value.MapOf(
		"processes", value.MapOf(marker, "stopped"), "full", true)); len(events) != 0 {
		t.Errorf("`stopped` fired for a process that is running: %v", events)
	}

	absent := marker + "-never-started"
	if events := fire(t, "proc", value.MapOf(
		"processes", value.MapOf(absent, "stopped"), "full", true)); len(events) != 1 {
		t.Errorf("`stopped` said nothing about a process that is not there")
	} else if got := events[0].Data["running"]; got != false {
		t.Errorf("the absence event says running=%v", got)
	}
	if events := fire(t, "proc", value.MapOf(
		"processes", value.MapOf(absent, "running"), "full", true)); len(events) != 0 {
		t.Errorf("`running` fired for a process that is not there: %v", events)
	}
}

func TestLivePSBeaconFiresOnAThresholdAndKeepsQuietBelowIt(t *testing.T) {
	psSkipUnlessUnix(t)
	marker := psMarker(t)
	pid := startMarkedChild(t, marker)

	// A threshold every process crosses, so the firing path is
	// exercised against a real row rather than a contrived one.
	events := fire(t, "ps", value.MapOf(
		"processes", value.MapOf(marker, value.MapOf("rss_kb", []any{">", int64(0)})),
		"full", true,
	))
	if len(events) != 1 {
		t.Fatalf("the ps beacon fired %d times, want 1: %v", len(events), events)
	}
	data := events[0].Data
	if data["field"] != "rss_kb" || data["comparison"] != ">" {
		t.Errorf("the event reads %v", data)
	}
	if got := data["pid"]; got != int64(pid) {
		t.Errorf("the event names pid %v, want %d", got, pid)
	}
	if got, _ := data["value"].(float64); got <= 0 {
		t.Errorf("the event carries a resident size of %v", got)
	}
	if command, _ := data["command"].(string); !strings.Contains(command, marker) {
		t.Errorf("the event's command is %q", command)
	}

	// A threshold nothing crosses says nothing at all.
	if events := fire(t, "ps", value.MapOf(
		"processes", value.MapOf(marker, value.MapOf("rss_kb", []any{">", int64(1) << 40})),
		"full", true,
	)); len(events) != 0 {
		t.Errorf("the ps beacon fired below its threshold: %v", events)
	}
}

// A configuration that cannot mean anything is refused rather than
// watched. A beacon is set up once and trusted for years, and one that
// silently watches nothing is the failure that is never noticed.
func TestTheProcessBeaconsRefuseAConfigurationThatMeansNothing(t *testing.T) {
	psSkipUnlessUnix(t)

	for _, tc := range []struct {
		beacon string
		args   *value.Map
		want   string
	}{
		{"proc", value.NewMap(0), "needs `processes`"},
		{"ps", value.NewMap(0), "needs `processes`"},
		{"proc", value.MapOf("processes", value.NewMap(0)), "is empty"},
		{"ps", value.MapOf("processes", value.MapOf("x", value.NewMap(0))), "at least one threshold"},
		{"ps", value.MapOf("processes", value.MapOf("x", "not-a-mapping")), "at least one threshold"},
	} {
		mod, _ := beacon.New().Lookup(tc.beacon)
		_, err := mod.Fn(beaconContext(), &beacon.Instance{Name: tc.beacon, Args: tc.args})
		if err == nil {
			t.Errorf("the %s beacon accepted %v", tc.beacon, tc.args.StringKeys())
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("the %s beacon refused with %q, want it to mention %q", tc.beacon, err, tc.want)
		}
	}

	// A threshold on a field the process table does not have is a
	// refusal naming the fields it does, because the alternative is a
	// beacon that never fires and never says why.
	marker := psMarker(t)
	startMarkedChild(t, marker)
	mod, _ := beacon.New().Lookup("ps")
	_, err := mod.Fn(beaconContext(), &beacon.Instance{Name: "ps", Args: value.MapOf(
		"processes", value.MapOf(marker, value.MapOf("cpu_load", []any{">", int64(1)})),
		"full", true,
	)})
	if err == nil {
		t.Fatal("a threshold on a field that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "cpu_percent") {
		t.Errorf("the refusal does not list the fields there are: %v", err)
	}
}
