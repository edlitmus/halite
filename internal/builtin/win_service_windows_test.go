package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// The virtual `service` module reaches the service control manager here.
//
// It reached nothing before: a Windows node answered every service call
// with "no init system was recognised on this node", naming systemd,
// FreeBSD rc, sysvinit and launchd, so a node on this platform could not
// be told to start anything.
func TestTheServiceModuleFindsTheWindowsProvider(t *testing.T) {
	c := realCtx(t)
	p, err := pickServiceProvider(c)
	if err != nil {
		t.Fatalf("no provider was chosen on Windows: %v", err)
	}
	if p.Name() != "windows" {
		t.Fatalf("provider = %q, want windows", p.Name())
	}

	// And the module answers about a real service. The event log is on
	// every Windows since NT and running on every one that is working.
	r := New()
	running, err := r.Exec.Call(c, "service.status", value.MapOf("name", "EventLog"))
	if err != nil {
		t.Fatalf("service.status: %v", err)
	}
	if running != true {
		t.Errorf("service.status EventLog = %#v", running)
	}
	enabled, err := r.Exec.Call(c, "service.enabled", value.MapOf("name", "EventLog"))
	if err != nil {
		t.Fatalf("service.enabled: %v", err)
	}
	if enabled != true {
		t.Errorf("service.enabled EventLog = %#v", enabled)
	}

	all, err := r.Exec.Call(c, "service.get_all", value.NewMap(0))
	if err != nil {
		t.Fatalf("service.get_all: %v", err)
	}
	names, ok := all.([]any)
	if !ok || len(names) < 20 {
		t.Fatalf("service.get_all returned %#v", all)
	}
}

// win_service.info carries what the virtual module has no place for.
func TestWinServiceInfoCarriesWhatServiceCannot(t *testing.T) {
	r := New()
	got, err := r.Exec.Call(realCtx(t), "win_service.info", value.MapOf("name", "EventLog"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.(*value.Map)
	if !ok {
		t.Fatalf("info returned %#v", got)
	}
	for _, key := range []string{"name", "display_name", "state", "start_type", "pid",
		"binary_path", "account", "description"} {
		if _, has := m.Get(key); !has {
			t.Errorf("%s is absent from win_service.info", key)
		}
	}
	if state, _ := m.Get("state"); state != "running" {
		t.Errorf("state = %#v", state)
	}
	if display, _ := m.Get("display_name"); display == "" {
		t.Error("display_name is empty; the console shows one")
	}
	// The name the manager knows and the name the console shows differ
	// for most of what ships with Windows, which is the reason this
	// module reports both.
	name, _ := m.Get("name")
	display, _ := m.Get("display_name")
	if name == display {
		t.Logf("this service's two names agree: %v", name)
	}
}

// The start type is one of four values, not a boolean, which is why
// win_service has its own state for it.
func TestWinServiceStartTypeReadsOneOfFourValues(t *testing.T) {
	r := New()
	got, err := r.Exec.Call(realCtx(t), "win_service.get_start_type", value.MapOf("name", "EventLog"))
	if err != nil {
		t.Fatal(err)
	}
	switch got {
	case "auto", "auto_delayed", "manual", "disabled", "boot", "system":
	default:
		t.Errorf("start type = %#v", got)
	}

	// Its state converges: asked for what the host already has, it
	// reports no change.
	current, _ := got.(string)
	res, err := r.States.Call(realCtx(t), "win_service.start_type",
		value.MapOf("name", "EventLog", "start_type", current))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changes.Len() != 0 {
		t.Errorf("a state asking for what the host has reported changes: %+v", res.Changes)
	}
	if !strings.Contains(res.Comment, "already") {
		t.Errorf("comment = %q", res.Comment)
	}
}

// Test mode changes nothing, which for a service manager matters more
// than for most modules: an operator runs --test precisely because they
// are not ready to restart anything.
func TestWinServiceStartTypeInTestModeChangesNothing(t *testing.T) {
	r := New()
	before, err := r.Exec.Call(realCtx(t), "win_service.get_start_type", value.MapOf("name", "EventLog"))
	if err != nil {
		t.Fatal(err)
	}
	want := "manual"
	if before == "manual" {
		want = "auto"
	}

	res, err := r.States.Call(testCtx(t), "win_service.start_type",
		value.MapOf("name", "EventLog", "start_type", want))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != nil {
		t.Errorf("test mode should predict, not decide: %+v", res)
	}
	if res.Changes.Len() == 0 {
		t.Error("test mode predicted no change for a start type it would have altered")
	}

	after, err := r.Exec.Call(realCtx(t), "win_service.get_start_type", value.MapOf("name", "EventLog"))
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("test mode changed the start type from %v to %v", before, after)
	}
}

// A service that is not there is an error naming it and saying which of
// the two names it wanted.
func TestAServiceThatIsNotThereIsNamed(t *testing.T) {
	r := New()
	_, err := r.Exec.Call(realCtx(t), "service.status",
		value.MapOf("name", "no-such-service-on-this-host"))
	if err == nil {
		t.Fatal("a service that does not exist was found")
	}
	if !strings.Contains(err.Error(), "no-such-service-on-this-host") {
		t.Errorf("the error does not name it: %v", err)
	}
}

// Reload is refused by name rather than silently restarting, because a
// restart is a bigger change than the state asked for.
func TestReloadIsRefusedRatherThanTurnedIntoARestart(t *testing.T) {
	err := windowsProvider{}.Reload(realCtx(t), "EventLog")
	if err == nil {
		t.Fatal("reload was accepted on a platform with no reload")
	}
	if !strings.Contains(err.Error(), "service.restart") {
		t.Errorf("the refusal does not say what to use instead: %v", err)
	}
}
