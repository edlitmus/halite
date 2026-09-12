package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// TestSwapOnArgvIsCheckedFromAnyHost is the `quotaSetArgv` pattern: every
// platform's spelling is a pure function of GOOS, so every row is checked
// here whatever this test happens to be running on.
func TestSwapOnArgvIsCheckedFromAnyHost(t *testing.T) {
	tests := []struct {
		name     string
		goos     string
		priority int64
		want     []string
		wantErr  bool
	}{
		{"linux, no priority", "linux", -1, []string{"swapon", "/dev/sda2"}, false},
		{"linux, a priority", "linux", 5, []string{"swapon", "-p", "5", "/dev/sda2"}, false},
		{"linux, priority zero is still a priority", "linux", 0, []string{"swapon", "-p", "0", "/dev/sda2"}, false},
		{"freebsd, no priority", "freebsd", -1, []string{"swapon", "/dev/md0"}, false},
		{"freebsd, a priority is refused", "freebsd", 3, nil, true},
		{"an unknown platform is refused", "plan9", -1, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := "/dev/sda2"
			if tt.goos == "freebsd" {
				path = "/dev/md0"
			}
			got, err := swapOnArgv(tt.goos, path, tt.priority)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("swapOnArgv(%q, %q, %d) = %v, want an error", tt.goos, path, tt.priority, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("swapOnArgv(%q, %q, %d): %v", tt.goos, path, tt.priority, err)
			}
			if !equalStrings(got, tt.want) {
				t.Errorf("swapOnArgv(%q, %q, %d) = %v, want %v", tt.goos, path, tt.priority, got, tt.want)
			}
		})
	}
}

// TestSwapOffArgvIsTheSameShapeOnBothPlatforms asserts the fact the doc
// comment claims: swapoff takes no platform-specific flag, on either
// platform this module knows.
func TestSwapOffArgvIsTheSameShapeOnBothPlatforms(t *testing.T) {
	for _, goos := range []string{"linux", "freebsd"} {
		got, err := swapOffArgv(goos, "/dev/md0")
		if err != nil {
			t.Fatalf("swapOffArgv(%q, ...): %v", goos, err)
		}
		want := []string{"swapoff", "/dev/md0"}
		if !equalStrings(got, want) {
			t.Errorf("swapOffArgv(%q, ...) = %v, want %v", goos, got, want)
		}
	}
}

func TestSwapOffArgvRefusesAnUnknownPlatform(t *testing.T) {
	if _, err := swapOffArgv("plan9", "/dev/md0"); err == nil {
		t.Error("an unknown platform was accepted")
	}
}

func TestSwapPathActiveFindsAnExactKeyOnly(t *testing.T) {
	active := value.MapOf("/dev/md0", value.MapOf("size", int64(65536), "used", int64(0)))
	if !swapPathActive(active, "/dev/md0") {
		t.Error("a path that is a key in the map was reported as not active")
	}
	if swapPathActive(active, "/dev/md1") {
		t.Error("a path that is not a key in the map was reported as active")
	}
	if swapPathActive(nil, "/dev/md0") {
		t.Error("a nil map was reported as having an active path")
	}
	if swapPathActive(value.NewMap(0), "/dev/md0") {
		t.Error("an empty map was reported as having an active path")
	}
}

// swapTestCtx builds a Context whose commands are scripted and whose
// `Which` never touches the real PATH.
func swapTestCtx(responses map[string]exec.Result) *exec.Context {
	return &exec.Context{
		Runner: &exec.RecordingRunner{Responses: responses},
		Lookup: func(name string) string { return "/sbin/" + name },
	}
}

// A path that no real host's active swap table would ever contain, so
// the "already active" branch is never accidentally taken by a real
// /proc/swaps or a real swapctl -l this test cannot script -- see the
// doc comment on `swapPathActive`.
const swapTestPath = "/halite/test/no-such-swap-device-3f8e9c1"

func TestSwapOnRunsThePlatformCommandAndReportsChange(t *testing.T) {
	goos := runtime.GOOS
	if goos != "linux" && goos != "freebsd" {
		t.Skipf("this build's swapOnArgv does not know %s", goos)
	}
	c := swapTestCtx(nil)
	out, err := swapOnFn(c, value.MapOf("name", swapTestPath))
	if err != nil {
		t.Fatalf("swapOnFn: %v", err)
	}
	m := out.(*value.Map)
	if changed, _ := m.GetString("changed"); changed != true {
		t.Errorf("enabling a swap path that was not active reported no change: %v", out)
	}
	wantArgv, err := swapOnArgv(goos, swapTestPath, -1)
	if err != nil {
		t.Fatal(err)
	}
	want := (exec.Command{Argv: wantArgv}).String()
	found := false
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if ran == want {
			found = true
		}
	}
	if !found {
		t.Errorf("swapOnFn did not run %q; it ran %v", want, c.Runner.(*exec.RecordingRunner).RanCommands())
	}
}

func TestSwapOnInTestModeRunsNoMutatingCommand(t *testing.T) {
	goos := runtime.GOOS
	if goos != "linux" && goos != "freebsd" {
		t.Skipf("this build's swapOnArgv does not know %s", goos)
	}
	c := swapTestCtx(nil)
	c.Test = true
	out, err := swapOnFn(c, value.MapOf("name", swapTestPath))
	if err != nil {
		t.Fatalf("swapOnFn: %v", err)
	}
	m := out.(*value.Map)
	comment, _ := m.GetString("comment")
	if !strings.Contains(comment.(string), "Nothing was changed: this was a test run.") {
		t.Errorf("a test run's comment does not say it changed nothing: %v", comment)
	}
	for _, ran := range c.Runner.(*exec.RecordingRunner).RanCommands() {
		if strings.HasPrefix(ran, "swapon ") || ran == "swapon" {
			t.Errorf("a test run executed the mutating command: %q", ran)
		}
	}
}

func TestSwapOnRefusesWithoutTheTool(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if _, err := swapOnFn(c, value.MapOf("name", swapTestPath)); err == nil || !strings.Contains(err.Error(), "swapon") {
		t.Errorf("swapOnFn without swapon on the node = %v, want an error naming swapon", err)
	}
}

func TestSwapOnRejectsAnEmptyName(t *testing.T) {
	c := swapTestCtx(nil)
	if _, err := swapOnFn(c, value.NewMap(0)); err == nil {
		t.Error("an empty name was accepted")
	}
}

func TestSwapOnSurfacesANonZeroExit(t *testing.T) {
	goos := runtime.GOOS
	if goos != "linux" && goos != "freebsd" {
		t.Skipf("this build's swapOnArgv does not know %s", goos)
	}
	argv, err := swapOnArgv(goos, swapTestPath, -1)
	if err != nil {
		t.Fatal(err)
	}
	key := (exec.Command{Argv: argv}).String()
	c := swapTestCtx(map[string]exec.Result{
		key: {Code: 1, Stderr: "swapon: " + swapTestPath + ": No such file or directory\n"},
	})
	_, err = swapOnFn(c, value.MapOf("name", swapTestPath))
	if err == nil || !strings.Contains(err.Error(), "No such file or directory") {
		t.Errorf("swapOnFn did not surface the tool's own error: %v", err)
	}
}

// TestSwapOffSkipsWhenThePathIsNotActive exercises the branch that
// decides nothing needs to be run.
//
// The other branch -- an active path that swapoff actually turns off --
// is not reachable from a scripted test: `activeSwaps` reads
// /proc/swaps directly from the filesystem where a host has one, ahead
// of anything `RecordingRunner` can answer for, so a fixture here would
// only be exercising the fixture. `TestLiveSwapOnAndOffRoundTripARealDevice`
// in live_swap_test.go is where that branch is proven, against a real
// memory-backed device this module puts into the active table itself.
func TestSwapOffSkipsWhenThePathIsNotActive(t *testing.T) {
	goos := runtime.GOOS
	if goos != "linux" && goos != "freebsd" {
		t.Skipf("this build's swapOffArgv does not know %s", goos)
	}
	c := swapTestCtx(nil)
	out, err := swapOffFn(c, value.MapOf("name", swapTestPath))
	if err != nil {
		t.Fatalf("swapOffFn: %v", err)
	}
	m := out.(*value.Map)
	if changed, _ := m.GetString("changed"); changed != false {
		t.Errorf("disabling a swap path that was never active reported a change: %v", out)
	}
}

func TestSwapOffRejectsAnEmptyName(t *testing.T) {
	c := swapTestCtx(nil)
	if _, err := swapOffFn(c, value.NewMap(0)); err == nil {
		t.Error("an empty name was accepted")
	}
}

func TestSwapOffRefusesWithoutTheTool(t *testing.T) {
	c := &exec.Context{Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	if _, err := swapOffFn(c, value.MapOf("name", swapTestPath)); err == nil || !strings.Contains(err.Error(), "swapoff") {
		t.Errorf("swapOffFn without swapoff on the node = %v, want an error naming swapoff", err)
	}
}

func TestSwapRefusesOnAPlatformItIsNotDeclaredFor(t *testing.T) {
	if runtime.GOOS == "linux" || runtime.GOOS == "freebsd" {
		t.Skip("this is about the platforms swap is not declared for")
	}
	_, err := New().Exec.Call(&exec.Context{}, "swap.list", value.NewMap(0))
	if err == nil || !strings.Contains(err.Error(), "this node is "+runtime.GOOS) {
		t.Errorf("swap.list did not refuse by platform: %v", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
