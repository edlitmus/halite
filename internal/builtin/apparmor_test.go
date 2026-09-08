package builtin

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// These run from any host. AppArmor is a Linux LSM and the module is
// declared for Linux, but everything it decides is decided from two
// files and one tool lookup, and all three can be supplied — which is
// the arrangement DIVERGENCE 4.10 and the platform audit before it both
// argued for: a branch that can only be reached on one platform is a
// branch nobody checks.
//
// What cannot be checked from here is whether `aa-enforce` does what
// this expects on a real node. That needs a Linux runner, which CI has.

// apparmorFixture points the two sysfs paths at files a test writes,
// and returns the context and a way to set what is loaded.
func apparmorFixture(t *testing.T, enabled string, profiles string) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	dir := t.TempDir()

	enabledPath := filepath.Join(dir, "enabled")
	profilesPath := filepath.Join(dir, "profiles")
	if enabled != "" {
		writeFile(t, enabledPath, enabled)
	}
	if profiles != "" {
		writeFile(t, profilesPath, profiles)
	}

	oldEnabled, oldProfiles := AppArmorEnabledPath, AppArmorProfilesPath
	AppArmorEnabledPath, AppArmorProfilesPath = enabledPath, profilesPath
	t.Cleanup(func() { AppArmorEnabledPath, AppArmorProfilesPath = oldEnabled, oldProfiles })

	runner := &exec.RecordingRunner{Responses: map[string]exec.Result{}}
	c := newCtx(false)
	c.Runner = runner
	// apparmor-utils is present unless a test says otherwise.
	c.Lookup = func(name string) string {
		if strings.HasPrefix(name, "aa-") || name == "apparmor_parser" {
			return "/usr/sbin/" + name
		}
		return ""
	}
	return c, runner
}

// The securityfs list is read the way its own format works, not the way
// it usually looks.
//
// The mode comes from the last parenthesised group rather than from the
// first space, and that is the whole of the difference: a profile name
// may contain spaces, and splitting on the first one drops the profile
// entirely rather than reporting it wrong — so a state naming it would
// be told it is not loaded while it is enforcing.
func TestTheLoadedProfileListIsReadByItsFormat(t *testing.T) {
	got := parseAppArmorProfiles(strings.Join([]string{
		"/usr/bin/man (enforce)",
		"man_filter (enforce)",
		"man_groff (enforce)",
		"/usr/sbin/tcpdump (complain)",
		"/usr/lib/snapd/snap-confine//mount-namespace-capture-helper (enforce)",
		"lsb_release (unconfined)",
		"some profile with spaces (kill)",
		"",
		"   ",
		"a line with no mode at all",
	}, "\n"))

	for name, want := range map[string]string{
		"/usr/bin/man":             "enforce",
		"man_filter":               "enforce",
		"man_groff":                "enforce",
		"/usr/sbin/tcpdump":        "complain",
		"lsb_release":              "unconfined",
		"some profile with spaces": "kill",
		"/usr/lib/snapd/snap-confine//mount-namespace-capture-helper": "enforce",
	} {
		if got[name] != want {
			t.Errorf("%q read as %q, want %q", name, got[name], want)
		}
	}
	if len(got) != 7 {
		t.Errorf("read %d profiles, want 7: %v", len(got), got)
	}
	// Three files hold these seven profiles, and one of those files
	// holds three of them. That is the reason nothing in this module
	// identifies a profile by its file.
	if _, ok := got["usr.bin.man"]; ok {
		t.Error("a file name was read as a profile name")
	}
}

// A kernel with AppArmor built in but switched off is not the same as a
// kernel without it, and the reason says which.
func TestWhyAppArmorIsOffSaysWhichKindOfOff(t *testing.T) {
	// Switched off.
	c, _ := apparmorFixture(t, "N\n", "")
	st, err := apparmorStatus(c)
	if err != nil {
		t.Fatal(err)
	}
	if on, _ := st.Get("enabled"); on != false {
		t.Errorf("a kernel parameter of N read as enabled: %+v", st)
	}
	reason, _ := st.Get("reason")
	if !strings.Contains(reason.(string), "not enabled") {
		t.Errorf("the reason does not say it is switched off: %v", reason)
	}

	// Not present at all: the file does not exist.
	c2, _ := apparmorFixture(t, "", "")
	st2, err := apparmorStatus(c2)
	if err != nil {
		t.Fatal(err)
	}
	reason2, _ := st2.Get("reason")
	if !strings.Contains(reason2.(string), "no AppArmor") {
		t.Errorf("a kernel without AppArmor was reported as one with it switched off: %v", reason2)
	}
}

// A node that is confined and a caller that cannot read the profile list
// is a node reported as confined.
//
// This is the case worth having. The enabled flag is world readable and
// the profile list is root only, so an unprivileged `apparmor.status`
// can answer the first question and not the second. Reporting "AppArmor
// is off" there would be wrong in the direction that matters: an
// operator checking whether a node is confined, and being told no.
func TestStatusSeparatesOffFromUnreadable(t *testing.T) {
	c, _ := apparmorFixture(t, "Y\n", "")
	st, err := apparmorStatus(c)
	if err != nil {
		t.Fatalf("an unreadable profile list was an error: %v", err)
	}
	if on, _ := st.Get("enabled"); on != true {
		t.Errorf("AppArmor was reported off on a node where it is on: %+v", st)
	}
	if readable, _ := st.Get("profiles_readable"); readable != false {
		t.Errorf("an absent profile list was reported readable: %+v", st)
	}
}

// The counts are per mode, and a mode with nothing in it still reports
// zero rather than going missing.
func TestStatusCountsEveryMode(t *testing.T) {
	c, _ := apparmorFixture(t, "Y\n", strings.Join([]string{
		"/usr/bin/man (enforce)",
		"man_filter (enforce)",
		"/usr/sbin/tcpdump (complain)",
	}, "\n")+"\n")
	st, err := apparmorStatus(c)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := st.Get("profiles"); n != 3 {
		t.Errorf("profile count is %v, want 3", n)
	}
	raw, _ := st.Get("modes")
	modes, ok := raw.(*value.Map)
	if !ok {
		t.Fatalf("modes is %T", raw)
	}
	for mode, want := range map[string]int{
		"enforce": 2, "complain": 1, "kill": 0, "unconfined": 0,
	} {
		got, present := modes.Get(mode)
		if !present {
			t.Errorf("%s is missing from the counts; a mode with nothing in it is still a mode", mode)
			continue
		}
		if got != want {
			t.Errorf("%s counted %v, want %d", mode, got, want)
		}
	}
}

// **`tools` says whether a mode can be changed, not whether a binary is
// on PATH.**
//
// The two came apart on a real Ubuntu 24.04: apparmor-utils 4.0.1 is
// installed, is on PATH, and cannot run, because it parses every profile
// under /etc/apparmor.d with its own Python parser before doing anything
// and cannot read the set Ubuntu itself ships. `tools: true` there was
// an answer an operator would have acted on. DIVERGENCE 5.37.
//
// The three shapes below are the ones seen: two different unparseable
// mount rules, and the `Include file not found` that came of moving one
// of them out of the way.
func TestStatusAsksWhetherTheToolsWorkRatherThanWhetherTheyExist(t *testing.T) {
	loaded := "/usr/bin/man (enforce)\n"

	for _, tc := range []struct {
		name, stderr string
	}{
		{"a mount rule the python parser rejects",
			"ERROR: Operation {'runbindable'} cannot have a source. Source = AARE('/')"},
		{"a mount rule it cannot parse at all",
			`ERROR: Can't parse mount rule mount "" -> "/tmp/",`},
		{"an include it cannot find",
			"ERROR: Include file /etc/apparmor.d/abstractions/passt not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, runner := apparmorFixture(t, "Y\n", loaded)
			runner.Responses["aa-enforce "+apparmorProbeProfile] = exec.Result{
				Code: 1, Stderr: tc.stderr,
			}
			st, err := apparmorStatus(c)
			if err != nil {
				t.Fatal(err)
			}
			if tools, _ := st.Get("tools"); tools != false {
				t.Errorf("tools = %v on a node where every aa-* call fails", tools)
			}
			why, _ := st.Get("tools_reason")
			reason, _ := why.(string)
			if !strings.Contains(reason, "cannot parse") {
				t.Errorf("tools_reason does not say why: %q", reason)
			}
			if !strings.Contains(reason, "no mode") {
				t.Errorf("tools_reason does not say what it means for the operator: %q", reason)
			}
		})
	}

	// The reason carries the tool's own words, and the tools print a
	// blank line before their error -- so a message built from the
	// *first* line came out as a colon with nothing after it, in a CI
	// log, where it was the only thing explaining a skip.
	c, runner := apparmorFixture(t, "Y\n", loaded)
	runner.Responses["aa-enforce "+apparmorProbeProfile] = exec.Result{
		Code:   1,
		Stderr: "\nERROR: Can't parse mount rule mount " + `""` + " -> " + `"/tmp/"` + ",",
	}
	st, err := apparmorStatus(c)
	if err != nil {
		t.Fatal(err)
	}
	why, _ := st.Get("tools_reason")
	reason, _ := why.(string)
	if !strings.Contains(reason, "Can't parse mount rule") {
		t.Errorf("the reason does not carry what the tool said: %q", reason)
	}

	// And where the probe comes back the way a working tool answers --
	// it did not find the profile -- the tools are usable.
	c, runner = apparmorFixture(t, "Y\n", loaded)
	runner.Responses["aa-enforce "+apparmorProbeProfile] = exec.Result{
		Code: 1, Stderr: "ERROR: profile halite-probe-does-not-exist does not exist",
	}
	st, err = apparmorStatus(c)
	if err != nil {
		t.Fatal(err)
	}
	if tools, _ := st.Get("tools"); tools != true {
		t.Errorf("tools = %v on a node whose tools work", tools)
	}
	if _, present := st.Get("tools_reason"); present {
		t.Error("a working node carries a reason it does not need")
	}

	// A node with no apparmor-utils at all names the package, because
	// that is a different problem with a different fix.
	c, _ = apparmorFixture(t, "Y\n", loaded)
	c.Lookup = func(string) string { return "" }
	st, err = apparmorStatus(c)
	if err != nil {
		t.Fatal(err)
	}
	if tools, _ := st.Get("tools"); tools != false {
		t.Errorf("tools = %v with no tools installed", tools)
	}
	why, _ = st.Get("tools_reason")
	if reason, _ := why.(string); !strings.Contains(reason, "apparmor-utils") {
		t.Errorf("the reason does not name the package: %q", reason)
	}
}

// The state moves a profile between modes, and does nothing to one that
// is already right.
func TestTheModeStateConvergesAProfile(t *testing.T) {
	loaded := "/usr/sbin/tcpdump (complain)\n/usr/bin/man (enforce)\n"

	// Already enforcing: no command, no change.
	c, runner := apparmorFixture(t, "Y\n", loaded)
	res, err := apparmorModeState(c, value.MapOf("name", "/usr/bin/man", "mode", "enforce"))
	if err != nil {
		t.Fatal(err)
	}
	if res.HasChanges() {
		t.Errorf("a profile already in enforce mode reported a change: %+v", res.Changes)
	}
	if !res.Succeeded() {
		t.Errorf("a converged profile did not succeed: %+v", res)
	}
	if len(runner.Ran) != 0 {
		t.Errorf("a converged profile ran %v", runner.RanCommands())
	}

	// Complaining, asked to enforce: one command, and the right one.
	c, runner = apparmorFixture(t, "Y\n", loaded)
	res, err = apparmorModeState(c, value.MapOf("name", "/usr/sbin/tcpdump", "mode", "enforce"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("a complaining profile asked to enforce reported no change: %+v", res)
	}
	want := "aa-enforce /usr/sbin/tcpdump"
	if got := runner.RanCommands(); len(got) != 1 || got[0] != want {
		t.Errorf("ran %v, want [%s]", got, want)
	}
	if _, ok := res.Changes.Get("/usr/sbin/tcpdump"); !ok {
		t.Errorf("the change is not keyed by the profile: %+v", res.Changes)
	}

	// And in test mode nothing runs.
	c, runner = apparmorFixture(t, "Y\n", loaded)
	c.Test = true
	res, err = apparmorModeState(c, value.MapOf("name", "/usr/sbin/tcpdump", "mode", "enforce"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Errorf("test mode reported no change where there is one: %+v", res)
	}
	if len(runner.Ran) != 0 {
		t.Errorf("test mode ran %v", runner.RanCommands())
	}
}

// A profile named by its file is refused with the distinction spelled
// out, rather than with "not found".
//
// `/etc/apparmor.d/usr.sbin.tcpdump` holds a profile called
// `/usr/sbin/tcpdump`, and writing the file name is the mistake this
// module is shaped around. A bare failure would send an operator
// looking for a missing profile that is loaded and enforcing.
func TestAProfileNamedByItsFileIsRefusedWithTheReason(t *testing.T) {
	c, runner := apparmorFixture(t, "Y\n", "/usr/sbin/tcpdump (complain)\n")
	res, err := apparmorModeState(c, value.MapOf("name", "usr.sbin.tcpdump", "mode", "enforce"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatalf("a file name was accepted as a profile name: %+v", res)
	}
	for _, want := range []string{"/etc/apparmor.d/usr.sbin.tcpdump", "/usr/sbin/tcpdump", "list_profiles"} {
		if !strings.Contains(res.Comment, want) {
			t.Errorf("the refusal does not mention %q: %s", want, res.Comment)
		}
	}
	if len(runner.Ran) != 0 {
		t.Errorf("a refused state ran %v", runner.RanCommands())
	}
}

// Disabling a profile that is not loaded succeeds and says it cannot
// tell the two reasons apart.
//
// `aa-disable` fails on a profile that is not loaded, so running it
// would report a failure for a node that is already in the asked-for
// condition. Succeeding silently would be worse: "not loaded" here
// means either disabled or never installed, and the module reads the
// kernel rather than the filesystem, so it genuinely does not know
// which. It says so instead of picking one.
func TestDisablingAnUnloadedProfileSaysWhatItCannotTell(t *testing.T) {
	c, runner := apparmorFixture(t, "Y\n", "/usr/bin/man (enforce)\n")
	res, err := apparmorModeState(c, value.MapOf("name", "/usr/sbin/tcpdump", "mode", "disable"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Errorf("disabling an unloaded profile failed: %+v", res)
	}
	if res.HasChanges() {
		t.Errorf("disabling an unloaded profile reported a change: %+v", res.Changes)
	}
	if !strings.Contains(res.Comment, "never installed") && !strings.Contains(res.Comment, "no profile by that name") {
		t.Errorf("the comment does not say what it cannot tell apart: %s", res.Comment)
	}
	if len(runner.Ran) != 0 {
		t.Errorf("aa-disable was run against a profile that is not loaded: %v", runner.RanCommands())
	}

	// A loaded one is unloaded, and by the tool.
	c, runner = apparmorFixture(t, "Y\n", "/usr/bin/man (enforce)\n")
	res, err = apparmorModeState(c, value.MapOf("name", "/usr/bin/man", "mode", "disable"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("disabling a loaded profile reported no change: %+v", res)
	}
	if got := runner.RanCommands(); len(got) != 1 || got[0] != "aa-disable /usr/bin/man" {
		t.Errorf("ran %v, want [aa-disable /usr/bin/man]", got)
	}
}

// `kill` and `unconfined` are modes a profile can be in and are not
// modes this state sets, and the refusal says which is which.
func TestTheStateRefusesAModeItCannotSet(t *testing.T) {
	c, _ := apparmorFixture(t, "Y\n", "/usr/bin/man (enforce)\n")
	for _, mode := range []string{"kill", "unconfined", "enforcing", ""} {
		res, err := apparmorModeState(c, value.MapOf("name", "/usr/bin/man", "mode", mode))
		if err != nil {
			t.Fatal(err)
		}
		if res.Succeeded() {
			t.Errorf("mode %q was accepted", mode)
		}
	}
	res, _ := apparmorModeState(c, value.MapOf("name", "/usr/bin/man", "mode", "kill"))
	for _, want := range []string{"enforce", "complain", "disable", "profile itself"} {
		if !strings.Contains(res.Comment, want) {
			t.Errorf("the refusal does not mention %q: %s", want, res.Comment)
		}
	}
}

// A node with no apparmor-utils is told the package to install.
//
// It is not installed by a default Ubuntu, so "aa-enforce: not found" is
// a message that sends somebody looking for a bug. The package name is
// the whole fix.
func TestAMissingToolNamesThePackage(t *testing.T) {
	c, _ := apparmorFixture(t, "Y\n", "/usr/sbin/tcpdump (complain)\n")
	c.Lookup = func(string) string { return "" }
	res, err := apparmorModeState(c, value.MapOf("name", "/usr/sbin/tcpdump", "mode", "enforce"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatal("a node with no apparmor-utils reported success")
	}
	if !strings.Contains(res.Comment, "apparmor-utils") {
		t.Errorf("the failure does not name the package: %s", res.Comment)
	}
}

// A kernel without AppArmor gets a refusal naming that, rather than a
// tool failure.
func TestTheStateRefusesOnAKernelWithoutAppArmor(t *testing.T) {
	c, runner := apparmorFixture(t, "", "")
	res, err := apparmorModeState(c, value.MapOf("name", "/usr/bin/man", "mode", "enforce"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatal("a kernel without AppArmor accepted the state")
	}
	if !strings.Contains(res.Comment, "AppArmor") {
		t.Errorf("the refusal does not name AppArmor: %s", res.Comment)
	}
	if len(runner.Ran) != 0 {
		t.Errorf("a kernel without AppArmor still ran %v", runner.RanCommands())
	}
}

// Off this platform the registry refuses by name, which is the contract
// every platform module has.
func TestAppArmorRefusesOffLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("this is the refusal, and it does not happen on linux")
	}
	r := New()
	if _, err := r.Exec.Call(newCtx(false), "apparmor.status", value.NewMap(0)); err == nil {
		t.Error("apparmor.status was accepted off linux")
	} else if !strings.Contains(err.Error(), runtime.GOOS) || !strings.Contains(err.Error(), "linux") {
		t.Errorf("the refusal does not name both sides: %v", err)
	}
	if _, err := r.States.Call(newCtx(true), "apparmor.mode",
		value.MapOf("name", "/usr/bin/man", "mode", "enforce")); err == nil {
		t.Error("apparmor.mode was accepted off linux")
	}
}

// And on Linux it is reachable through the registry, which is the half
// the direct calls above do not check.
func TestAppArmorIsReachableThroughTheRegistryOnLinux(t *testing.T) {
	skipOffPlatform(t, linuxOnly)
	r := New()
	// status answers on any Linux node, confined or not, because the
	// question it answers is whether the node is confined.
	if _, err := r.Exec.Call(newCtx(false), "apparmor.status", value.NewMap(0)); err != nil {
		t.Errorf("apparmor.status failed on a linux node: %v", err)
	}
}

// The reload function operates on a file, and every other function
// operates on a profile. That difference is in the parameter names,
// because it is the one an operator gets wrong.
func TestReloadTakesAFileAndTheRestTakeAProfile(t *testing.T) {
	r := New()
	sigs := r.Exec.Signatures()
	for name, want := range map[string]string{
		"apparmor.reload":   "path",
		"apparmor.enforce":  "name",
		"apparmor.complain": "name",
		"apparmor.disable":  "name",
		"apparmor.mode":     "name",
	} {
		sig, ok := sigs.Lookup(name)
		if !ok {
			t.Errorf("%s is not registered", name)
			continue
		}
		if len(sig.Params) == 0 || sig.Params[0].Name != want {
			t.Errorf("%s's first parameter is not %q: %+v", name, want, sig.Params)
			continue
		}
		// And the documentation says which of the two it is, so the
		// distinction is readable without this test.
		doc := strings.ToLower(sig.Params[0].Doc)
		if want == "path" && !strings.Contains(doc, "file") {
			t.Errorf("%s's parameter does not say it is a file: %s", name, sig.Params[0].Doc)
		}
		if want == "name" && !strings.Contains(doc, "not the file") {
			t.Errorf("%s's parameter does not say it is not the file: %s", name, sig.Params[0].Doc)
		}
	}
}
