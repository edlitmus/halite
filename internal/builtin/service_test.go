package builtin

import (
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// fakeBootProvider is the minimum serviceProvider bootStateNeedsChange
// reads, with no Disabled method -- every real provider but Windows.
type fakeBootProvider struct{ enabled bool }

func (f fakeBootProvider) Name() string                                      { return "fake" }
func (f fakeBootProvider) Available(c *exec.Context) bool                    { return true }
func (f fakeBootProvider) Status(c *exec.Context, name string) (bool, error) { return false, nil }
func (f fakeBootProvider) Enabled(c *exec.Context, name string) (bool, error) {
	return f.enabled, nil
}
func (f fakeBootProvider) Start(c *exec.Context, name string) error   { return nil }
func (f fakeBootProvider) Stop(c *exec.Context, name string) error    { return nil }
func (f fakeBootProvider) Restart(c *exec.Context, name string) error { return nil }
func (f fakeBootProvider) Reload(c *exec.Context, name string) error  { return nil }
func (f fakeBootProvider) Enable(c *exec.Context, name string) error  { return nil }
func (f fakeBootProvider) Disable(c *exec.Context, name string) error { return nil }

// fakeWindowsLikeProvider adds the third state Windows' `manual` is:
// not enabled, but also not the state Disable sets.
type fakeWindowsLikeProvider struct {
	fakeBootProvider
	disabled bool
}

func (f fakeWindowsLikeProvider) Disabled(c *exec.Context, name string) (bool, error) {
	return f.disabled, nil
}

// On every provider without a third state, bootStateNeedsChange is the
// plain comparison it always was: Enabled() == want decides it.
func TestBootStateNeedsChangeIsAPlainComparisonWithNoThirdState(t *testing.T) {
	cases := []struct {
		enabled, want, needsChange bool
	}{
		{enabled: true, want: true, needsChange: false},
		{enabled: false, want: false, needsChange: false},
		{enabled: true, want: false, needsChange: true},
		{enabled: false, want: true, needsChange: true},
	}
	for _, c := range cases {
		now, needs, err := bootStateNeedsChange(fakeBootProvider{enabled: c.enabled}, nil, "x", c.want)
		if err != nil {
			t.Fatal(err)
		}
		if now != c.enabled {
			t.Errorf("enabled=%v want=%v: now = %v, want %v", c.enabled, c.want, now, c.enabled)
		}
		if needs != c.needsChange {
			t.Errorf("enabled=%v want=%v: needsChange = %v, want %v", c.enabled, c.want, needs, c.needsChange)
		}
	}
}

// A manual service is not enabled, same as a disabled one, but only
// disabled is the state service.disabled promises. This is the
// Windows-specific defect: checking Enabled() == false before deciding
// there was nothing to do left a manual service manual forever.
func TestBootStateNeedsChangeTellsManualFromDisabled(t *testing.T) {
	manual := fakeWindowsLikeProvider{fakeBootProvider{enabled: false}, false}
	now, needs, err := bootStateNeedsChange(manual, nil, "x", false)
	if err != nil {
		t.Fatal(err)
	}
	if now != false {
		t.Errorf("now = %v, want false", now)
	}
	if !needs {
		t.Error("a manual service reported no change needed to become disabled")
	}

	disabled := fakeWindowsLikeProvider{fakeBootProvider{enabled: false}, true}
	if _, needs, err := bootStateNeedsChange(disabled, nil, "x", false); err != nil || needs {
		t.Errorf("an already-disabled service reported needsChange=%v, err=%v, want false, nil", needs, err)
	}

	// Moving *toward* enabled never consults Disabled: Enabled() alone
	// says whether anything needs to happen.
	enabled := fakeWindowsLikeProvider{fakeBootProvider{enabled: true}, false}
	if _, needs, err := bootStateNeedsChange(enabled, nil, "x", true); err != nil || needs {
		t.Errorf("an already-enabled service reported needsChange=%v, err=%v, want false, nil", needs, err)
	}
}
