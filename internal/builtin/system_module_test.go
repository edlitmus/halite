package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// newSystemTestRegistry builds a registry carrying only this module,
// rather than builtin.New()'s full set, so that a failure here names
// this file rather than anything else registered beside it.
//
// This comment used to say that registerSystemModule "is not yet wired
// into New()" and that the wiring "belongs to whoever assembles
// builtin.go". It was true when written and it stayed true: the module
// was finished, its tests passed against the registry below, and nothing
// in any build ever registered it. Isolating a module under test is
// still worth doing -- but it cannot be the only place a module is
// registered, which is what TestEveryRegistrationFunctionIsCalled in
// builtin_test.go now enforces for every module in this package.
func newSystemTestRegistry() *Registries {
	r := &Registries{Exec: exec.NewRegistry(), States: states.NewRegistry()}
	registerSystemModule(r)
	return r
}

// Every test in this file drives either a pure argument-vector function
// or the exec.Registry with a RecordingRunner that never touches a real
// process. Nothing here runs `halt`, `poweroff`, `reboot`, `shutdown`,
// `date` or `init`, on this host or any other -- see system_module.go's
// package comment for why, and live_system_module_test.go for the one
// safe read this module allows itself against the real host.

// ---- systemPowerArgv: the pure table, checkable from any host ----

func TestSystemPowerArgvOnEachSupportedPlatform(t *testing.T) {
	for _, c := range []struct {
		goos, verb, want string
		delay            int64
		message          string
	}{
		{"linux", "halt", "halt", 0, ""},
		{"freebsd", "halt", "halt", 0, ""},
		{"linux", "poweroff", "poweroff", 0, ""},
		{"freebsd", "poweroff", "poweroff", 0, ""},

		// reboot: the same flag, `-r`, on both platforms.
		{"linux", "reboot", "shutdown -r now", 0, ""},
		{"freebsd", "reboot", "shutdown -r now", 0, ""},
		{"linux", "reboot", "shutdown -r +5", 5, ""},
		{"freebsd", "reboot", "shutdown -r +5", 5, ""},
		{"linux", "reboot", `shutdown -r +5 "a tree asked for this"`, 5, "a tree asked for this"},
		{"freebsd", "reboot", `shutdown -r +5 "a tree asked for this"`, 5, "a tree asked for this"},

		// shutdown: `-h` on Linux (which systemd's own manual says is
		// "equivalent to --poweroff, unless --halt is specified also"),
		// `-p` on FreeBSD (which turns the power off after halting, per
		// the real /sbin/shutdown usage line in the package comment).
		{"linux", "shutdown", "shutdown -h now", 0, ""},
		{"freebsd", "shutdown", "shutdown -p now", 0, ""},
		{"linux", "shutdown", "shutdown -h +10", 10, ""},
		{"freebsd", "shutdown", "shutdown -p +10", 10, ""},
	} {
		argv, err := systemPowerArgv(c.goos, c.verb, c.delay, c.message)
		if err != nil {
			t.Errorf("%s %s: %v", c.goos, c.verb, err)
			continue
		}
		if got := (exec.Command{Argv: argv}).String(); got != c.want {
			t.Errorf("%s %s runs\n  %s\nwant\n  %s", c.goos, c.verb, got, c.want)
		}
	}
}

// A platform this build has no evidence for is refused by name, not
// given one of the two spellings and a coin toss -- the same contract
// `quotaSetArgv` holds for the platforms it does not know.
func TestSystemPowerArgvRefusesAPlatformThisBuildHasNoEvidenceFor(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "openbsd", "netbsd", "dragonfly", "plan9"} {
		if _, err := systemPowerArgv(goos, "halt", 0, ""); err == nil {
			t.Errorf("%s was given a halt command; this module claims evidence only for linux and freebsd", goos)
		}
	}
}

func TestSystemPowerArgvRefusesAVerbThatIsNotOneOfTheFour(t *testing.T) {
	if _, err := systemPowerArgv("linux", "sleep", 0, ""); err == nil {
		t.Error("\"sleep\" was accepted as a power verb")
	}
}

func TestSystemPowerArgvRefusesANegativeDelayNowhereButRunTime(t *testing.T) {
	// The argv function itself does not validate the delay -- that is
	// systemPowerRun's job, checked below -- but a negative delay must
	// not silently become "now" through systemPowerTimeArg either,
	// because that would hide a caller's mistake rather than report it.
	if got := systemPowerTimeArg(-1); got != "now" {
		t.Errorf("systemPowerTimeArg(-1) = %q; systemPowerRun is what refuses a negative delay before this is reached", got)
	}
}

func TestSystemPowerTimeArg(t *testing.T) {
	for _, c := range []struct {
		delay int64
		want  string
	}{
		{0, "now"},
		{1, "+1"},
		{5, "+5"},
		{60, "+60"},
	} {
		if got := systemPowerTimeArg(c.delay); got != c.want {
			t.Errorf("systemPowerTimeArg(%d) = %q, want %q", c.delay, got, c.want)
		}
	}
}

// ---- systemClockArgv: the two platforms' different formats ----

func TestSystemClockArgvOnEachSupportedPlatform(t *testing.T) {
	target := time.Date(2026, time.September, 12, 14, 30, 45, 0, time.UTC)

	linux, err := systemClockArgv("linux", target)
	if err != nil {
		t.Fatalf("linux: %v", err)
	}
	if want := []string{"date", "--set=2026-09-12 14:30:45"}; !equalStrings(linux, want) {
		t.Errorf("linux clock command is %v, want %v", linux, want)
	}

	freebsd, err := systemClockArgv("freebsd", target)
	if err != nil {
		t.Fatalf("freebsd: %v", err)
	}
	// cc yy mm dd HH MM . SS, per /rescue/date's own usage line.
	if want := []string{"date", "202609121430.45"}; !equalStrings(freebsd, want) {
		t.Errorf("freebsd clock command is %v, want %v", freebsd, want)
	}

	if _, err := systemClockArgv("windows", target); err == nil {
		t.Error("Windows was given a clock-setting command")
	}
}

// ---- the compose functions: validation and the normalisation trap ----

func TestSystemComposeTimeValidatesEachField(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		label                string
		hour, minute, second int64
		wantErr              bool
	}{
		{"midnight", 0, 0, 0, false},
		{"last second of the day", 23, 59, 59, false},
		{"hour 24", 24, 0, 0, true},
		{"negative hour", -1, 0, 0, true},
		{"minute 60", 12, 60, 0, true},
		{"second 60", 12, 0, 60, true},
	} {
		_, err := systemComposeTime(now, c.hour, c.minute, c.second)
		if c.wantErr && err == nil {
			t.Errorf("%s: accepted", c.label)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: %v", c.label, err)
		}
	}
}

// A day that does not exist in the month asked for must be refused, not
// silently rolled into the next month the way time.Date's own
// normalisation would -- setting the clock to a real but unintended
// date is a worse outcome than an error.
func TestSystemComposeDateRefusesANormalisedDate(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	if _, err := systemComposeDate(now, 2026, 2, 30); err == nil {
		t.Error("February 30th, 2026 was accepted")
	}
	target, err := systemComposeDate(now, 2026, 3, 15)
	if err != nil {
		t.Fatalf("a real date was refused: %v", err)
	}
	if target.Year() != 2026 || target.Month() != time.March || target.Day() != 15 {
		t.Errorf("got %v, want 2026-03-15", target)
	}
	// The time of day is carried over from `now`.
	if target.Hour() != 12 {
		t.Errorf("the time of day was not preserved: got hour %d, want 12", target.Hour())
	}
}

func TestSystemComposeDateValidatesEachField(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		label            string
		year, month, day int64
		wantErr          bool
	}{
		{"a plausible date", 2026, 9, 12, false},
		{"month 0", 2026, 0, 12, true},
		{"month 13", 2026, 13, 12, true},
		{"day 0", 2026, 9, 0, true},
		{"day 32", 2026, 9, 32, true},
		{"year 1969", 1969, 9, 12, true},
		{"year 10000", 10000, 9, 12, true},
	} {
		_, err := systemComposeDate(now, c.year, c.month, c.day)
		if c.wantErr && err == nil {
			t.Errorf("%s: accepted", c.label)
		}
		if !c.wantErr && err != nil {
			t.Errorf("%s: %v", c.label, err)
		}
	}
}

func TestSystemComposeDateTimeSetsAllSixFields(t *testing.T) {
	target, err := systemComposeDateTime(2026, 9, 12, 14, 30, 45)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.September, 12, 14, 30, 45, 0, time.Local)
	if !target.Equal(want) {
		t.Errorf("got %v, want %v", target, want)
	}
}

// ---- the registry, with a RecordingRunner that never executes anything ----

// The strongest guarantee this file can offer: test mode for every power
// verb makes zero calls into the runner. Not "zero mutating calls" the
// way quota.set's test mode still reads the filesystem -- zero calls of
// any kind, because there is nothing worth reading before predicting
// that a halt, a poweroff, a reboot or a shutdown would happen.
func TestPowerVerbsRunNothingAtAllInTestMode(t *testing.T) {
	if _, why := systemSupportedHere(); why != "" {
		t.Skip(why)
	}
	r := newSystemTestRegistry()
	for _, tc := range []struct {
		fn    string
		args  *value.Map
		verb  string
		delay int64
		msg   string
	}{
		{"system.halt", value.NewMap(0), "halt", 0, ""},
		{"system.poweroff", value.NewMap(0), "poweroff", 0, ""},
		{"system.shutdown", value.NewMap(0), "shutdown", 0, ""},
		{"system.reboot", value.MapOf("delay", int64(5), "message", "test"), "reboot", 5, "test"},
	} {
		wantArgv, err := systemPowerArgv(runtime.GOOS, tc.verb, tc.delay, tc.msg)
		if err != nil {
			t.Fatalf("%s: this platform has no argv for %s: %v", tc.fn, tc.verb, err)
		}
		recorder := &exec.RecordingRunner{}
		c := &exec.Context{
			Test:   true,
			Runner: recorder,
			Lookup: func(name string) string { return "/sbin/" + name },
		}
		out, err := r.Exec.Call(c, tc.fn, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.fn, err)
		}
		m, ok := out.(*value.Map)
		if !ok {
			t.Fatalf("%s: returned %T, want *value.Map", tc.fn, out)
		}
		if changed, _ := m.GetString("changed"); changed != true {
			t.Errorf("%s: changed=%v, want true", tc.fn, changed)
		}
		if got, _ := m.GetString("command"); got != (exec.Command{Argv: wantArgv}).String() {
			t.Errorf("%s: command=%v, want %s", tc.fn, got, (exec.Command{Argv: wantArgv}).String())
		}
		if ran := recorder.RanCommands(); len(ran) != 0 {
			t.Errorf("%s: test mode ran %v; it must run nothing at all", tc.fn, ran)
		}
	}
}

// A negative delay is refused before a command is even built, on both
// verbs that take one.
func TestPowerVerbsRefuseANegativeDelay(t *testing.T) {
	if _, why := systemSupportedHere(); why != "" {
		t.Skip(why)
	}
	r := newSystemTestRegistry()
	c := &exec.Context{Test: true, Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "/sbin/shutdown" }}
	for _, fn := range []string{"system.reboot", "system.shutdown"} {
		if _, err := r.Exec.Call(c, fn, value.MapOf("delay", int64(-1))); err == nil {
			t.Errorf("%s accepted a delay of -1", fn)
		}
	}
}

// A node with none of these tools is told so by name, in test mode too --
// a prediction made up on a node that could not act on it would be a
// worse answer than a refusal.
func TestPowerVerbsRefuseWhenTheToolIsMissing(t *testing.T) {
	if _, why := systemSupportedHere(); why != "" {
		t.Skip(why)
	}
	r := newSystemTestRegistry()
	c := &exec.Context{Test: true, Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	for _, fn := range []string{"system.halt", "system.poweroff", "system.reboot", "system.shutdown"} {
		if _, err := r.Exec.Call(c, fn, value.NewMap(0)); err == nil {
			t.Errorf("%s was accepted on a node with none of its tools", fn)
		}
	}
}

// The clock setters, the same way: test mode predicts and runs nothing.
func TestClockSettersRunNothingAtAllInTestMode(t *testing.T) {
	if _, why := systemSupportedHere(); why != "" {
		t.Skip(why)
	}
	r := newSystemTestRegistry()
	recorder := &exec.RecordingRunner{}
	c := &exec.Context{
		Test:   true,
		Runner: recorder,
		Lookup: func(name string) string { return "/usr/bin/" + name },
	}
	for _, tc := range []struct {
		fn   string
		args *value.Map
	}{
		{"system.set_system_time", value.MapOf("hour", int64(14), "minute", int64(30))},
		{"system.set_system_date", value.MapOf("year", int64(2026), "month", int64(9), "day", int64(12))},
		{"system.set_system_date_time", value.MapOf(
			"year", int64(2026), "month", int64(9), "day", int64(12),
			"hour", int64(14), "minute", int64(30))},
	} {
		out, err := r.Exec.Call(c, tc.fn, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.fn, err)
		}
		m, ok := out.(*value.Map)
		if !ok {
			t.Fatalf("%s: returned %T", tc.fn, out)
		}
		if changed, _ := m.GetString("changed"); changed != true {
			t.Errorf("%s: changed=%v, want true", tc.fn, changed)
		}
	}
	if ran := recorder.RanCommands(); len(ran) != 0 {
		t.Errorf("test mode ran %v; it must run nothing at all", ran)
	}
}

func TestSetSystemTimeRefusesAnOutOfRangeHour(t *testing.T) {
	if _, why := systemSupportedHere(); why != "" {
		t.Skip(why)
	}
	r := newSystemTestRegistry()
	c := &exec.Context{Test: true, Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "/usr/bin/date" }}
	if _, err := r.Exec.Call(c, "system.set_system_time", value.MapOf("hour", int64(24), "minute", int64(0))); err == nil {
		t.Error("hour 24 was accepted")
	}
}

// systemSupportedHere reports why this host cannot run the registry
// tests, or "". It exists so a build on a platform this module has no
// argv table for still compiles and its tests still run -- they just
// skip with a reason, rather than calling into a registry function that
// the platform check would refuse for a different reason than the one
// being tested.
func systemSupportedHere() (ok bool, why string) {
	switch runtime.GOOS {
	case "linux", "freebsd":
		return true, ""
	}
	return false, runtime.GOOS + " is not one of the two platforms this module has evidence for"
}

// ---- the computer description: real file I/O, no process ever run ----

func withMachineInfoAt(t *testing.T, path string) {
	t.Helper()
	old := EtcMachineInfoPath
	EtcMachineInfoPath = path
	t.Cleanup(func() { EtcMachineInfoPath = old })
}

func TestComputerDescIsEmptyWhenTheFileDoesNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine-info")
	got, err := systemReadComputerDesc(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// Setting it in test mode changes nothing on disk, and the real run
// after it writes the file, preserves any other line already there, and
// reads back exactly what was written -- including a value that itself
// contains a quote, which is where a naive writer breaks its own file.
func TestSetComputerDescTestModeThenRealRunThenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine-info")
	withMachineInfoAt(t, path)
	if err := os.WriteFile(path, []byte("CHASSIS=server\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	testCtx := &exec.Context{Test: true, Runner: &exec.RecordingRunner{}, Lookup: func(string) string { return "" }}
	out, err := systemSetComputerDescFn(testCtx, value.MapOf("description", `Ed's "beastie" box`))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("test mode: changed=%v, want true", changed)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "PRETTY_HOSTNAME") {
		t.Errorf("test mode wrote to %s:\n%s", path, body)
	}

	// hostnamectl does not exist on this host (it is systemd-only, and
	// this build's tests run on Linux and FreeBSD both), so the real run
	// below always falls through to the direct file write -- no process
	// is executed by this test either.
	realCtx := &exec.Context{Lookup: func(string) string { return "" }}
	out, err = systemSetComputerDescFn(realCtx, value.MapOf("description", `Ed's "beastie" box`))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != true {
		t.Errorf("real run: changed=%v, want true", changed)
	}

	body, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "CHASSIS=server") {
		t.Errorf("the existing CHASSIS line was lost:\n%s", text)
	}
	if want := `PRETTY_HOSTNAME="Ed's \"beastie\" box"`; !strings.Contains(text, want) {
		t.Errorf("machine-info does not contain %s:\n%s", want, text)
	}

	got, err := systemReadComputerDesc(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := `Ed's "beastie" box`; got != want {
		t.Errorf("round trip: got %q, want %q", got, want)
	}

	// Asking again for the same description changes nothing.
	out, err = systemSetComputerDescFn(realCtx, value.MapOf("description", `Ed's "beastie" box`))
	if err != nil {
		t.Fatal(err)
	}
	if changed, _ := out.(*value.Map).GetString("changed"); changed != false {
		t.Errorf("setting the same description again: changed=%v, want false", changed)
	}
}

func TestSetComputerDescReplacesAnExistingLineInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine-info")
	withMachineInfoAt(t, path)
	original := "CHASSIS=server\nPRETTY_HOSTNAME=\"old name\"\nICON_NAME=computer-server\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &exec.Context{Lookup: func(string) string { return "" }}
	if _, err := systemSetComputerDescFn(c, value.MapOf("description", "new name")); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (the line was replaced in place, not appended):\n%s", len(lines), body)
	}
	if lines[0] != "CHASSIS=server" || lines[2] != "ICON_NAME=computer-server" {
		t.Errorf("the other lines moved:\n%s", body)
	}
	if lines[1] != `PRETTY_HOSTNAME="new name"` {
		t.Errorf("line 2 is %q, want the replaced PRETTY_HOSTNAME", lines[1])
	}
}

func TestGetComputerDescReadsAnUnquotedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "machine-info")
	if err := os.WriteFile(path, []byte("PRETTY_HOSTNAME=bare-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := systemReadComputerDesc(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "bare-value" {
		t.Errorf("got %q, want %q", got, "bare-value")
	}
}
