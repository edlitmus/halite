//go:build windows

package winsvc

import (
	"strings"
	"testing"
)

// The manager is asked about a service every Windows has.
//
// Reading needs no administrator rights, which is deliberate: an
// operator running `service.status` on a node they are not root on
// should get an answer, and the access this asks for is what makes that
// true.
func TestQueryReadsARealService(t *testing.T) {
	// The event log: present on every Windows since NT, running on every
	// one that is working, and named the same on all of them.
	const name = "EventLog"

	got, err := Query(name)
	if err != nil {
		t.Fatalf("querying %s: %v", name, err)
	}
	if got.Name != name {
		t.Errorf("name = %q", got.Name)
	}
	if got.State != "running" {
		t.Errorf("state = %q; the event log should be running", got.State)
	}
	if got.PID == 0 {
		t.Error("a running service reported no process")
	}
	if got.DisplayName == "" {
		t.Error("no display name; the console shows one")
	}
	if got.StartType != "auto" && got.StartType != "auto_delayed" {
		t.Errorf("start type = %q; the event log starts with the machine", got.StartType)
	}
	if got.BinaryPath == "" {
		t.Error("no binary path")
	}
	if got.Account == "" {
		t.Error("no account; every service runs as something")
	}

	running, err := Running(name)
	if err != nil {
		t.Fatal(err)
	}
	if !running {
		t.Error("Running disagrees with Query about the event log")
	}
	enabled, err := Enabled(name)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Error("the event log is not reported as enabled")
	}
	if !Exists(name) {
		t.Error("Exists says the event log is not there")
	}
}

// A service that is not there is an error naming it, and saying which of
// the two names it was looking for.
//
// The distinction is not pedantry: the spooler is `Spooler` and shows as
// "Print Spooler", so a tree written from the console gets this wrong
// often enough that the message has to say so.
func TestQueryingAServiceThatIsNotThereSaysWhichNameItWanted(t *testing.T) {
	_, err := Query("no-such-service-on-this-host")
	if err == nil {
		t.Fatal("a service that does not exist was found")
	}
	if !strings.Contains(err.Error(), "no-such-service-on-this-host") {
		t.Errorf("the error does not name the service: %v", err)
	}
	if !strings.Contains(err.Error(), "display name") {
		t.Errorf("the error does not distinguish the two names: %v", err)
	}
	if Exists("no-such-service-on-this-host") {
		t.Error("Exists found a service that is not there")
	}
}

// Listing is one call rather than one per service, and it has to return
// the machine's real inventory.
func TestListNamesTheMachinesServices(t *testing.T) {
	names, err := List()
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(names) < 20 {
		t.Fatalf("%d services; a working Windows has many more", len(names))
	}
	found := false
	for _, n := range names {
		if n == "EventLog" {
			found = true
		}
	}
	if !found {
		t.Error("the listing does not include the event log")
	}
	// Sorted, because `service.get_all` promises it and a caller
	// comparing two runs depends on it.
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("the listing is not sorted: %q before %q", names[i-1], names[i])
		}
	}

	// The status listing carries what the name listing cannot.
	statuses, err := ListStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != len(names) {
		t.Errorf("%d statuses for %d names", len(statuses), len(names))
	}
	running := 0
	for _, s := range statuses {
		if s.State == "running" {
			running++
		}
	}
	if running == 0 {
		t.Error("no service is running, on a machine that is running")
	}
}

// A start type round-trips between the name a state writes and what the
// manager stores. Delayed automatic start is a separate flag, and a
// build that lost it would report a delayed service as `auto` and change
// it on every run.
func TestStartTypeNamesRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		delayed bool
	}{
		{"auto", false},
		{"auto_delayed", true},
		{"manual", false},
		{"disabled", false},
	}
	for _, c := range cases {
		code, delayed, err := StartTypeCode(c.name)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if delayed != c.delayed {
			t.Errorf("%s: delayed = %v", c.name, delayed)
		}
		if got := startTypeName(code, delayed); got != c.name {
			t.Errorf("%s -> %d -> %s", c.name, code, got)
		}
	}

	// Salt's spellings are accepted too, because a tree being migrated
	// already writes them.
	for _, alias := range []string{"automatic", "demand", "delayed"} {
		if _, _, err := StartTypeCode(alias); err != nil {
			t.Errorf("%s should be understood: %v", alias, err)
		}
	}

	// A start type that is not settable says so rather than being
	// accepted and ignored.
	if _, _, err := StartTypeCode("boot"); err == nil {
		t.Error("boot was accepted as a settable start type")
	}
	if _, _, err := StartTypeCode("sometimes"); err == nil {
		t.Error("an invented start type was accepted")
	}
}

// Every state the manager can report has a name. An unnamed state would
// reach a comparison as a number and never match anything.
func TestEveryStateHasAName(t *testing.T) {
	for code := uint32(1); code <= 7; code++ {
		if name := stateName(code); strings.HasPrefix(name, "unknown") {
			t.Errorf("state %d has no name", code)
		}
	}
	if got := stateName(99); !strings.Contains(got, "99") {
		t.Errorf("an unknown state should carry its code: %q", got)
	}
}

// Starting a service that is already running is not an error, and
// stopping one that is already stopped is not either. A state that
// declares `running: True` runs this on every highstate.
func TestStartingWhatIsAlreadyRunningIsNotAnError(t *testing.T) {
	// The event log cannot be stopped, so this exercises the
	// already-running path without touching the machine's state.
	if err := Start("EventLog"); err != nil {
		if strings.Contains(err.Error(), "administrator") {
			t.Skip("starting a service needs administrator rights, and this process does not have them")
		}
		t.Errorf("starting a running service reported %v", err)
	}
}
