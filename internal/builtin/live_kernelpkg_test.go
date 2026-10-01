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

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The kernelpkg module, against the real package database and the real
// kernels in /boot.
//
// The oracles are never this module's parsers. What is installed is
// what /boot holds (Debian's image packages each ship
// /boot/vmlinuz-<release>, and that is what the boot loader boots), what
// is running is /proc/sys/kernel/osrelease, and every ordering is
// checked with `dpkg --compare-versions`. A removal is checked by asking
// dpkg about the package *and its unsigned twin*, because the trap this
// module is built around is apt answering a purge by installing the twin.
//
// Two gates. HALITE_SYSTEM_LIVE=1 reads. HALITE_KERNELPKG_LIVE=1 also
// installs and removes kernels, which runs the initramfs and boot-loader
// hooks and takes a minute a kernel; it never touches the running kernel,
// and every test that changes the set puts back what it found -- the
// same packages at the same versions with the same automatic marks --
// and fails if it could not.
//
// Nothing here reboots, and nothing schedules a reboot: `latest_active`
// is driven in test mode only.

func kernelpkgLiveGate(t *testing.T) (*hexec.Context, kernelFamily) {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to read this host's kernels")
	}
	if runtime.GOOS != "linux" {
		t.Skipf("kernelpkg is Linux's; TestLiveKernelpkgIsRefusedOffLinux covers %s", runtime.GOOS)
	}
	c := liveRoot()
	c.Grains = value.MapOf("kernelrelease", procRelease(t))
	f, err := pickKernelFamily(c)
	if err != nil {
		t.Skipf("this host's package manager is not one kernelpkg reads: %v", err)
	}
	return c, f
}

func kernelpkgMutateGate(t *testing.T) (*hexec.Context, kernelFamily) {
	t.Helper()
	c, f := kernelpkgLiveGate(t)
	if os.Getenv("HALITE_KERNELPKG_LIVE") != "1" {
		t.Skip("set HALITE_KERNELPKG_LIVE=1 as well to install and remove kernels here; each runs the " +
			"initramfs and boot-loader hooks, and the running kernel is never touched")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_KERNELPKG_LIVE is set and this is not root")
	}
	return c, f
}

func procRelease(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func kpCall(t *testing.T, c *hexec.Context, fn string, args *value.Map) (any, error) {
	t.Helper()
	if args == nil {
		args = value.NewMap(0)
	}
	return New().Exec.Call(c, "kernelpkg."+fn, args)
}

func kpMust(t *testing.T, c *hexec.Context, fn string, args *value.Map) any {
	t.Helper()
	v, err := kpCall(t, c, fn, args)
	if err != nil {
		t.Fatalf("kernelpkg.%s: %v", fn, err)
	}
	return v
}

func kpRunOut(t *testing.T, argv ...string) (string, bool) {
	t.Helper()
	cmd := osexec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func kpRun(t *testing.T, argv ...string) string {
	t.Helper()
	out, ok := kpRunOut(t, argv...)
	if !ok {
		t.Fatalf("%s: %s", strings.Join(argv, " "), out)
	}
	return out
}

// bootReleases are the kernels /boot holds of one flavour.
func bootReleases(t *testing.T, flavour string) []string {
	t.Helper()
	files, _ := filepath.Glob("/boot/vmlinuz-*")
	var out []string
	for _, f := range files {
		r := strings.TrimPrefix(filepath.Base(f), "vmlinuz-")
		if fl, ok := debianFlavour(r); ok && fl == flavour {
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

func dpkgVersion(t *testing.T, pkg string) string {
	t.Helper()
	out, ok := kpRunOut(t, "dpkg-query", "-W", "-f=${Status}\\t${Version}", pkg)
	if !ok {
		return ""
	}
	status, v, _ := strings.Cut(out, "\t")
	if !strings.HasSuffix(status, " installed") {
		return ""
	}
	return v
}

func dpkgGreater(t *testing.T, a, b string) bool {
	t.Helper()
	_, ok := kpRunOut(t, "dpkg", "--compare-versions", a, "gt", b)
	return ok
}

// onlyKernel reports whether a removal names the kernel's image and
// nothing that is not of its release: on Ubuntu its linux-modules and
// headers come too, on Debian its headers.
func onlyKernel(removed []string, release string) bool {
	image := false
	for _, p := range removed {
		if p == "linux-image-"+release {
			image = true
		}
		if !strings.HasSuffix(p, "-"+release) && !strings.HasSuffix(p, "-"+release+"-unsigned") {
			return false
		}
	}
	return image
}

func removedOf(v any) any {
	m, _ := v.(*value.Map)
	if m == nil {
		return nil
	}
	r, _ := m.Get("removed")
	return r
}

func TestLiveKernelpkgIsRefusedOffLinux(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to ask this host")
	}
	if runtime.GOOS == "linux" {
		t.Skip("this is Linux, where kernelpkg runs; the read tests cover it")
	}
	for _, fn := range []string{"active", "list_installed", "upgrade", "remove"} {
		args := value.NewMap(0)
		if fn == "remove" {
			args.Set("release", "14.3-RELEASE")
		}
		_, err := kpCall(t, liveRoot(), fn, args)
		if err == nil || !strings.Contains(err.Error(), "runs on linux") {
			t.Errorf("kernelpkg.%s on %s: %v", fn, runtime.GOOS, err)
			continue
		}
		t.Logf("kernelpkg.%s: %v", fn, err)
	}
}

// The readers, against /boot, /proc and dpkg's own ordering.
func TestLiveKernelpkgReadsAgreeWithTheMachine(t *testing.T) {
	c, f := kernelpkgLiveGate(t)
	if _, ok := f.(aptKernels); !ok {
		t.Skipf("this host's kernels are %s's; this test reads Debian's /boot naming", f.name())
	}
	running := procRelease(t)
	if got := kpMust(t, c, "active", nil); got != running {
		t.Fatalf("active = %v, and the kernel says %s", got, running)
	}
	flavour, ok := debianFlavour(running)
	if !ok {
		t.Fatalf("the running release %s has no flavour this module can read", running)
	}
	installed := anyStrings(kpMust(t, c, "list_installed", nil))
	t.Logf("running %s; installed %v", running, installed)

	sorted := append([]string(nil), installed...)
	sort.Strings(sorted)
	if want := bootReleases(t, flavour); strings.Join(sorted, " ") != strings.Join(want, " ") {
		t.Errorf("list_installed has %v and /boot has %v", sorted, want)
	}
	for i := 1; i < len(installed); i++ {
		a, b := dpkgVersion(t, "linux-image-"+installed[i-1]), dpkgVersion(t, "linux-image-"+installed[i])
		if a == "" || b == "" {
			continue // the unsigned twin is the one installed; its version is the same
		}
		if dpkgGreater(t, a, b) {
			t.Errorf("list_installed puts %s (%s) before %s (%s), and dpkg orders them the other way",
				installed[i-1], a, installed[i], b)
		}
	}

	if dpkgVersion(t, "linux-image-"+running) == "" && dpkgVersion(t, "linux-image-"+running+"-unsigned") == "" &&
		dpkgVersion(t, "linux-image-unsigned-"+running) == "" {
		// A container: the kernel is its host's and no package here is it.
		_, err := kpCall(t, c, "needs_reboot", nil)
		if err == nil || !strings.Contains(err.Error(), "container") {
			t.Errorf("needs_reboot with the running kernel not a package here: %v", err)
		}
		t.Logf("the running kernel is not a package on this host, and needs_reboot said so: %v", err)
		return
	}

	latest := kpMust(t, c, "latest_installed", nil)
	if latest != installed[len(installed)-1] {
		t.Errorf("latest_installed = %v, list_installed ends with %s", latest, installed[len(installed)-1])
	}
	if got, want := kpMust(t, c, "needs_reboot", nil), latest != running; got != want {
		t.Errorf("needs_reboot = %v, with %s running and %v the newest installed", got, running, latest)
	}

	avail := value.KeyString(kpMust(t, c, "latest_available", nil))
	policy := kpRun(t, "apt-cache", "policy", "linux-image-"+avail)
	if !strings.Contains(policy, "Candidate:") || strings.Contains(policy, "Candidate: (none)") {
		t.Errorf("latest_available %s is not a package apt can install:\n%s", avail, policy)
	}
	meta := kpRun(t, "apt-cache", "show", "--no-all-versions", "linux-image-"+flavour)
	if !strings.Contains(" "+strings.NewReplacer(",", " ", "|", " ", "\n", " ").Replace(controlField(meta, "Depends"))+" ", " linux-image-"+avail+" ") {
		t.Errorf("latest_available %s is not what linux-image-%s depends on:\n%s", avail, flavour, meta)
	}
	up := kpMust(t, c, "upgrade_available", nil)
	availVersion := strings.TrimSpace(strings.SplitN(strings.SplitN(policy, "Candidate:", 2)[1], "\n", 2)[0])
	newest := dpkgVersion(t, "linux-image-"+value.KeyString(latest))
	if want := dpkgGreater(t, availVersion, newest); up != want {
		t.Errorf("upgrade_available = %v; available %s, newest installed %s", up, availVersion, newest)
	}

	// The states in test mode change nothing and say what they would do.
	tc := liveRoot()
	tc.Grains = c.Grains
	tc.Test = true
	before := bootReleases(t, flavour)
	res, err := kernelpkgLatestActiveState(tc, value.MapOf("name", "k"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("latest_active in test mode: result=%v comment=%s", res.Result, res.Comment)
	if latest != running && res.Result != nil && !strings.Contains(res.Comment, "already pending") {
		t.Errorf("latest_active in test mode with %v installed and %s running: %+v", latest, running, res)
	}
	if latest == running && (res.Result == nil || !*res.Result) {
		t.Errorf("latest_active with the newest running: %+v", res)
	}
	pending, err := rebootScheduled(liveRoot())
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := pending.(*value.Map).GetString("scheduled"); p == true && !strings.Contains(res.Comment, "already pending") {
		t.Errorf("a reboot is pending after latest_active in test mode")
	}
	if _, err := kpCall(t, tc, "upgrade", value.MapOf("reboot", true)); err != nil {
		t.Errorf("upgrade in test mode: %v", err)
	}
	if after := bootReleases(t, flavour); strings.Join(after, " ") != strings.Join(before, " ") {
		t.Errorf("test mode changed /boot: %v to %v", before, after)
	}
}

// ---- putting the kernel set back ----

// debianKernelSnapshot is every linux-image and linux-headers package
// installed, its version, and whether apt marked it automatic.
//
// It and its putBack return errors rather than taking a *testing.T,
// because the conformance case's Setup and Cleanup have none.
type debianKernelSnapshot struct {
	versions map[string]string
	auto     map[string]bool
}

func runEnv(argv ...string) (string, error) {
	cmd := osexec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %v: %s", strings.Join(argv, " "), err, out)
	}
	return string(out), nil
}

func readDebianKernelSnapshot() (debianKernelSnapshot, error) {
	s := debianKernelSnapshot{versions: map[string]string{}, auto: map[string]bool{}}
	// dpkg-query exits 1 when one pattern matches nothing, and still
	// lists the other.
	out, _ := runEnv("dpkg-query", "-W", "-f=${Package}\\t${Version}\\t${Status}\\n", "linux-image-*", "linux-headers-*", "linux-kbuild-*", "linux-modules-*")
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 3 && strings.HasSuffix(fields[2], " installed") {
			s.versions[fields[0]] = fields[1]
		}
	}
	autoOut, err := runEnv("apt-mark", "showauto")
	if err != nil {
		return s, err
	}
	for _, p := range strings.Fields(autoOut) {
		if _, ok := s.versions[p]; ok {
			s.auto[p] = true
		}
	}
	return s, nil
}

func takeDebianKernelSnapshot(t *testing.T) debianKernelSnapshot {
	t.Helper()
	s, err := readDebianKernelSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (s debianKernelSnapshot) String() string {
	var lines []string
	for p, v := range s.versions {
		mark := "manual"
		if s.auto[p] {
			mark = "auto"
		}
		lines = append(lines, p+" "+v+" ("+mark+")")
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// putBack restores the set: what is missing reinstalled at its version,
// what is extra purged (never the running kernel's), the marks reset. It
// says what it did, and fails when the set still differs.
func (s debianKernelSnapshot) putBack() (string, error) {
	now, err := readDebianKernelSnapshot()
	if err != nil {
		return "", err
	}
	running := ""
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		running = strings.TrimSpace(string(b))
	}
	var missing, extra []string
	for p, v := range s.versions {
		if now.versions[p] != v {
			missing = append(missing, p+"="+v)
		}
	}
	for p := range now.versions {
		if _, ok := s.versions[p]; !ok {
			if running != "" && (strings.HasSuffix(p, "-"+running) || strings.HasSuffix(p, "-"+running+"-unsigned")) {
				return "", fmt.Errorf("%s appeared during the test and is the running kernel's; it was left", p)
			}
			extra = append(extra, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	var did []string
	if len(extra) > 0 {
		if _, err := runEnv(append([]string{"apt-get", "purge", "-y", "-q"}, extra...)...); err != nil {
			return "", err
		}
		did = append(did, fmt.Sprintf("purged %v", extra))
	}
	if len(missing) > 0 {
		if _, err := runEnv(append([]string{"apt-get", "install", "-y", "-q", "--allow-downgrades"}, missing...)...); err != nil {
			return "", err
		}
		did = append(did, fmt.Sprintf("reinstalled %v", missing))
	}
	after, err := readDebianKernelSnapshot()
	if err != nil {
		return "", err
	}
	for p := range s.versions {
		mark := ""
		if s.auto[p] && !after.auto[p] {
			mark = "auto"
		} else if !s.auto[p] && after.auto[p] {
			mark = "manual"
		}
		if mark != "" {
			if _, err := runEnv("apt-mark", mark, p); err != nil {
				return "", err
			}
			did = append(did, "marked "+p+" "+mark)
		}
	}
	final, err := readDebianKernelSnapshot()
	if err != nil {
		return "", err
	}
	if final.String() != s.String() {
		return "", fmt.Errorf("the kernel set was not put back.\nbefore:\n%s\nafter:\n%s", s, final)
	}
	return strings.Join(did, "; "), nil
}

func (s debianKernelSnapshot) restore(t *testing.T) {
	t.Helper()
	did, err := s.putBack()
	if err != nil {
		t.Error(err)
		return
	}
	t.Logf("kernel set put back (%s):\n%s", did, s)
}

// olderDebianKernel is the newest image of the running flavour that the
// repositories offer, is not installed, and is older than the running
// kernel: one this test can install and remove without going near
// anything the host boots.
func olderDebianKernel(t *testing.T, running, flavour string) (string, bool) {
	t.Helper()
	runningVersion := dpkgVersion(t, "linux-image-"+running)
	out := kpRun(t, "apt-cache", "pkgnames", "linux-image-")
	best, bestVersion, bestHeaders := "", "", false
	for _, pkg := range strings.Fields(out) {
		if strings.HasSuffix(pkg, "-unsigned") {
			continue
		}
		rel, ok := debianImageRelease(pkg, flavour)
		if !ok || dpkgVersion(t, pkg) != "" {
			continue
		}
		policy, _ := kpRunOut(t, "apt-cache", "policy", pkg)
		_, after, ok := strings.Cut(policy, "Candidate:")
		if !ok {
			continue
		}
		v := strings.TrimSpace(strings.SplitN(after, "\n", 2)[0])
		if v == "(none)" || !dpkgGreater(t, runningVersion, v) {
			continue
		}
		// A kernel whose headers the archive still has is preferred,
		// because the headers are what hold the unsigned twin in.
		headers := candidateOf(t, "linux-headers-"+rel) != ""
		if best == "" || (headers && !bestHeaders) || (headers == bestHeaders && dpkgGreater(t, v, bestVersion)) {
			best, bestVersion, bestHeaders = rel, v, headers
		}
	}
	return best, best != ""
}

// candidateOf is apt's candidate version of a package, or "".
func candidateOf(t *testing.T, pkg string) string {
	t.Helper()
	policy, _ := kpRunOut(t, "apt-cache", "policy", pkg)
	_, after, ok := strings.Cut(policy, "Candidate:")
	if !ok {
		return ""
	}
	v := strings.TrimSpace(strings.SplitN(after, "\n", 2)[0])
	if v == "(none)" {
		return ""
	}
	return v
}

// remove, cleanup and their refusals, on an older kernel the test
// installs for the purpose.
func TestLiveKernelpkgRemovesAnOlderKernelAndRefusesTheRest(t *testing.T) {
	c, f := kernelpkgMutateGate(t)
	if _, ok := f.(aptKernels); !ok {
		t.Skipf("this host's kernels are %s's", f.name())
	}
	running := procRelease(t)
	flavour, _ := debianFlavour(running)
	if dpkgVersion(t, "linux-image-"+running) == "" {
		t.Skipf("the running kernel %s is not a package here (a container?), so nothing can be "+
			"compared against it", running)
	}
	old, ok := olderDebianKernel(t, running, flavour)
	if !ok {
		t.Skipf("the repositories offer no %s kernel older than %s that is not installed", flavour, running)
	}
	snap := takeDebianKernelSnapshot(t)
	t.Logf("before:\n%s", snap)
	t.Cleanup(func() { snap.restore(t) })

	// The headers come too, when the archive has them, because they are
	// what makes the twin trap reachable: linux-headers-R depends on
	// `linux-image-R | linux-image-R-unsigned`, so purging the signed
	// image alone is answered by installing the unsigned one. A kernel
	// installed without its headers has nothing holding the twin in.
	pkgs := []string{"linux-image-" + old}
	if candidateOf(t, "linux-headers-"+old) != "" {
		pkgs = append(pkgs, "linux-headers-"+old)
	} else {
		t.Logf("the archive has no linux-headers-%s, so nothing holds the unsigned twin in and that trap "+
			"is not exercised here", old)
	}
	t.Logf("installing %v for the test", pkgs)
	install := func() {
		kpRun(t, append([]string{"apt-get", "install", "-y", "-q"}, pkgs...)...)
		if _, err := os.Stat("/boot/vmlinuz-" + old); err != nil {
			t.Fatalf("the setup installed %s and /boot has no kernel for it: %v", old, err)
		}
	}
	gone := func() {
		t.Helper()
		if _, err := os.Stat("/boot/vmlinuz-" + old); err == nil {
			t.Errorf("/boot/vmlinuz-%s is still there", old)
		}
		for _, p := range []string{"linux-image-" + old, "linux-image-" + old + "-unsigned"} {
			if v := dpkgVersion(t, p); v != "" {
				t.Errorf("%s is installed (%s) after the removal", p, v)
			}
		}
	}
	install()

	installed := anyStrings(kpMust(t, c, "list_installed", nil))
	if len(installed) == 0 || installed[0] != old {
		t.Fatalf("list_installed = %v; the older kernel %s should be first", installed, old)
	}

	// The running kernel, refused before apt is asked.
	if _, err := kpCall(t, c, "remove", value.MapOf("release", running)); err == nil ||
		!strings.Contains(err.Error(), "running kernel") {
		t.Errorf("removing the running kernel: %v", err)
	}
	// The kernel the metapackage depends on, when that is not the running
	// one: refused, naming the metapackage.
	meta := "linux-image-" + flavour
	newest := installed[len(installed)-1]
	if newest != running && dpkgVersion(t, meta) != "" {
		_, err := kpCall(t, c, "remove", value.MapOf("release", newest))
		if err == nil || !strings.Contains(err.Error(), meta) {
			t.Errorf("removing %s, which %s depends on: %v", newest, meta, err)
		}
		_, err = kpCall(t, c, "cleanup", value.MapOf("keep_latest", false))
		if err == nil || !strings.Contains(err.Error(), "nothing was removed") {
			t.Errorf("cleanup keep_latest=false: %v", err)
		}
		if _, err := os.Stat("/boot/vmlinuz-" + old); err != nil {
			t.Errorf("a refused cleanup removed %s", old)
		}
		t.Logf("removal of %s refused: %v", newest, err)
	}

	// Test mode names what would go and removes nothing.
	tc := liveRoot()
	tc.Grains, tc.Test = c.Grains, true
	got, err := kpCall(t, tc, "remove", value.MapOf("release", old))
	if err != nil {
		t.Fatalf("remove in test mode: %v", err)
	}
	if removed := anyStrings(removedOf(got)); !onlyKernel(removed, old) {
		t.Errorf("remove in test mode would remove %v", removed)
	}
	if dpkgVersion(t, "linux-image-"+old) == "" {
		t.Fatal("remove in test mode removed the kernel")
	}

	got = kpMust(t, c, "remove", value.MapOf("release", old))
	t.Logf("remove: %v", removedOf(got))
	gone()
	if dpkgVersion(t, meta) == "" && snap.versions[meta] != "" {
		t.Errorf("removing %s removed %s", old, meta)
	}

	// cleanup with the newest kept removes the older kernel and nothing else.
	install()
	got = kpMust(t, c, "cleanup", nil)
	removed := anyStrings(removedOf(got))
	t.Logf("cleanup: %v", removed)
	if !onlyKernel(removed, old) {
		t.Errorf("cleanup removed %v", removed)
	}
	gone()
	for _, k := range installed[1:] {
		if _, err := os.Stat("/boot/vmlinuz-" + k); err != nil {
			t.Errorf("cleanup removed %s, which it should have kept", k)
		}
	}
}

// upgrade installs the newest kernel the repositories offer, after the
// test has taken it away, and a second call has nothing to do.
func TestLiveKernelpkgUpgradeInstallsTheNewest(t *testing.T) {
	c, f := kernelpkgMutateGate(t)
	if _, ok := f.(aptKernels); !ok {
		t.Skipf("this host's kernels are %s's", f.name())
	}
	running := procRelease(t)
	avail := value.KeyString(kpMust(t, c, "latest_available", nil))
	if avail == running {
		t.Skipf("the newest kernel offered, %s, is the running one, so there is nothing this test may "+
			"take away to put back", avail)
	}
	snap := takeDebianKernelSnapshot(t)
	t.Cleanup(func() { snap.restore(t) })
	debianTakeAway(t, avail)

	tc := liveRoot()
	tc.Grains, tc.Test = c.Grains, true
	got, err := kpCall(t, tc, "upgrade", nil)
	if err != nil {
		t.Fatal(err)
	}
	ups, _ := got.(*value.Map).Get("upgrades")
	if _, ok := ups.(*value.Map).Get("linux-image-" + avail); !ok {
		t.Errorf("upgrade in test mode would install %v, not linux-image-%s", value.KeyString(ups), avail)
	}
	if _, err := os.Stat("/boot/vmlinuz-" + avail); err == nil {
		t.Fatal("upgrade in test mode installed the kernel")
	}

	got = kpMust(t, c, "upgrade", nil)
	if b, err := value.EncodeJSON(got, 0); err == nil {
		t.Logf("upgrade: %s", b)
	}
	if _, err := os.Stat("/boot/vmlinuz-" + avail); err != nil {
		t.Fatalf("upgrade did not put %s in /boot: %v", avail, err)
	}
	if latest := kpMust(t, c, "latest_installed", nil); latest != avail {
		t.Errorf("latest_installed = %v after upgrading to %s", latest, avail)
	}
	if kpMust(t, c, "upgrade_available", nil) != false {
		t.Error("upgrade_available is still true after the upgrade")
	}
	if kpMust(t, c, "needs_reboot", nil) != true {
		t.Errorf("needs_reboot is false with %s installed and %s running", avail, running)
	}
	got = kpMust(t, c, "upgrade", nil)
	ups, _ = got.(*value.Map).Get("upgrades")
	if ups.(*value.Map).Len() != 0 {
		t.Errorf("a second upgrade installed %s", value.KeyString(ups))
	}
}

// debianTakeAway purges a non-running kernel and its twin directly, the
// way this module refuses to, so that a test can watch it come back.
func debianTakeAway(t *testing.T, release string) {
	t.Helper()
	if release == procRelease(t) {
		t.Fatalf("refusing to take away the running kernel %s", release)
	}
	if err := takeAwayDebianKernel(release); err != nil {
		t.Fatal(err)
	}
}

// takeAwayDebianKernel purges a kernel's packages, named the way
// `kernelpkg.remove` names them -- which on Debian includes the unsigned
// twin, so that apt does not install it in the image's place -- without
// the module's refusal to take the metapackages with it.
func takeAwayDebianKernel(release string) error {
	targets, err := debianRemovalTargets(liveRoot(), kernelImage{Release: release, Packages: []string{"linux-image-" + release}})
	if err != nil {
		return err
	}
	if _, err := runEnv(append([]string{"apt-get", "purge", "-y", "-q"}, targets...)...); err != nil {
		return err
	}
	if _, err := os.Stat("/boot/vmlinuz-" + release); err == nil {
		return fmt.Errorf("%s was purged and /boot still has it", release)
	}
	return nil
}
