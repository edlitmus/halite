package modules

import (
	"fmt"
	"strings"
	"testing"
)

// fakeSvcBackend is a backend whose probes are canned and whose actions are
// counted, so state logic can be tested without a real service manager.
type fakeSvcBackend struct {
	running    bool
	enabled    bool
	enabledErr error

	enableCalls, startCalls, restartCalls int
}

func (f *fakeSvcBackend) backend() *svcBackend {
	return &svcBackend{
		name:    "fake",
		running: func(string) bool { return f.running },
		start:   func(string) error { f.startCalls++; return nil },
		stop:    func(string) error { return nil },
		restart: func(string) error { f.restartCalls++; return nil },
		enable:  func(string) error { f.enableCalls++; return nil },
		enabled: func(string) (bool, error) { return f.enabled, f.enabledErr },
	}
}

func TestServiceRunningDoesNotReEnableAnEnabledService(t *testing.T) {
	fake := &fakeSvcBackend{running: true, enabled: true}
	res := applyServiceRunning(fake.backend(), &Ctx{}, "nginx", map[string]any{"enable": "true"})
	if !res.Ok || res.Changed {
		t.Fatalf("already running and enabled must be a no-op: %+v", res)
	}
	if fake.enableCalls != 0 {
		t.Errorf("enable called %d times on an already-enabled service", fake.enableCalls)
	}
}

func TestServiceRunningEnablesADisabledService(t *testing.T) {
	fake := &fakeSvcBackend{running: true, enabled: false}
	res := applyServiceRunning(fake.backend(), &Ctx{}, "nginx", map[string]any{"enable": "true"})
	if !res.Ok || !res.Changed || res.Changes["enabled"] != "true" {
		t.Fatalf("disabled service not reported enabled: %+v", res)
	}
	if fake.enableCalls != 1 {
		t.Errorf("enable called %d times, want 1", fake.enableCalls)
	}
}

func TestServiceRunningTestModeReportsEnableOnlyWhenDisabled(t *testing.T) {
	fake := &fakeSvcBackend{running: true, enabled: true}
	res := applyServiceRunning(fake.backend(), &Ctx{Test: true}, "nginx", map[string]any{"enable": "true"})
	if !res.Ok || res.Changed {
		t.Errorf("test mode reported a pending change for an enabled service: %+v", res)
	}

	fake = &fakeSvcBackend{running: true, enabled: false}
	res = applyServiceRunning(fake.backend(), &Ctx{Test: true}, "nginx", map[string]any{"enable": "true"})
	if !res.Ok || !res.Changed || !strings.Contains(res.Comment, "would be enabled") {
		t.Errorf("test mode did not report the pending enable: %+v", res)
	}
	if fake.enableCalls != 0 {
		t.Errorf("test mode called enable %d times", fake.enableCalls)
	}
}

func TestServiceRunningUnprobeableBackendEnablesWithoutReportingChange(t *testing.T) {
	fake := &fakeSvcBackend{running: true, enabledErr: fmt.Errorf("cannot probe")}
	res := applyServiceRunning(fake.backend(), &Ctx{}, "nginx", map[string]any{"enable": "true"})
	if !res.Ok || res.Changed {
		t.Fatalf("an unprovable enable must not be reported as a change: %+v", res)
	}
	if fake.enableCalls != 1 {
		t.Errorf("enable called %d times, want the best-effort 1", fake.enableCalls)
	}
}

func TestServiceRunningStartsAStoppedService(t *testing.T) {
	fake := &fakeSvcBackend{running: false, enabled: true}
	res := applyServiceRunning(fake.backend(), &Ctx{}, "nginx", map[string]any{"enable": "true"})
	if !res.Ok || !res.Changed || res.Changes["started"] != "true" {
		t.Fatalf("stopped service not started: %+v", res)
	}
	if fake.enableCalls != 0 || fake.startCalls != 1 {
		t.Errorf("enable=%d start=%d, want 0 and 1", fake.enableCalls, fake.startCalls)
	}
}
