package builtin

import (
	"context"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	hexec "github.com/edlitmus/halite/internal/exec"
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
	// Bounded like every other dnf here, and with Error rather than
	// Fatal: this runs as a cleanup, after a step may already have been
	// killed for its time, and what it has to say is still worth saying.
	if len(extra) > 0 {
		if err := kpStep(t, "putting back: dnf remove", kpPutBackLimit, func(ctx context.Context) error {
			return kpDNF(ctx, append([]string{binary, "remove", "-y", "-q", "--noautoremove"}, extra...)...)
		}); err != nil {
			t.Error(err)
		}
	}
	if len(missing) > 0 {
		if err := kpStep(t, "putting back: dnf install", kpPutBackLimit, func(ctx context.Context) error {
			return kpDNF(ctx, append([]string{binary, "install", "-y", "-q"}, missing...)...)
		}); err != nil {
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

// # Why every dnf in the install test is bounded
//
// The rocky9 leg failed three times in a day (two PR runs on 2026-10-15,
// main's scheduled run on 2026-10-16) the same way: the test logged its
// plan, then `dnf install -y -q kernel-R` sat for the rest of `go test`'s
// thirty minutes and the binary panicked. The passing runs either side
// of them installed the same kernel from the same image in about a
// minute; the whole test took 99s. Nothing in the log said which half of
// the install had stopped -- the download from a mirror, or the
// scriptlets that run depmod and dracut -- because the command's output
// was being collected, not printed, and died with the binary.
//
// The panic was not the end of it. go test exits without killing the
// children it started, so that dnf lived on in the container, holding
// dnf's lock, and the conformance harness that run.sh starts next waited
// on that lock in `pkg.installed` until the job's own timeout cancelled
// the leg fourteen minutes later. One stall cost two test binaries and
// the job. DIVERGENCE 5.319.
//
// So each step that can reach a mirror or run a scriptlet runs under a
// limit of a few minutes -- several times what it takes when it works --
// and is killed with its whole process group when the limit passes,
// through the same OSRunner the module runs dnf with. Before the kill,
// what is running and what dnf last wrote to its logs is printed,
// because the next stall is the only evidence there will be of where
// the time goes. And the download is a step of its own, `--downloadonly`
// with a minimum rate (kpMinRate) and one retry (kpDownload), so that a
// slow mirror is told apart from a slow install by which step names
// itself, and a mirror that stalls once costs minutes rather than the
// leg.
const (
	kpDownloadLimit = 4 * time.Minute
	kpInstallLimit  = 5 * time.Minute
	kpQuickLimit    = 2 * time.Minute
	kpPutBackLimit  = 4 * time.Minute

	// kpMinRate is the speed below which dnf abandons a transfer when it
	// has lasted `timeout` seconds (30 by default). dnf's own default is
	// 1000 bytes a second, which a mirror trickling a 100MB kernel can
	// stay above for a day. A runner here downloads EL's metadata at tens
	// of MB/s (the image build logs 15-64 MB/s for BaseOS), so 200k is far
	// below a healthy transfer and far above a stuck one. It applies only
	// to the test's own downloads; the module's dnf runs with the host's
	// configuration, as it does on a node.
	kpMinRate = "200k"
)

// kpStep runs fn under a limit and says how long it took. When the limit
// passes, it prints what the container is running and the tail of dnf's
// logs while the step is still alive, then cancels fn's context, which
// kills its process group, and returns an error naming the step.
//
// The limit is also held under the test binary's own deadline, less
// room for the put-back, so that the bounded steps cannot add up to the
// thirty minutes they exist to stop.
func kpStep(t *testing.T, step string, limit time.Duration, fn func(context.Context) error) error {
	t.Helper()
	if dl, ok := t.Deadline(); ok {
		left := time.Until(dl) - kpPutBackLimit - time.Minute
		if left <= 0 {
			return fmt.Errorf("%s: not started, %s before go test's deadline is too little to run it and put the kernels back", step, time.Until(dl).Round(time.Second))
		}
		if left < limit {
			limit = left
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan struct{})
	timer := time.AfterFunc(limit, func() {
		defer close(fired)
		t.Logf("%s has run for %s; what is running and what dnf last logged, before it is killed:\n%s", step, limit, kpWhatIsRunning())
		cancel()
	})
	start := time.Now()
	err := fn(ctx)
	if !timer.Stop() {
		<-fired
		return fmt.Errorf("%s did not finish in %s and was killed with its process group. "+
			"A healthy run does this in well under a minute; the log above says whether dnf was still "+
			"downloading or was in a scriptlet (DIVERGENCE 5.319): %v", step, limit, err)
	}
	if err != nil {
		return fmt.Errorf("%s, after %s: %w", step, time.Since(start).Round(time.Second), err)
	}
	t.Logf("%s: %s", step, time.Since(start).Round(100*time.Millisecond))
	return nil
}

// kpDownload fetches a package and what it needs into dnf's cache,
// under kpMinRate, trying a second time if the first attempt runs out of
// time. A second `dnf` asks for the mirror list again rather than
// resuming on the transfer that stalled, so it is a new chance at a
// healthy mirror; a dnf that failed outright, rather than running out
// its time, is not retried, because that is an answer, not a stall.
func kpDownload(t *testing.T, binary, pkg string) {
	t.Helper()
	var err error
	for attempt := 1; attempt <= 2; attempt++ {
		var stalled bool
		err = kpStep(t, fmt.Sprintf("dnf download %s, attempt %d", pkg, attempt), kpDownloadLimit, func(ctx context.Context) error {
			err := kpDNF(ctx, binary, "install", "-y", "-q", "--downloadonly", "--setopt=minrate="+kpMinRate, pkg)
			stalled = ctx.Err() != nil
			return err
		})
		if err == nil || !stalled {
			break
		}
		t.Log(err)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// kpDNF runs one of the test's own dnf commands through the OSRunner,
// which puts the child in its own process group and kills the group when
// ctx ends.
func kpDNF(ctx context.Context, argv ...string) error {
	res, err := (&hexec.OSRunner{}).Run(ctx, hexec.Command{Argv: argv})
	if err != nil {
		return fmt.Errorf("%w\n--- stdout\n%s--- stderr\n%s", err, res.Stdout, res.Stderr)
	}
	return nil
}

// kpBoundedCall is a kernelpkg function under a step's limit: the
// module's dnf runs through c.Ctx, so cancelling it kills that dnf.
func kpBoundedCall(t *testing.T, step string, limit time.Duration, fn string, args *value.Map, test bool) any {
	t.Helper()
	var got any
	if err := kpStep(t, step, limit, func(ctx context.Context) error {
		c := liveRoot()
		c.Ctx = ctx
		c.Test = test
		c.Grains = value.MapOf("kernelrelease", procRelease(t))
		var err error
		got, err = kpCall(t, c, fn, args)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return got
}

// kpWhatIsRunning is every process in the container with its parent and
// the kernel function it is waiting in, read from /proc because the
// image need not have ps, and the last lines of dnf's three logs.
func kpWhatIsRunning() string {
	var b strings.Builder
	b.WriteString("--- processes (pid ppid wchan cmdline)\n")
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	sort.Slice(dirs, func(i, j int) bool {
		a, _ := strconv.Atoi(filepath.Base(dirs[i]))
		c, _ := strconv.Atoi(filepath.Base(dirs[j]))
		return a < c
	})
	for _, d := range dirs {
		stat, err := os.ReadFile(d + "/stat")
		if err != nil {
			continue
		}
		// The second field is the command in parentheses and may hold
		// spaces, so the fields after it are counted from the last ')'.
		ppid := "?"
		if i := strings.LastIndexByte(string(stat), ')'); i >= 0 {
			if f := strings.Fields(string(stat[i+1:])); len(f) > 1 {
				ppid = f[1]
			}
		}
		wchan, _ := os.ReadFile(d + "/wchan")
		cmdline, _ := os.ReadFile(d + "/cmdline")
		fmt.Fprintf(&b, "%s %s %s %s\n", filepath.Base(d), ppid, wchan, strings.ReplaceAll(strings.TrimRight(string(cmdline), "\x00"), "\x00", " "))
	}
	for _, log := range []string{"/var/log/dnf.librepo.log", "/var/log/dnf.log", "/var/log/dnf.rpm.log"} {
		data, err := os.ReadFile(log)
		if err != nil {
			fmt.Fprintf(&b, "--- %s: %v\n", log, err)
			continue
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		fmt.Fprintf(&b, "--- %s, last %d lines\n%s\n", log, len(lines), strings.Join(lines, "\n"))
	}
	return b.String()
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
		if err := kpStep(t, "dnf remove "+avail, kpInstallLimit, func(ctx context.Context) error {
			return kpDNF(ctx, r.binary, "remove", "-y", "-q", "kernel-core-"+avail+arch)
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Downloaded, then installed from the cache alone (-C), so the two
	// halves of an install are two steps with two times, and a mirror
	// that stalls is a step that says "download".
	kpDownload(t, r.binary, "kernel-"+old+arch)
	if err := kpStep(t, "dnf install "+old+" from the cache", kpInstallLimit, func(ctx context.Context) error {
		return kpDNF(ctx, r.binary, "install", "-y", "-q", "-C", "kernel-"+old+arch)
	}); err != nil {
		t.Fatal(err)
	}
	if got := anyStrings(mustCall(t, call, "list_installed", nil)); !strings.Contains(" "+strings.Join(got, " ")+" ", " "+old+" ") {
		t.Fatalf("list_installed = %v after installing %s", got, old)
	}
	if mustCall(t, call, "upgrade_available", nil) != true {
		t.Errorf("upgrade_available is false with %s installed and %s offered", old, avail)
	}

	// The module's own `upgrade` runs `dnf install` with the node's
	// configuration and no rate of the test's choosing, so what it will
	// download is fetched here first, under kpMinRate, and the upgrade
	// finds it in dnf's cache. The upgrade's time below says whether it
	// did: a download in it would show as the minute or so the first
	// install took before this was split.
	kpDownload(t, r.binary, "kernel-"+avail+arch)
	got := kpBoundedCall(t, "kernelpkg.upgrade", kpInstallLimit, "upgrade", nil, false)
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
	if again := kpBoundedCall(t, "kernelpkg.upgrade, again", kpQuickLimit, "upgrade", nil, false).(*value.Map); func() int { u, _ := again.Get("upgrades"); return u.(*value.Map).Len() }() != 0 {
		t.Errorf("a second upgrade installed something")
	}

	capture(t, r.binary, "remove", "--assumeno", "kernel-core-"+old+arch)
	plan := kpBoundedCall(t, "kernelpkg.remove in test mode", kpQuickLimit, "remove", value.MapOf("release", old), true)
	t.Logf("remove in test mode: %v", removedOf(plan))
	if rel := rpmKernelReleases(t); !strings.Contains(strings.Join(rel, " "), old) {
		t.Fatal("remove in test mode removed the kernel")
	}
	got = kpBoundedCall(t, "kernelpkg.remove", kpInstallLimit, "remove", value.MapOf("release", old), false)
	t.Logf("remove: %v", removedOf(got))
	if out, err := runEnv("rpm", "-q", "kernel-"+old, "kernel-core-"+old, "kernel-modules-"+old); err == nil ||
		strings.Count(out, "is not installed") != 3 {
		t.Errorf("after remove, rpm says:\n%s", out)
	}
	if _, err := os.Stat("/lib/modules/" + old + arch + "/vmlinuz"); err == nil {
		t.Errorf("/lib/modules still has %s after the removal", old)
	}
}
