package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `apparmor`, driven against a running AppArmor.
//
// # What was assumed until now
//
// Every fixture in `apparmor_test.go` was written from documentation:
// what securityfs prints, what `aa-enforce` does, what a disabled
// profile looks like. The module's own evidence note said so. Three
// things in particular were guesses, and each is the kind that produces
// a module which reports success and changes nothing:
//
//   - that securityfs's two columns are `name (mode)` and parse the way
//     `parseAppArmorProfiles` reads them;
//   - that `aa-enforce` and `aa-complain` accept a *profile name* rather
//     than the file it lives in, which is what the module passes;
//   - that a disabled profile has no mode at all rather than a mode
//     called `disable` — the distinction this module is most careful
//     about, and the one nothing had checked.
//
// # It brings its own profile
//
// Nothing here touches a profile the machine came with. Changing the
// mode of a real one would confine or unconfine something the runner
// depends on, and a test that can break the machine it runs on is one
// nobody runs on a machine they care about.
//
// So it writes its own into /etc/apparmor.d — which is where it has to
// be, because the `aa-*` tools look profiles up by name in that
// directory — loads it, moves it through every mode, and removes it.
// The profile confines nothing: it is attached to no executable and
// grants nothing.
//
// # Where it runs
//
// The same place as `hostname` and `sysctl`: a GitHub runner, which is a
// fresh virtual machine per job with AppArmor in its kernel, destroyed
// minutes later. Not a container — a container has no securityfs of its
// own and loading a profile inside one would be loading it into the
// host's kernel.

// liveAppArmorProfile is the name and the file. The name is deliberately
// not a path: a profile named after an executable would be attached to
// it, and this one must confine nothing.
const (
	liveAppArmorProfile = "halite-live-probe"
	liveAppArmorDir     = "/etc/apparmor.d"
)

// The smallest profile that loads.
//
// `file,` grants access to every file, which sounds alarming and is not:
// the profile is attached to no executable, so nothing ever runs under
// it. A profile that granted nothing would be equally unused and would
// risk `apparmor_parser` rejecting it as empty on some versions.
const liveAppArmorBody = `profile halite-live-probe {
  file,
}
`

// apparmorLive sets the profile up and tears it down, or fails saying
// what this machine is missing.
//
// It fails rather than skips, for the reason the other live helpers do:
// `HALITE_SYSTEM_LIVE=1` is a statement that this machine is available,
// and a test that quietly passed by finding no AppArmor would be the
// third instance of the defect this file exists to prevent.
func apparmorLive(t *testing.T) *exec.Context {
	t.Helper()
	c := system(t)
	if runtime.GOOS != "linux" {
		t.Skipf("AppArmor is a Linux LSM and this is %s", runtime.GOOS)
	}
	if _, err := os.Stat(AppArmorEnabledPath); err != nil {
		t.Fatalf("this machine has no AppArmor (%s: %v); HALITE_SYSTEM_LIVE says it is available", AppArmorEnabledPath, err)
	}
	for _, tool := range []string{"apparmor_parser", "aa-enforce", "aa-complain", "aa-disable"} {
		if c.Which(tool) == "" {
			t.Fatalf("%s is not installed; apparmor_parser is in `apparmor` and the aa-* tools are in `apparmor-utils`", tool)
		}
	}
	requireWorkingAppArmorTools(t, c)
	return c
}

// requireWorkingAppArmorTools fails early, once, when this machine's
// `aa-*` tools cannot run at all.
//
// # Why a whole check for this
//
// The `aa-*` tools are Python, and before doing anything they parse
// *every* profile under /etc/apparmor.d with their own parser — not
// with `apparmor_parser`. One profile that parser does not understand
// therefore breaks every mode change on the machine, whatever profile
// was asked about.
//
// That is not hypothetical. Ubuntu 24.04's own apparmor-utils 4.0.1
// cannot parse `abstractions/passt`, shipped by Ubuntu's own `passt`
// package, and on a machine with it installed `aa-enforce`,
// `aa-complain` and `aa-disable` all fail — including on stock profiles
// like /usr/bin/man. DIVERGENCE 5.37.
//
// Without this check, three tests fail with the same confusing message
// and none of them says that the fault is neither halite's nor the
// profile's.
func requireWorkingAppArmorTools(t *testing.T, c *exec.Context) {
	t.Helper()
	res, err := c.Run(exec.Command{
		// `--help` does not parse the tree; a real invocation against a
		// profile that does not exist does, and fails at the parse
		// before it gets as far as not finding it.
		Argv:           []string{"aa-complain", "halite-no-such-profile-probe"},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("running aa-complain at all: %v", err)
	}
	out := res.Stderr + res.Stdout
	if !strings.Contains(out, "cannot have a source") && !strings.Contains(out, "Traceback") {
		return
	}
	var offenders []string
	if entries, err := os.ReadDir(filepath.Join(liveAppArmorDir, "abstractions")); err == nil {
		for _, e := range entries {
			p := filepath.Join(liveAppArmorDir, "abstractions", e.Name())
			if b, err := os.ReadFile(p); err == nil && strings.Contains(string(b), "runbindable") {
				offenders = append(offenders, p)
			}
		}
	}
	t.Fatalf("this machine's aa-* tools cannot parse its own profile tree, so no mode "+
		"change can be made on it by any means:\n  %s\n"+
		"Profiles using syntax they reject: %v\n"+
		"That is a defect in apparmor-utils rather than in halite or in the profile "+
		"asked about — it fails the same way on /usr/bin/man. DIVERGENCE 5.37 records "+
		"it; the workflow moves the offending abstraction aside so that halite's own "+
		"behaviour can be established separately.",
		strings.TrimSpace(firstLine(out)), offenders)
}

// writeLiveProfile puts the profile on disk and removes every trace of
// it afterwards, whatever the test did to it.
func writeLiveProfile(t *testing.T, c *exec.Context) string {
	t.Helper()
	path := filepath.Join(liveAppArmorDir, liveAppArmorProfile)
	if err := os.WriteFile(path, []byte(liveAppArmorBody), 0o644); err != nil {
		t.Fatalf("writing the test profile: %v", err)
	}
	t.Cleanup(func() {
		// Unload it, whether or not the test managed to. `--remove`
		// against a profile that is not loaded is harmless.
		_, _ = c.Run(exec.Command{
			Argv:           []string{"apparmor_parser", "--remove", path},
			IgnoreExitCode: true,
		})
		// aa-disable leaves a symlink behind, which is the whole point
		// of it, and leaving one on the machine would keep the profile
		// unloaded across a boot that never comes.
		_ = os.Remove(filepath.Join(liveAppArmorDir, "disable", liveAppArmorProfile))
		_ = os.Remove(path)
		if profiles, err := apparmorProfiles(); err == nil {
			if mode, still := profiles[liveAppArmorProfile]; still {
				t.Errorf("the test profile is still loaded in %s mode", mode)
			}
		}
	})
	return path
}

// **securityfs really parses**, and the count agrees with the machine.
//
// `parseAppArmorProfiles` reads two columns out of a file this build had
// never opened. What it prints on a real Ubuntu is the question, and a
// fixture written from the documented format is precisely the thing 5.31
// established is worth nothing.
func TestLiveAppArmorReadsWhatSecurityfsPrints(t *testing.T) {
	c := apparmorLive(t)
	r := New()

	out, err := r.Exec.Call(c, "apparmor.status", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	status, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("status returned %T", out)
	}
	if enabled, _ := status.Get("enabled"); enabled != true {
		t.Fatalf("AppArmor is not enabled on this machine: %v", status)
	}
	if readable, _ := status.Get("profiles_readable"); readable != true {
		t.Fatalf("securityfs is not readable as root: %v", status)
	}
	if tools, _ := status.Get("tools"); tools != true {
		t.Errorf("status reports the aa-* tools absent and this test found them")
	}
	count, _ := status.Get("profiles")
	n, _ := count.(int)
	if n < 1 {
		t.Fatalf("a stock Ubuntu has profiles loaded and this reports %v: %v", count, status)
	}
	t.Logf("%d profiles loaded; modes %v", n, mustGetAny(status, "modes"))

	// The listing and the count come from the same read, so they have to
	// agree. They would not if the parser dropped a line it did not
	// recognise -- which is exactly how a securityfs format this build
	// has never seen would fail.
	listed, err := r.Exec.Call(c, "apparmor.list_profiles", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	profiles, ok := listed.(*value.Map)
	if !ok {
		t.Fatalf("list_profiles returned %T", listed)
	}
	if profiles.Len() != n {
		t.Errorf("status counts %d profiles and list_profiles returns %d; a line was dropped", n, profiles.Len())
	}

	// Every mode the parser produced is one the module knows. A parse
	// that took the wrong column would put a profile *name* here.
	for _, key := range profiles.Keys() {
		name, _ := key.(string)
		v, _ := profiles.Get(name)
		mode, _ := v.(string)
		if !apparmorModes[mode] {
			t.Errorf("profile %q is in mode %q, which is not one AppArmor has; "+
				"securityfs was parsed wrongly", name, mode)
		}
	}

	// And against the file, not through the module: the number of lines
	// is the number of profiles.
	raw, err := os.ReadFile(AppArmorProfilesPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	if lines != n {
		t.Errorf("%s has %d non-empty lines and the module found %d profiles",
			AppArmorProfilesPath, lines, n)
	}
}

// **A profile really loaded, moved through every mode, and unloaded.**
//
// This is the mutating half and the reason the module needed a machine.
// Each step is checked against securityfs rather than against what the
// tool printed, because a tool that succeeded and changed nothing is the
// failure being looked for.
func TestLiveAppArmorMovesAProfileThroughEveryMode(t *testing.T) {
	c := apparmorLive(t)
	r := New()
	path := writeLiveProfile(t, c)

	// Loaded through the module, which is `apparmor_parser --replace`.
	if _, err := r.Exec.Call(c, "apparmor.reload", value.MapOf("path", path)); err != nil {
		t.Fatalf("apparmor.reload: %v", err)
	}

	// A freshly parsed profile is in enforce mode: the file names no
	// flags, and enforce is what AppArmor does with that.
	if mode := liveMode(t, c, r); mode != "enforce" {
		t.Fatalf("a newly loaded profile is in %q mode, not enforce", mode)
	}

	// complain, then back to enforce. Both through the aa-* tools, and
	// both by *profile name* -- which is what the module passes and what
	// had never been checked against the real tools.
	//
	// Each tool is reported separately rather than the first failure
	// ending the test, because "which of the three works here" is the
	// answer wanted and one Fatal hides the other two.
	if _, err := r.Exec.Call(c, "apparmor.complain", value.MapOf("name", liveAppArmorProfile)); err != nil {
		t.Errorf("apparmor.complain: %v", err)
	} else if mode := liveMode(t, c, r); mode != "complain" {
		t.Errorf("after apparmor.complain the profile is in %q mode", mode)
	}
	if _, err := r.Exec.Call(c, "apparmor.enforce", value.MapOf("name", liveAppArmorProfile)); err != nil {
		t.Errorf("apparmor.enforce: %v", err)
	} else if mode := liveMode(t, c, r); mode != "enforce" {
		t.Errorf("after apparmor.enforce the profile is in %q mode", mode)
	}

	// **A disabled profile has no mode**, which is the distinction this
	// module is most careful about and which nothing had checked. It is
	// unloaded, so it is absent from securityfs entirely rather than
	// present with a mode called `disable`.
	if _, err := r.Exec.Call(c, "apparmor.disable", value.MapOf("name", liveAppArmorProfile)); err != nil {
		t.Fatalf("apparmor.disable: %v", err)
	}
	if mode := liveMode(t, c, r); mode != "" {
		t.Errorf("a disabled profile reports mode %q; disabling unloads it, so it should "+
			"have no mode at all", mode)
	}
	listed, err := r.Exec.Call(c, "apparmor.list_profiles", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	if profiles, _ := listed.(*value.Map); profiles != nil {
		if _, still := profiles.Get(liveAppArmorProfile); still {
			t.Error("a disabled profile is still in the listing")
		}
	}

	// And it stays disabled across a boot, which is what `aa-disable`
	// does that unloading alone does not: a symlink in the disable
	// directory.
	link := filepath.Join(liveAppArmorDir, "disable", liveAppArmorProfile)
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("aa-disable left no symlink in %s, so this profile would come back "+
			"at the next boot: %v", filepath.Dir(link), err)
	}
}

// The state converges, and predicts in test mode without touching the
// kernel.
//
// `apparmor.mode` is the state an estate actually writes, and a state
// that reports a change on every run is one an operator stops reading.
func TestLiveAppArmorStateConvergesAndPredicts(t *testing.T) {
	c := apparmorLive(t)
	r := New()
	path := writeLiveProfile(t, c)

	if _, err := r.Exec.Call(c, "apparmor.reload", value.MapOf("path", path)); err != nil {
		t.Fatalf("apparmor.reload: %v", err)
	}
	if mode := liveMode(t, c, r); mode != "enforce" {
		t.Fatalf("setup left the profile in %q mode", mode)
	}

	args := value.MapOf("name", liveAppArmorProfile, "mode", "complain")

	// Test mode predicts and changes nothing. On a module that decides
	// whether a violation is denied or merely logged, that promise is
	// the one an operator leans on hardest.
	predicted, err := r.States.Call(testCtx(t), "apparmor.mode", args)
	if err != nil {
		t.Fatal(err)
	}
	if predicted.Result != nil {
		t.Errorf("test mode decided rather than predicted: %+v", predicted)
	}
	if predicted.Changes.Len() == 0 {
		t.Error("test mode predicted no change for a mode it would have altered")
	}
	if mode := liveMode(t, c, r); mode != "enforce" {
		t.Fatalf("test mode changed the kernel: the profile is now in %q mode", mode)
	}

	// Then for real, and then again.
	applied, err := r.States.Call(c, "apparmor.mode", args)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Succeeded() {
		t.Fatalf("the state failed: %s", applied.Comment)
	}
	if mode := liveMode(t, c, r); mode != "complain" {
		t.Fatalf("the state reported success and the profile is in %q mode", mode)
	}
	again, err := r.States.Call(c, "apparmor.mode", args)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changes.Len() != 0 {
		t.Errorf("a second run reported changes; this state does not converge: %+v", again.Changes)
	}
	if applied.Changes.Len() == 0 {
		t.Error("the first run reported no change for a mode it moved")
	}
}

// A profile this machine does not have is refused, rather than reported
// as changed.
func TestLiveAppArmorRefusesAProfileThatIsNotThere(t *testing.T) {
	c := apparmorLive(t)
	r := New()

	out, err := r.States.Call(c, "apparmor.mode", value.MapOf(
		"name", "halite-no-such-profile", "mode", "enforce"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Succeeded() {
		t.Errorf("a profile this machine does not have was accepted: %+v", out)
	}
	if !strings.Contains(out.Comment, "halite-no-such-profile") {
		t.Errorf("the refusal does not name the profile: %q", out.Comment)
	}
}

// liveMode reads the test profile's mode through the module.
func liveMode(t *testing.T, c *exec.Context, r *Registries) string {
	t.Helper()
	out, err := r.Exec.Call(c, "apparmor.mode", value.MapOf("name", liveAppArmorProfile))
	if err != nil {
		t.Fatalf("apparmor.mode: %v", err)
	}
	mode, _ := out.(string)
	return mode
}

func mustGetAny(m *value.Map, key string) any {
	v, _ := m.Get(key)
	return v
}
