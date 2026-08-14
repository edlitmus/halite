package modules

import (
	"fmt"
	"testing"
)

// fakeService is a service manager whose answers the test controls.
type fakeService struct {
	isEnabled bool
	probeErr  error
	calls     []string
}

func (f *fakeService) backend() *svcBackend {
	return &svcBackend{
		name:    "fake",
		running: func(string) bool { return true },
		enable:  func(n string) error { f.calls = append(f.calls, "enable "+n); f.isEnabled = true; return nil },
		disable: func(n string) error { f.calls = append(f.calls, "disable "+n); f.isEnabled = false; return nil },
		enabled: func(string) (bool, error) { return f.isEnabled, f.probeErr },
	}
}

func TestServiceIsEnabledThenLeftAlone(t *testing.T) {
	fake := &fakeService{}
	args := map[string]any{"name": "nginx"}

	r := applyServiceEnablement(fake.backend(), &Ctx{}, "nginx", args, true)
	if !r.Ok || !r.Changed {
		t.Fatalf("want the service enabled: %+v", r)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "enable nginx" {
		t.Fatalf("unexpected calls %v", fake.calls)
	}

	r = applyServiceEnablement(fake.backend(), &Ctx{}, "nginx", args, true)
	if !r.Ok || r.Changed {
		t.Fatalf("a second run should be a no-op: %+v", r)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("an enabled service should not be enabled again: %v", fake.calls)
	}
}

func TestServiceIsDisabled(t *testing.T) {
	fake := &fakeService{isEnabled: true}

	r := applyServiceEnablement(fake.backend(), &Ctx{}, "nginx", nil, false)
	if !r.Changed {
		t.Fatalf("want the service disabled: %+v", r)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "disable nginx" {
		t.Fatalf("unexpected calls %v", fake.calls)
	}
	if r := applyServiceEnablement(fake.backend(), &Ctx{}, "nginx", nil, false); r.Changed {
		t.Fatalf("a disabled service is already right: %+v", r)
	}
}

func TestEnablementDryRunCallsNothing(t *testing.T) {
	fake := &fakeService{}
	r := applyServiceEnablement(fake.backend(), &Ctx{Test: true}, "nginx", nil, true)
	if !r.Ok || !r.Changed {
		t.Fatalf("a dry run should report the pending change: %+v", r)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("a dry run must not touch the service manager: %v", fake.calls)
	}
}

func TestEnablementNeedsABackendThatCanProbe(t *testing.T) {
	// Without a probe every run would report a change, and being
	// idempotent about boot configuration is the whole point of the state.
	fake := &fakeService{probeErr: fmt.Errorf("launchd cannot report enablement")}
	r := applyServiceEnablement(fake.backend(), &Ctx{}, "nginx", nil, true)
	if r.Ok {
		t.Fatalf("want a failure the operator can read: %+v", r)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("nothing should have been called: %v", fake.calls)
	}
}

func TestRcConfValueTakesTheLastAssignment(t *testing.T) {
	conf := `# /etc/rc.conf
hostname="old.example.com"
sshd_enable="YES"
hostname="web1.example.com"
`
	cases := []struct {
		key  string
		want string
	}{
		{"hostname", "web1.example.com"}, // the shell would use the last one
		{"sshd_enable", "YES"},
		{"absent", ""},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := rcConfValue(conf, tc.key); got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestHostnameStateNeedsAName(t *testing.T) {
	if r := networkSystem(&Ctx{}, "", map[string]any{}); r.Ok {
		t.Fatal("a hostname state with no name should fail")
	}
}
