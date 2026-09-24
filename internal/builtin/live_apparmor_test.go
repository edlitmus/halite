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
	// AppArmor is the LSM Debian, Ubuntu and SUSE ship. RHEL ships
	// SELinux instead and has no /sys/module/apparmor at all, which is a
	// different machine rather than a broken one.
	_, err := os.Stat(AppArmorEnabledPath)
	requireToolOfFamilies(t, "AppArmor ("+AppArmorEnabledPath+")", err == nil, "Debian", "Suse")

	// Present is not enabled, and that distinction is a real machine
	// state rather than a broken one. **openSUSE Leap 16 builds AppArmor
	// into its kernel and does not enable it** -- it defaults to SELinux
	// -- so the file above exists, says `N`, and every test past this
	// point was failing against a module that had correctly reported
	// "built into this kernel and not enabled".
	//
	// There is nothing to drive on such a machine, so this skips, and
	// says which node it skipped on. It is not narrowed to SUSE: a
	// Debian node with AppArmor switched off is the same situation, and
	// a skip naming an Ubuntu machine is itself worth reading.
	enabled, err := os.ReadFile(AppArmorEnabledPath)
	if err != nil || strings.TrimSpace(string(enabled)) != "Y" {
		t.Skipf("AppArmor is built into this %s kernel and not enabled (%s reads %q); "+
			"there is nothing here to drive",
			liveOSName(t), AppArmorEnabledPath, strings.TrimSpace(string(enabled)))
	}

	if c.Which("apparmor_parser") == "" {
		t.Fatal("apparmor_parser is not installed; it is in the `apparmor` package")
	}
	return c
}

// requireModeChanges skips, once and loudly, when this machine's `aa-*`
// tools cannot run at all.
//
// # Why a whole check for this
//
// The `aa-*` tools are Python, and before doing anything they parse
// *every* profile under /etc/apparmor.d with their own parser — not
// with `apparmor_parser`. One profile that parser does not understand
// therefore breaks every mode change on the machine, whatever profile
// was asked about. DIVERGENCE 5.37 found the first such file, and 5.133
// found that one runner image had three.
//
// # It asks the module, not a copy of the module
//
// This used to carry its own probe, with its own list of error messages
// that meant "broken" — a second copy of `apparmorToolsUsable`, written
// separately and kept in step by hand. They were in step, and both were
// wrong in the same way on the day the runner image started failing on
// an Edge profile neither list named: the check waved the tests
// through, and three of them failed with an error about a browser. Two
// paths that must agree are best made one path, so this now asks the
// module's own probe, and a probe that is wrong shows up as a test that
// fails instead of a skip that was never needed.
func requireModeChanges(t *testing.T, c *exec.Context) {
	t.Helper()
	for _, tool := range []string{"aa-enforce", "aa-complain", "aa-disable"} {
		if c.Which(tool) == "" {
			t.Skipf("%s is not installed; the aa-* tools are in `apparmor-utils`, "+
				"which Ubuntu does not install by default", tool)
		}
	}
	if usable, why := apparmorToolsUsable(c); !usable {
		t.Skipf("%s\n"+
			"That is a fault in this node's profile tree or in apparmor-utils rather "+
			"than in halite or in the profile asked about. DIVERGENCE 5.37 and 5.133 "+
			"record what was found. On the fleet leg this cannot happen quietly: the "+
			"workflow step that prepares the tree runs the same probe and fails the job "+
			"if the tools still cannot read it.", why)
	}
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
	// `tools` is not "is the binary installed" -- it is "can a mode be
	// changed here", and on Ubuntu 24.04 those differ. So it is checked
	// against an actual invocation rather than against an expectation.
	tools, _ := status.Get("tools")
	usable, why := apparmorToolsUsable(c)
	if tools != usable {
		t.Errorf("status reports tools=%v and running one says %v (%s)", tools, usable, why)
	}
	if usable {
		if _, present := status.Get("tools_reason"); present {
			t.Error("a node whose tools work carries a reason it does not need")
		}
	} else {
		reason, _ := status.Get("tools_reason")
		if text, _ := reason.(string); strings.TrimSpace(text) == "" {
			t.Error("tools=false with no reason; an operator cannot act on that")
		} else {
			t.Logf("mode changes are not possible here: %s", text)
		}
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
	requireModeChanges(t, c)
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
	requireModeChanges(t, c)
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
	requireModeChanges(t, c)
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

	// The execution functions do not read securityfs first, so they are
	// the ones exposed to the tools exiting 0 for a name they cannot
	// find. DIVERGENCE 5.133.
	for _, fn := range []string{"enforce", "complain", "disable"} {
		if _, err := r.Exec.Call(c, "apparmor."+fn, value.MapOf("name", "halite-no-such-profile")); err == nil {
			t.Errorf("apparmor.%s reported success for a profile this machine does not have", fn)
		} else if !strings.Contains(err.Error(), "nothing was changed") {
			t.Errorf("apparmor.%s failed, but not by saying nothing changed: %v", fn, err)
		}
	}
}

// **`apparmor.status` sees a tree the tools cannot read**, on a real
// machine, by building one.
//
// Everything `apparmorToolsUsable` knows about a broken tree came from
// broken trees somebody else shipped, and those change under it: 5.37's
// was `passt`, 5.133's was Edge and Firefox. This makes its own, the
// smallest one the runner showed the tools refuse -- two profiles, in two
// files, attached to one path that does not exist -- and checks that the
// module says `tools: false`, names both files, and that a mode change
// blames the tree rather than the profile it was asked about. Then it
// removes them and checks the answer goes back to true, so the test is
// about the conflict and not about some other fault on the machine.
//
// Neither profile is loaded: the conflict is between files on disk,
// which is all the Python parser reads, and loading them would attach
// two profiles to a path in the kernel for no reason.
func TestLiveAppArmorStatusSeesATreeTheToolsCannotRead(t *testing.T) {
	c := apparmorLive(t)
	requireModeChanges(t, c)
	r := New()

	var files []string
	for _, name := range []string{"halite-live-dup-a", "halite-live-dup-b"} {
		path := filepath.Join(liveAppArmorDir, name)
		body := "profile " + name + " /halite/live/dup/target {\n  file,\n}\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
		files = append(files, path)
	}
	removed := false
	remove := func() {
		for _, f := range files {
			_ = os.Remove(f)
		}
		removed = true
	}
	t.Cleanup(func() {
		if !removed {
			remove()
		}
	})

	out, err := r.Exec.Call(c, "apparmor.status", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	status, _ := out.(*value.Map)
	if tools, _ := status.Get("tools"); tools != false {
		t.Errorf("status reports tools=%v with two profiles attached to one path; "+
			"every aa-* call fails on this tree", tools)
	}
	reason, _ := status.Get("tools_reason")
	text, _ := reason.(string)
	for _, f := range files {
		if !strings.Contains(text, f) {
			t.Errorf("tools_reason does not name %s: %q", f, text)
		}
	}
	t.Logf("with the conflict in place: %s", text)

	// A mode change on a profile that has nothing to do with the
	// conflict. It fails -- the tools fail for every name -- and the
	// error has to say that it is not the profile's fault. `/usr/bin/man`
	// is used because it is there on every Ubuntu and the call cannot
	// succeed, so nothing about it is changed.
	_, err = r.Exec.Call(c, "apparmor.complain", value.MapOf("name", "/usr/bin/man"))
	if err == nil {
		t.Fatal("aa-complain succeeded on a tree it cannot parse; if the conflict built " +
			"here is no longer one this apparmor-utils rejects, this test needs a new one")
	}
	if !strings.Contains(err.Error(), "not this profile") {
		t.Errorf("the failure blames the profile rather than the tree: %v", err)
	}

	remove()
	if usable, why := apparmorToolsUsable(c); !usable {
		t.Errorf("with the conflict removed the tools still cannot run, so the answer "+
			"above was not about the conflict: %s", why)
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
