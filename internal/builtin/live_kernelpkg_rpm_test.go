package builtin

import (
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// kernelpkg on EL: rpm's database, dnf's repositories, and
// /lib/modules/<release>.<arch>/vmlinuz, which kernel-core ships and
// which is what the boot loader entries name.
//
// These run in fleet.yml's rpm legs, which are containers on an Ubuntu
// runner: the kernel there is Ubuntu's and no kernel package is
// installed until a test installs one, so `needs_reboot` and `cleanup`
// must say they cannot tell, and do. The raw output of each command is
// logged between `--- capture` markers, because those logs are where
// the parser's fixtures come from.

func kernelpkgRPMGate(t *testing.T) (*rpmKernels, func(fn string, args *value.Map) (any, error)) {
	t.Helper()
	c, f := kernelpkgLiveGate(t)
	r, ok := f.(rpmKernels)
	if !ok {
		t.Skipf("this host's kernels are %s's; this test reads EL's", f.name())
	}
	return &r, func(fn string, args *value.Map) (any, error) { return kpCall(t, c, fn, args) }
}

// capture logs what a command printed, exactly, with its exit status.
func capture(t *testing.T, argv ...string) string {
	t.Helper()
	cmd := osexec.Command(argv[0], argv[1:]...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		code = -1
		if ee, ok := err.(*osexec.ExitError); ok {
			code = ee.ExitCode()
		}
	}
	t.Logf("--- capture %s\n--- exit %d\n--- stdout\n%s--- stderr\n%s--- end capture",
		strings.Join(argv, "\t"), code, stdout.String(), stderr.String())
	return stdout.String()
}

// rpmKernelReleases is what rpm lists for kernel-core, oldest last or
// first as rpm likes; the caller sorts.
func rpmKernelReleases(t *testing.T) []string {
	t.Helper()
	out, err := runEnv("rpm", "-q", "kernel-core", "--queryformat", "%{VERSION}-%{RELEASE}\\n")
	if err != nil {
		return nil // "package kernel-core is not installed"
	}
	return strings.Fields(out)
}

func modulesReleases(t *testing.T) []string {
	t.Helper()
	files, _ := filepath.Glob("/lib/modules/*/vmlinuz")
	var out []string
	for _, f := range files {
		out = append(out, rpmStripArch(filepath.Base(filepath.Dir(f))))
	}
	sort.Strings(out)
	return out
}

func TestLiveKernelpkgReadsAgreeWithRPM(t *testing.T) {
	r, call := kernelpkgRPMGate(t)
	capture(t, "rpm", "-q", "kernel-core", "--queryformat", rpmKernelQueryFormat)
	capture(t, r.binary, "repoquery", "--quiet", "--latest-limit=1", "--queryformat", rpmKernelQueryFormat, rpmKernelPackage)
	capture(t, r.binary, "--quiet", "list", "--showduplicates", "kernel-core")

	running := procRelease(t)
	active, err := call("active", nil)
	if err != nil || active != rpmStripArch(running) {
		t.Fatalf("active = %v, %v; the kernel says %s", active, err, running)
	}
	installed := anyStrings(mustCall(t, call, "list_installed", nil))
	sorted := append([]string(nil), installed...)
	sort.Strings(sorted)
	want := rpmKernelReleases(t)
	sort.Strings(want)
	if strings.Join(sorted, " ") != strings.Join(want, " ") {
		t.Errorf("list_installed = %v, rpm -q kernel-core lists %v", installed, want)
	}
	if mods := modulesReleases(t); strings.Join(sorted, " ") != strings.Join(mods, " ") {
		t.Errorf("list_installed = %v, /lib/modules has kernels %v", installed, mods)
	}
	for i := 1; i < len(installed); i++ {
		if CompareRPM(installed[i-1], installed[i]) > 0 {
			t.Errorf("list_installed is out of order at %s, %s", installed[i-1], installed[i])
		}
	}

	avail := value.KeyString(mustCall(t, call, "latest_available", nil))
	list := capture(t, r.binary, "--quiet", "list", "--showduplicates", "kernel-core-"+avail)
	if !strings.Contains(list, avail) {
		t.Errorf("latest_available %s is not a kernel-core dnf lists", avail)
	}
	t.Logf("running %s; installed %v; newest available %s", running, installed, avail)

	if _, ok := r.findInstalled(installed, rpmStripArch(running)); !ok {
		_, err := call("needs_reboot", nil)
		if err == nil || !strings.Contains(err.Error(), "container") {
			t.Errorf("needs_reboot with the running kernel not a package here: %v", err)
		}
		_, err = call("cleanup", nil)
		if err == nil || !strings.Contains(err.Error(), "cannot be told") {
			t.Errorf("cleanup with the running kernel not a package here: %v", err)
		}
		t.Logf("the running kernel is not a package here, and needs_reboot and cleanup said so")
		return
	}
	latest := installed[len(installed)-1]
	if got := mustCall(t, call, "needs_reboot", nil); got != (latest != rpmStripArch(running)) {
		t.Errorf("needs_reboot = %v with %s running and %s the newest installed", got, running, latest)
	}
	if got := mustCall(t, call, "upgrade_available", nil); got != (CompareRPM(avail, latest) > 0) {
		t.Errorf("upgrade_available = %v; available %s, newest installed %s", got, avail, latest)
	}
}

func (rpmKernels) findInstalled(installed []string, release string) (string, bool) {
	for _, r := range installed {
		if r == release {
			return r, true
		}
	}
	return "", false
}

func mustCall(t *testing.T, call func(string, *value.Map) (any, error), fn string, args *value.Map) any {
	t.Helper()
	v, err := call(fn, args)
	if err != nil {
		t.Fatalf("kernelpkg.%s: %v", fn, err)
	}
	return v
}

// rpmKernelSet is every installed package whose name starts with
// kernel, at its version and architecture, as `dnf install` names one.
func rpmKernelSet() (map[string]bool, error) {
	out, err := runEnv("rpm", "-qa", "--queryformat", "%{NAME}-%{VERSION}-%{RELEASE}.%{ARCH}\\n", "kernel*")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range strings.Fields(out) {
		set[p] = true
	}
	return set, nil
}

func putRPMKernelsBack(t *testing.T, binary string, before map[string]bool) {
	t.Helper()
	now, err := rpmKernelSet()
	if err != nil {
		t.Error(err)
		return
	}
	var missing, extra []string
	for p := range before {
		if !now[p] {
			missing = append(missing, p)
		}
	}
	for p := range now {
		if !before[p] {
			extra = append(extra, p)
		}
	}
	if len(extra) > 0 {
		if _, err := runEnv(append([]string{binary, "remove", "-y", "-q", "--noautoremove"}, extra...)...); err != nil {
			t.Error(err)
		}
	}
	if len(missing) > 0 {
		if _, err := runEnv(append([]string{binary, "install", "-y", "-q"}, missing...)...); err != nil {
			t.Error(err)
		}
	}
	after, _ := rpmKernelSet()
	if fmt.Sprint(rpmSetList(after)) != fmt.Sprint(rpmSetList(before)) {
		t.Errorf("the kernel packages were not put back:\nbefore %v\nafter  %v", rpmSetList(before), rpmSetList(after))
		return
	}
	t.Logf("kernel packages put back (removed %v, reinstalled %v): %v", extra, missing, rpmSetList(after))
}

func rpmSetList(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// upgrade, remove and their refusals: an older kernel installed for the
// purpose, the newest installed by `upgrade`, the older removed.
func TestLiveKernelpkgRPMInstallsAndRemovesKernels(t *testing.T) {
	r, call := kernelpkgRPMGate(t)
	if os.Getenv("HALITE_KERNELPKG_LIVE") != "1" {
		t.Skip("set HALITE_KERNELPKG_LIVE=1 as well to install and remove kernels here")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_KERNELPKG_LIVE is set and this is not root")
	}
	running := rpmStripArch(procRelease(t))
	avail := value.KeyString(mustCall(t, call, "latest_available", nil))
	if avail == running {
		t.Skipf("the newest kernel offered, %s, is the running one; there is nothing to take away and put back", avail)
	}
	// The newest older kernel dnf offers that is not installed and not
	// running.
	installedNow := map[string]bool{}
	for _, k := range rpmKernelReleases(t) {
		installedNow[k] = true
	}
	list, _ := runEnv(r.binary, "--quiet", "list", "--showduplicates", "--available", "kernel-core")
	old := ""
	for _, line := range strings.Split(list, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 || !strings.HasPrefix(f[0], "kernel-core.") {
			continue
		}
		v := strings.TrimPrefix(f[1], "0:")
		if v == avail || v == running || installedNow[v] || CompareRPM(v, avail) >= 0 {
			continue
		}
		if old == "" || CompareRPM(v, old) > 0 {
			old = v
		}
	}
	if old == "" {
		t.Skipf("dnf offers no kernel older than %s that is not installed:\n%s", avail, list)
	}
	before, err := rpmKernelSet()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("before: %v; older kernel for the test %s, newest %s", rpmSetList(before), old, avail)
	t.Cleanup(func() { putRPMKernelsBack(t, r.binary, before) })

	// The container's kernel is the runner's, so the architecture is
	// Go's rather than uname -r's.
	arch := map[string]string{"amd64": ".x86_64", "arm64": ".aarch64", "ppc64le": ".ppc64le", "s390x": ".s390x"}[runtime.GOARCH]
	if installedNow[avail] {
		if _, err := runEnv(r.binary, "remove", "-y", "-q", "kernel-core-"+avail+arch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runEnv(r.binary, "install", "-y", "-q", "kernel-"+old+arch); err != nil {
		t.Fatal(err)
	}
	if got := anyStrings(mustCall(t, call, "list_installed", nil)); !strings.Contains(" "+strings.Join(got, " ")+" ", " "+old+" ") {
		t.Fatalf("list_installed = %v after installing %s", got, old)
	}
	if mustCall(t, call, "upgrade_available", nil) != true {
		t.Errorf("upgrade_available is false with %s installed and %s offered", old, avail)
	}

	got := mustCall(t, call, "upgrade", nil)
	if b, err := value.EncodeJSON(got, 0); err == nil {
		t.Logf("upgrade: %s", b)
	}
	installed := anyStrings(mustCall(t, call, "list_installed", nil))
	if len(installed) < 2 || installed[len(installed)-1] != avail {
		t.Fatalf("list_installed = %v after upgrading to %s", installed, avail)
	}
	if _, err := os.Stat("/lib/modules/" + avail + arch + "/vmlinuz"); err != nil {
		t.Errorf("upgrade did not leave a kernel for %s in /lib/modules: %v", avail, err)
	}
	if again := mustCall(t, call, "upgrade", nil).(*value.Map); func() int { u, _ := again.Get("upgrades"); return u.(*value.Map).Len() }() != 0 {
		t.Errorf("a second upgrade installed something")
	}

	capture(t, r.binary, "remove", "--assumeno", "kernel-core-"+old+arch)
	tc := liveRoot()
	tc.Test = true
	tc.Grains = value.MapOf("kernelrelease", procRelease(t))
	plan, err := kpCall(t, tc, "remove", value.MapOf("release", old))
	if err != nil {
		t.Fatalf("remove in test mode: %v", err)
	}
	t.Logf("remove in test mode: %v", removedOf(plan))
	if rel := rpmKernelReleases(t); !strings.Contains(strings.Join(rel, " "), old) {
		t.Fatal("remove in test mode removed the kernel")
	}
	got = mustCall(t, call, "remove", value.MapOf("release", old))
	t.Logf("remove: %v", removedOf(got))
	if out, err := runEnv("rpm", "-q", "kernel-"+old, "kernel-core-"+old, "kernel-modules-"+old); err == nil ||
		strings.Count(out, "is not installed") != 3 {
		t.Errorf("after remove, rpm says:\n%s", out)
	}
	if _, err := os.Stat("/lib/modules/" + old + arch + "/vmlinuz"); err == nil {
		t.Errorf("/lib/modules still has %s after the removal", old)
	}
}
