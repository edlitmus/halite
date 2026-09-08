package builtin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// fakeSystemd records the systemdBackend calls the provider makes and
// answers reads from set fields.
type fakeSystemd struct {
	calls     []string
	active    string
	fileState string
	units     []string
	readErr   error
	jobErr    error
	closed    bool
}

func (f *fakeSystemd) runJob(verb, unit string) error {
	f.calls = append(f.calls, verb+" "+unit)
	return f.jobErr
}
func (f *fakeSystemd) enable(u string) error  { f.calls = append(f.calls, "enable "+u); return nil }
func (f *fakeSystemd) disable(u string) error { f.calls = append(f.calls, "disable "+u); return nil }
func (f *fakeSystemd) mask(u string) error    { f.calls = append(f.calls, "mask "+u); return nil }
func (f *fakeSystemd) unmask(u string) error  { f.calls = append(f.calls, "unmask "+u); return nil }
func (f *fakeSystemd) activeState(u string) (string, error) {
	f.calls = append(f.calls, "activeState "+u)
	return f.active, f.readErr
}
func (f *fakeSystemd) unitFileState(u string) (string, error) {
	f.calls = append(f.calls, "unitFileState "+u)
	return f.fileState, f.readErr
}
func (f *fakeSystemd) listUnitFiles() ([]string, error) {
	f.calls = append(f.calls, "listUnitFiles")
	return f.units, f.readErr
}
func (f *fakeSystemd) Close() error { f.closed = true; return nil }

// useSystemd points systemdBus at fake. A nil fake stands for a bus that
// cannot be reached, which is the case the systemctl fallback is for.
func useSystemd(t *testing.T, fake *fakeSystemd) {
	t.Helper()
	old := systemdBus
	systemdBus = func(context.Context) (systemdBackend, error) {
		if fake == nil {
			return nil, errors.New("dbus: no system bus")
		}
		return fake, nil
	}
	t.Cleanup(func() { systemdBus = old })
}

func serviceCtx(t *testing.T, responses map[string]exec.Result) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	c := newCtx(false)
	r := &exec.RecordingRunner{Responses: responses}
	c.Runner = r
	return c, r
}

// The mutating verbs go over D-Bus and nothing is shelled out.
func TestSystemdProviderMutatesOverDBus(t *testing.T) {
	fake := &fakeSystemd{}
	useSystemd(t, fake)
	c, runner := serviceCtx(t, nil)
	p := systemdProvider{}

	for _, step := range []struct {
		do   func() error
		want string
	}{
		{func() error { return p.Start(c, "cron") }, "StartUnit cron.service"},
		{func() error { return p.Stop(c, "cron") }, "StopUnit cron.service"},
		{func() error { return p.Restart(c, "cron") }, "RestartUnit cron.service"},
		{func() error { return p.Reload(c, "nginx.service") }, "ReloadUnit nginx.service"},
		{func() error { return p.Enable(c, "cron") }, "enable cron.service"},
		{func() error { return p.Disable(c, "cron") }, "disable cron.service"},
		{func() error { return p.Mask(c, "cron") }, "mask cron.service"},
		{func() error { return p.Unmask(c, "cron") }, "unmask cron.service"},
	} {
		if err := step.do(); err != nil {
			t.Fatalf("%s: %v", step.want, err)
		}
	}
	got := strings.Join(fake.calls, "\n")
	for _, want := range []string{
		"StartUnit cron.service", "StopUnit cron.service", "RestartUnit cron.service",
		"ReloadUnit nginx.service", "enable cron.service", "disable cron.service",
		"mask cron.service", "unmask cron.service",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("D-Bus call %q was not made; calls were:\n%s", want, got)
		}
	}
	if ran := runner.RanCommands(); len(ran) != 0 {
		t.Errorf("systemctl was shelled out to while the bus was up: %v", ran)
	}
	if !fake.closed {
		t.Error("the connection was not closed")
	}
}

// Reads go over D-Bus and map systemd's own vocabulary.
func TestSystemdProviderReadsOverDBus(t *testing.T) {
	useSystemd(t, &fakeSystemd{active: "active", fileState: "enabled"})
	c, _ := serviceCtx(t, nil)
	p := systemdProvider{}

	if running, err := p.Status(c, "cron"); err != nil || !running {
		t.Errorf("Status = %v, %v; want true", running, err)
	}
	if enabled, err := p.Enabled(c, "cron"); err != nil || !enabled {
		t.Errorf("Enabled = %v, %v; want true", enabled, err)
	}

	useSystemd(t, &fakeSystemd{active: "inactive", fileState: "masked"})
	if running, _ := p.Status(c, "cron"); running {
		t.Error("Status reported an inactive unit as running")
	}
	if enabled, _ := p.Enabled(c, "cron"); enabled {
		t.Error("Enabled reported a masked unit as enabled")
	}
	if masked, err := p.Masked(c, "cron"); err != nil || !masked {
		t.Errorf("Masked = %v, %v; want true", masked, err)
	}
}

func TestSystemdProviderListsOverDBus(t *testing.T) {
	useSystemd(t, &fakeSystemd{units: []string{"a.service", "b.service"}})
	c, _ := serviceCtx(t, nil)

	got, err := systemdProvider{}.List(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "a.service,b.service" {
		t.Errorf("List = %v", got)
	}
}

// When the bus cannot be reached, every operation falls back to
// `systemctl` unchanged -- SPEC 15.2's "falling back" clause.
func TestSystemdProviderFallsBackToSystemctl(t *testing.T) {
	useSystemd(t, nil) // bus unreachable
	c, runner := serviceCtx(t, map[string]exec.Result{
		"systemctl is-active --quiet cron": {Code: 0},
		"systemctl is-enabled cron":        {Stdout: "enabled\n"},
	})
	p := systemdProvider{}

	if err := p.Start(c, "cron"); err != nil {
		t.Fatal(err)
	}
	if err := p.Enable(c, "cron"); err != nil {
		t.Fatal(err)
	}
	if running, err := p.Status(c, "cron"); err != nil || !running {
		t.Errorf("Status via fallback = %v, %v", running, err)
	}
	if enabled, err := p.Enabled(c, "cron"); err != nil || !enabled {
		t.Errorf("Enabled via fallback = %v, %v", enabled, err)
	}

	ran := strings.Join(runner.RanCommands(), "\n")
	for _, want := range []string{"systemctl start cron", "systemctl enable cron"} {
		if !strings.Contains(ran, want) {
			t.Errorf("fallback did not run %q; ran:\n%s", want, ran)
		}
	}
}

// A read that reached systemd and failed there (a malformed name) falls
// through to systemctl rather than failing the caller.
func TestSystemdProviderReadErrorFallsThrough(t *testing.T) {
	useSystemd(t, &fakeSystemd{readErr: errors.New("org.freedesktop.systemd1.NoSuchUnit")})
	c, runner := serviceCtx(t, map[string]exec.Result{
		"systemctl is-active --quiet cron": {Code: 3},
	})

	running, err := systemdProvider{}.Status(c, "cron")
	if err != nil {
		t.Fatalf("Status returned an error instead of falling through: %v", err)
	}
	if running {
		t.Error("Status = true for a unit systemd said it does not have")
	}
	if !strings.Contains(strings.Join(runner.RanCommands(), " "), "systemctl is-active") {
		t.Error("the read did not fall through to systemctl")
	}
}

// A job that finished "failed" is a real error from systemd and is
// returned -- never retried on the shell where it would fail again.
func TestSystemdProviderJobFailureIsNotRetriedOnTheShell(t *testing.T) {
	useSystemd(t, &fakeSystemd{jobErr: errors.New(`job for cron.service finished "failed"`)})
	c, runner := serviceCtx(t, nil)

	err := systemdProvider{}.Start(c, "cron")
	if err == nil {
		t.Fatal("a failed job was reported as success")
	}
	if !strings.Contains(err.Error(), "failed") {
		t.Errorf("error does not carry systemd's outcome: %v", err)
	}
	if len(runner.RanCommands()) != 0 {
		t.Errorf("a job error was retried on the shell: %v", runner.RanCommands())
	}
}
