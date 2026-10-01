package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Every fixture under testdata/zypper/leap16 was captured as root on
// 2026-09-30 on the lab's openSUSE Leap 16.0 (zypper 1.14.101, rpm
// 4.20.1), by one script that saved each command's argv, stdout, stderr
// and exit status byte for byte. It installed and removed `tree`, locked
// and unlocked it, added an unreachable repository and removed it again,
// and upgraded `libX11-data` one release and downgraded it back. None is
// written by hand.

// zypperFixtureRunner serves captured fixtures by the argv they were
// captured under, and fails a non-zero exit the way exec.OSRunner does
// when the command did not ask for its exit code.
//
// The second half is the point. exec.RecordingRunner hands back a
// scripted exit code with a nil error whatever the command asked, so a
// branch on `res.Code` in a command that forgot IgnoreExitCode passes
// under it and is unreachable on a machine (DIVERGENCE 5.113). This one
// cannot be fooled that way. And an argv that matches no fixture is a
// test failure, not a default answer, so a command that drifts from what
// was captured is caught rather than served something plausible.
type zypperFixtureRunner struct {
	t       *testing.T
	results map[string]exec.Result
	Ran     []exec.Command
}

func zypperFixture(t *testing.T, name, part string) string {
	t.Helper()
	return capturedFixture(t, zypperFixtureDir, name, part)
}

var zypperFixtureDir = filepath.Join("testdata", "zypper", "leap16")

// capturedFixture is one part (argv, stdout, stderr, exit) of a command
// captured under dir.
func capturedFixture(t *testing.T, dir, name, part string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name+"."+part))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// newZypperRunner loads the named fixtures, keyed by their argv. Two
// fixtures with the same argv (tree installed, then installed again) are
// two different scenarios and cannot share one runner.
func newZypperRunner(t *testing.T, names ...string) *zypperFixtureRunner {
	t.Helper()
	return newCapturedRunner(t, zypperFixtureDir, names...)
}

// newCapturedRunner is newZypperRunner for the fixtures under any dir
// laid out the same way.
func newCapturedRunner(t *testing.T, dir string, names ...string) *zypperFixtureRunner {
	t.Helper()
	r := &zypperFixtureRunner{t: t, results: map[string]exec.Result{}}
	for _, n := range names {
		argv := strings.TrimSpace(capturedFixture(t, dir, n, "argv"))
		if _, dup := r.results[argv]; dup {
			t.Fatalf("two fixtures answer %q", argv)
		}
		code, err := strconv.Atoi(strings.TrimSpace(capturedFixture(t, dir, n, "exit")))
		if err != nil {
			t.Fatal(err)
		}
		r.results[argv] = exec.Result{
			Stdout: capturedFixture(t, dir, n, "stdout"),
			Stderr: capturedFixture(t, dir, n, "stderr"),
			Code:   code,
		}
	}
	return r
}

func (r *zypperFixtureRunner) Run(_ context.Context, cmd exec.Command) (exec.Result, error) {
	r.Ran = append(r.Ran, cmd)
	key := strings.Join(cmd.Argv, " ")
	res, ok := r.results[key]
	if !ok {
		r.t.Errorf("no captured fixture answers `%s`", key)
		return exec.Result{Code: 1}, fmt.Errorf("no fixture for %s", key)
	}
	if res.Code != 0 && !cmd.IgnoreExitCode {
		return res, fmt.Errorf("%s exited %d", key, res.Code)
	}
	return res, nil
}

func zypperCtx(t *testing.T, r *zypperFixtureRunner) *exec.Context {
	t.Helper()
	c := newCtx(false)
	c.Grains = value.MapOf("os", "SUSE", "os_family", "Suse")
	c.Runner = r
	c.Lookup = func(name string) string {
		if name == "zypper" || name == "rpm" {
			return "/usr/bin/" + name
		}
		return ""
	}
	return c
}

// rpmQA serves the captured `rpm -qa`, taken before tree was installed,
// under its own argv, with `extra` lines appended.
func rpmQA(t *testing.T, r *zypperFixtureRunner, extra string) {
	t.Helper()
	argv := strings.TrimSpace(zypperFixture(t, "rpm-qa", "argv"))
	r.results[argv] = exec.Result{Stdout: zypperFixture(t, "rpm-qa", "stdout") + extra}
}

// ---- selection ----

func TestZypperIsPickedBeforeDnf(t *testing.T) {
	for _, tc := range []struct {
		name string
		path []string
		want string
	}{
		// Leap 16's repo-oss offers dnf; a SUSE node that installed it
		// must still be managed by zypper.
		{"a SUSE node with dnf installed", []string{"zypper", "rpm", "dnf"}, "zypperpkg"},
		{"a SUSE node", []string{"zypper", "rpm"}, "zypperpkg"},
		{"an EL node", []string{"rpm", "dnf"}, "dnfpkg"},
		// zypper without rpm is not a system zypper manages.
		{"zypper and no rpm", []string{"zypper"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCtx(false)
			on := map[string]bool{}
			for _, p := range tc.path {
				on[p] = true
			}
			c.Lookup = func(name string) string {
				if on[name] {
					return "/usr/bin/" + name
				}
				return ""
			}
			p, err := pickPkgProvider(c)
			if tc.want == "" {
				if err == nil {
					t.Errorf("picked %s on a node with no rpm", p.Name())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Name() != tc.want {
				t.Errorf("picked %s, want %s", p.Name(), tc.want)
			}
		})
	}
}

// ---- exit codes ----

// Every zypper command asks for its exit code. Run each provider method
// once and look at what it asked for, because the fixture runner can only
// catch a missing flag on a path whose fixture exits non-zero.
func TestZypperCommandsAskForTheirExitCode(t *testing.T) {
	rec := &exec.RecordingRunner{Default: exec.Result{Stdout: "<stream></stream>"}}
	c := newCtx(false)
	c.Runner = rec
	p := zypperProvider{}
	_ = p.Install(c, []string{"tree"}, map[string]string{"tree": "1"}, true)
	_ = p.Remove(c, []string{"tree"}, true)
	_, _ = p.LatestVersion(c, "tree")
	_ = p.RefreshDB(c)
	_ = p.Hold(c, "tree")
	_ = p.Unhold(c, "tree")
	_, _ = p.ListHolds(c)
	_, _ = p.Upgrade(c, true)
	_, _ = p.ListUpgrades(c, false)
	_, _ = p.ListRepos(c)
	seen := 0
	for _, cmd := range rec.Ran {
		if cmd.Argv[0] != "zypper" {
			continue
		}
		seen++
		if !cmd.IgnoreExitCode {
			t.Errorf("`%s` does not ask for its exit code, so its answer codes are unreachable on a machine",
				cmd.String())
		}
		if !contains(cmd.Argv, "--non-interactive") {
			t.Errorf("`%s` is interactive", cmd.String())
		}
		if contains(cmd.Argv, "--gpg-auto-import-keys") {
			t.Errorf("`%s` trusts any repository key it is offered", cmd.String())
		}
	}
	if seen < 12 {
		t.Errorf("only %d zypper commands ran; this test has stopped covering the provider", seen)
	}
}

// ---- reads ----

// SUSE keeps kernels side by side, and the lab machine had two of each.
func TestZypperListPkgsKeepsTheNewestOfTwoInstalledKernels(t *testing.T) {
	stdout := zypperFixture(t, "rpm-qa", "stdout")
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	reversed := make([]string, len(lines))
	for i, ln := range lines {
		reversed[len(lines)-1-i] = ln
	}
	for _, order := range []struct {
		name, stdout string
	}{{"as rpm printed it", stdout}, {"reversed", strings.Join(reversed, "\n") + "\n"}} {
		got := parseRPMInstalledNewest(order.stdout)
		for name, want := range map[string]string{
			"kernel-default":          "6.12.0-160000.37.1",
			"kernel-default-extra":    "6.12.0-160000.37.1",
			"kernel-default-optional": "6.12.0-160000.37.1",
			"zypper":                  "1.14.101-160000.1.1",
			"libX11-data":             "1.8.10-160000.3.1",
		} {
			if v, _ := got.Get(name); v != want {
				t.Errorf("%s: %s = %v, want %s", order.name, name, v, want)
			}
		}
		if got.Has("tree") {
			t.Errorf("%s: tree is listed, and the capture was taken before it was installed", order.name)
		}
	}
}

// The dnf provider sends the same `rpm -qa` argv, byte for byte, so the
// Leap capture is a real answer to it: EL's installonly kernels have the
// same shape (AlmaLinux 8.10 in the lab had two kernel-core, 5.172). It
// used to keep whichever instance rpm printed last, so the answer depended
// on the order of rpm's database -- the Leap capture happens to print the
// older kernel first, which is why only the reversed order caught it.
func TestDnfListPkgsKeepsTheNewestWhateverOrderRpmPrints(t *testing.T) {
	stdout := zypperFixture(t, "rpm-qa", "stdout")
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	reversed := make([]string, len(lines))
	for i, ln := range lines {
		reversed[len(lines)-1-i] = ln
	}
	argv := strings.TrimSpace(zypperFixture(t, "rpm-qa", "argv"))
	for _, order := range []struct {
		name, stdout string
	}{{"as rpm printed it", stdout}, {"reversed", strings.Join(reversed, "\n") + "\n"}} {
		r := newZypperRunner(t)
		r.results[argv] = exec.Result{Stdout: order.stdout}
		got, err := dnfProvider{binary: "dnf"}.ListPkgs(zypperCtx(t, r))
		if err != nil {
			t.Fatalf("%s: %v", order.name, err)
		}
		for name, want := range map[string]string{
			"kernel-default":       "6.12.0-160000.37.1",
			"kernel-default-extra": "6.12.0-160000.37.1",
			"libX11-data":          "1.8.10-160000.3.1",
		} {
			if v, _ := got.Get(name); v != want {
				t.Errorf("%s: %s = %v, want %s", order.name, name, v, want)
			}
		}
	}
}

func TestZypperLatestVersionFromTheRealSearch(t *testing.T) {
	for _, tc := range []struct {
		fixture, name, want string
	}{
		// Not installed: the only edition there is.
		{"se-tree", "tree", "2.2.1-160000.2.2"},
		// Installed at the newest the repositories have.
		{"se-zypper", "zypper", ""},
		{"se-tree-installed", "tree", ""},
		// Installed one release behind; the newer is `other-version`.
		{"se-libX11-data", "libX11-data", "1.8.10-160000.4.1"},
		// Two installed kernels and a newer one in the repository.
		{"se-kernel", "kernel-default", "6.12.0-160000.38.1"},
		// 104: no such package is an answer, not an error.
		{"se-nosuch", "nosuchpkg-halite", ""},
		// 106: one repository unreachable, and the rest answered.
		{"se-tree-bogus", "tree", "2.2.1-160000.2.2"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			c := zypperCtx(t, newZypperRunner(t, tc.fixture))
			got, err := zypperProvider{}.LatestVersion(c, tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("LatestVersion(%s) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

func TestZypperListUpgradesFromTheRealListUpdates(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		refresh bool
	}{
		{"lu", true},
		{"lu-norefresh", false},
		{"lu-bogus", true}, // 106, and the list is still whole
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			c := zypperCtx(t, newZypperRunner(t, tc.fixture))
			got, err := zypperProvider{}.ListUpgrades(c, tc.refresh)
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Count(zypperFixture(t, tc.fixture, "stdout"), "<update ")
			if want == 0 || got.Len() != want {
				t.Errorf("%d upgrades parsed, and the stream has %d <update> elements", got.Len(), want)
			}
			for name, v := range map[string]string{
				"kernel-default": "6.12.0-160000.38.1",
				"libX11-data":    "1.8.10-160000.4.1",
				"libexpat1":      "2.8.4-160000.1.1",
			} {
				if g, _ := got.Get(name); g != v {
					t.Errorf("%s = %v, want %s", name, g, v)
				}
			}
		})
	}
}

func TestZypperListHoldsFromTheRealLocks(t *testing.T) {
	c := zypperCtx(t, newZypperRunner(t, "locks-tree"))
	got, err := zypperProvider{}.ListHolds(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "tree" {
		t.Errorf("holds = %v, want [tree]", got)
	}

	c = zypperCtx(t, newZypperRunner(t, "locks-empty"))
	got, err = zypperProvider{}.ListHolds(c)
	if err != nil || len(got) != 0 {
		t.Errorf("holds with no locks = %v, %v", got, err)
	}
}

func TestZypperListReposFromTheRealRepos(t *testing.T) {
	c := zypperCtx(t, newZypperRunner(t, "lr"))
	got, err := zypperProvider{}.ListRepos(c)
	if err != nil {
		t.Fatal(err)
	}
	keys := got.StringKeys()
	sort.Strings(keys)
	want := []string{"Leap", "openSUSE:repo-non-oss", "openSUSE:repo-non-oss-debug",
		"openSUSE:repo-openh264", "openSUSE:repo-oss", "openSUSE:repo-oss-debug", "openSUSE:repo-oss-source"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("repos = %v, want %v", keys, want)
	}
	oss, _ := got.Get("openSUSE:repo-oss")
	m := oss.(*value.Map)
	for k, v := range map[string]any{
		"name": "repo-oss (16.0)", "enabled": true, "autorefresh": true, "gpgcheck": true,
		"priority": int64(99), "type": "rpm-md",
		"baseurl": "http://cdn.opensuse.org/distribution/leap/16.0/repo/oss/x86_64",
	} {
		if g, _ := m.Get(k); g != v {
			t.Errorf("repo-oss %s = %#v, want %#v", k, g, v)
		}
	}
	// The DVD the image was built from: configured, and disabled.
	leap, _ := got.Get("Leap")
	if enabled, _ := leap.(*value.Map).Get("enabled"); enabled != false {
		t.Errorf("Leap enabled = %v; the capture says 0", enabled)
	}
	// A repository never refreshed has no type, and none is invented.
	nonoss, _ := got.Get("openSUSE:repo-non-oss")
	if nonoss.(*value.Map).Has("type") {
		t.Error("repo-non-oss has a type, and zypper gave it none")
	}
}

// ---- mutations ----

func TestZypperInstallSpellsTheCommandThatWasCaptured(t *testing.T) {
	c := zypperCtx(t, newZypperRunner(t, "install-tree"))
	if err := (zypperProvider{}).Install(c, []string{"tree"}, nil, false); err != nil {
		t.Fatal(err)
	}

	// A pin brings --oldpackage with it, which is what makes a pin below
	// the installed version do anything at all.
	c = zypperCtx(t, newZypperRunner(t, "install-downgrade"))
	err := zypperProvider{}.Install(c, []string{"libX11-data"},
		map[string]string{"libX11-data": "1.8.10-160000.3.1"}, false)
	if err != nil {
		t.Fatal(err)
	}

	// refresh runs `zypper refresh` first.
	r := newZypperRunner(t, "refresh", "install-tree")
	c = zypperCtx(t, r)
	if err := (zypperProvider{}).Install(c, []string{"tree"}, nil, true); err != nil {
		t.Fatal(err)
	}
	if len(r.Ran) != 2 || r.Ran[0].Argv[2] != "refresh" {
		t.Errorf("ran %v", r.Ran)
	}
}

// Without --oldpackage, a pin below the installed version is "Nothing to
// do" and exit 0. The captured dry run is what the flag exists for; this
// holds that the capture still says so.
func TestZypperPinBelowInstalledIsSilentWithoutOldpackage(t *testing.T) {
	if code := strings.TrimSpace(zypperFixture(t, "downgrade-nooldpackage", "exit")); code != "0" {
		t.Fatalf("exit = %s", code)
	}
	out := zypperFixture(t, "downgrade-nooldpackage", "stdout")
	if !strings.Contains(out, "has lower version than the installed one") || !strings.Contains(out, "Nothing to do.") {
		t.Errorf("the capture no longer shows zypper declining silently:\n%s", out)
	}
}

func TestZypperInstallOfNothingThatExistsFails(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		names   []string
		pins    map[string]string
		want    string
	}{
		{"install-nosuch", []string{"nosuchpkg-halite"}, nil, "Package 'nosuchpkg-halite' not found."},
		{"install-badversion", []string{"tree"}, map[string]string{"tree": "9.9"}, "Package 'tree=9.9' not found."},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			r := newZypperRunner(t, tc.fixture)
			// The captured bad-version command has no --oldpackage,
			// because the capture script did not pass one; serve it under
			// the argv the provider spells.
			if tc.pins != nil {
				res := r.results["zypper --non-interactive install --auto-agree-with-licenses --name tree=9.9"]
				r.results["zypper --non-interactive install --auto-agree-with-licenses --oldpackage --name tree=9.9"] = res
			}
			err := zypperProvider{}.Install(zypperCtx(t, r), tc.names, tc.pins, false)
			if err == nil {
				t.Fatal("an install of a package no repository has succeeded")
			}
			if !strings.Contains(err.Error(), "104") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v", err)
			}
		})
	}
}

// 106 on install: the package was installed from the repositories that
// did answer. rpm is asked, and rpm decides.
func TestZypperInstallThatSkippedARepositoryIsCheckedAgainstRpm(t *testing.T) {
	r := newZypperRunner(t, "install-tree-bogus")
	rpmQA(t, r, "tree\t(none):2.2.1-160000.2.2\n")
	if err := (zypperProvider{}).Install(zypperCtx(t, r), []string{"tree"}, nil, false); err != nil {
		t.Errorf("tree was installed and the install failed: %v", err)
	}

	r = newZypperRunner(t, "install-tree-bogus")
	rpmQA(t, r, "")
	err := zypperProvider{}.Install(zypperCtx(t, r), []string{"tree"}, nil, false)
	if err == nil || !strings.Contains(err.Error(), "tree not installed") {
		t.Errorf("106 with the package absent = %v; want a failure naming it", err)
	}
}

func TestZypperRemoveIsCheckedAgainstRpmWhenZypperCannotSay(t *testing.T) {
	// 104, the package already gone: the outcome asked for.
	r := newZypperRunner(t, "remove-absent")
	rpmQA(t, r, "")
	if err := (zypperProvider{}).Remove(zypperCtx(t, r), []string{"tree"}, false); err != nil {
		t.Errorf("removing what is not there failed: %v", err)
	}

	// 104 with one name found and removed and one never there.
	r = newZypperRunner(t, "remove-mixed-dry")
	res := r.results["zypper --non-interactive remove --dry-run --name tree nosuchpkg-halite"]
	r.results["zypper --non-interactive remove --name tree nosuchpkg-halite"] = res
	rpmQA(t, r, "")
	if err := (zypperProvider{}).Remove(zypperCtx(t, r), []string{"tree", "nosuchpkg-halite"}, false); err != nil {
		t.Errorf("both are absent afterwards, and the removal failed: %v", err)
	}
	// ... and the same exit with tree still there is a failure.
	rpmQA(t, r, "tree\t(none):2.2.1-160000.2.2\n")
	err := zypperProvider{}.Remove(zypperCtx(t, r), []string{"tree", "nosuchpkg-halite"}, false)
	if err == nil || !strings.Contains(err.Error(), "tree still installed") {
		t.Errorf("tree is still installed and Remove said %v", err)
	}
}

// A lock stops a removal, and the reason zypper gives is on stdout.
func TestZypperRemoveOfALockedPackageSaysWhy(t *testing.T) {
	err := zypperProvider{}.Remove(zypperCtx(t, newZypperRunner(t, "remove-locked")), []string{"tree"}, false)
	if err == nil {
		t.Fatal("a locked package was removed")
	}
	if !strings.Contains(err.Error(), "remove lock to allow removal") {
		t.Errorf("the error does not carry zypper's reason: %v", err)
	}
}

func TestZypperRefreshWithAnUnreachableRepositoryFails(t *testing.T) {
	err := zypperProvider{}.RefreshDB(zypperCtx(t, newZypperRunner(t, "refresh-bogus")))
	if err == nil || !strings.Contains(err.Error(), "halite-bogus") {
		t.Errorf("refresh with one repository unreachable = %v", err)
	}
	if err := (zypperProvider{}).RefreshDB(zypperCtx(t, newZypperRunner(t, "refresh"))); err != nil {
		t.Error(err)
	}
}

func TestZypperHoldAndUnholdSpellTheCapturedCommands(t *testing.T) {
	c := zypperCtx(t, newZypperRunner(t, "addlock-tree"))
	if err := (zypperProvider{}).Hold(c, "tree"); err != nil {
		t.Fatal(err)
	}
	c = zypperCtx(t, newZypperRunner(t, "removelock-again"))
	if err := (zypperProvider{}).Unhold(c, "tree"); err != nil {
		t.Errorf("unholding what is not held: %v", err)
	}
}

// OwnerOf is not here any more. Leap's `rpm -qf` answers were captured
// under the dnf provider's old `--queryformat %{NAME}` argv, which ran two
// owners together (DIVERGENCE 5.177); the provider now sends rpm.owner's
// argv, which was captured on Rocky 9.8 and Alma 8.10 but not on Leap, so
// TestRpmPkgOwnerOfASharedPathIsOneRealPackage holds both providers to
// the Rocky bytes rather than this test serving Leap bytes under an argv
// Leap was never asked.
func TestZypperFileListAsksRpm(t *testing.T) {
	c := zypperCtx(t, newZypperRunner(t, "rpm-ql-tree"))
	files, err := zypperProvider{}.FileList(c, "tree")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(files, "/usr/bin/tree") || len(files) != 7 {
		t.Errorf("files = %v", files)
	}
}
