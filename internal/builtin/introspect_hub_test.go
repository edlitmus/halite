package builtin

import (
	"errors"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// recordingEvents is the bus a test watches instead of a hub.
type recordingEvents struct {
	tag  string
	data map[string]any
	err  error
}

func (r *recordingEvents) Send(tag string, data map[string]any) error {
	if r.err != nil {
		return r.err
	}
	r.tag, r.data = tag, data
	return nil
}

// `event.send` is what a state or a reaction fires an event with. It
// was registered as a stub that reported it needed a phase already
// delivered, so a tree that used it failed on every node.
func TestEventSendReachesTheBus(t *testing.T) {
	r := New()
	bus := &recordingEvents{}
	c := newCtx(false)
	c.Events = bus

	got, err := r.Exec.Call(c, "event.send", value.MapOf(
		"tag", "deploy/done",
		"data", value.MapOf("version", "1.2"),
	))
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Errorf("event.send returned %v", got)
	}
	if bus.tag != "deploy/done" {
		t.Errorf("the tag sent was %q", bus.tag)
	}
	if bus.data["version"] != "1.2" {
		t.Errorf("the payload sent was %v", bus.data)
	}
}

// A node with no hub has nowhere to put an event, and saying so beats
// reporting a success nobody received.
func TestEventSendWithoutAHubSaysWhy(t *testing.T) {
	r := New()
	_, err := r.Exec.Call(newCtx(false), "event.send", value.MapOf("tag", "deploy/done"))
	if err == nil {
		t.Fatal("event.send with no bus reported success")
	}
	if !strings.Contains(err.Error(), "no hub") {
		t.Errorf("event.send said %q", err)
	}
}

// A failure to reach the hub is the module's failure, not a silent one.
func TestEventSendReportsABusThatRefused(t *testing.T) {
	r := New()
	c := newCtx(false)
	c.Events = &recordingEvents{err: errors.New("the hub refused the tag")}
	if _, err := r.Exec.Call(c, "event.send", value.MapOf("tag", "halite/job/forged")); err == nil {
		t.Fatal("a refused event was reported as sent")
	}
}

// `pillar.refresh` and `saltutil.refresh_pillar` are two names for one
// thing, and both were stubs saying they needed phase 2.
func TestPillarRefreshRebuildsThroughTheContext(t *testing.T) {
	for _, name := range []string{"pillar.refresh", "saltutil.refresh_pillar"} {
		r := New()
		calls := 0
		c := newCtx(false)
		c.RecompilePillar = func() (*value.Map, error) {
			calls++
			return value.MapOf("a", 1, "b", 2), nil
		}

		got, err := r.Exec.Call(c, name, value.NewMap(0))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if calls != 1 {
			t.Errorf("%s rebuilt the pillar %d times", name, calls)
		}
		out, ok := got.(*value.Map)
		if !ok {
			t.Fatalf("%s returned %T", name, got)
		}
		if keys, _ := out.Get("keys"); keys != int64(2) {
			t.Errorf("%s reported %v keys", name, keys)
		}
	}
}

// A pillar that will not compile is what this function exists to find
// out, so it must not report a refresh.
func TestPillarRefreshReportsAPillarThatWillNotCompile(t *testing.T) {
	r := New()
	c := newCtx(false)
	c.RecompilePillar = func() (*value.Map, error) {
		return nil, errors.New("top.sls:3:1: that is not a target")
	}
	if _, err := r.Exec.Call(c, "pillar.refresh", value.NewMap(0)); err == nil {
		t.Fatal("a pillar that will not compile was reported as refreshed")
	}
}

var _ exec.EventSender = (*recordingEvents)(nil)
