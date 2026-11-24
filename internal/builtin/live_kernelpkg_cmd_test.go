package builtin

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// How long one package command in the kernelpkg live tests may run.
//
// Every apt, dpkg, dnf and rpm command those tests run used to have no
// limit at all. On 2026-11-24 an Ubuntu mirror served the fleet's AWS
// runners at 10 to 100 kB/s for four hours, the kernel tests' 370 MB of
// downloads ran into the suite's 40-minute alarm four times, and the alarm
// took every later live test in the package with it (issue 852, DIVERGENCE
// 5.729). It named the alarm, not apt, and the leg's output arrives all at
// once at the end, so what apt was doing was never seen.
//
// Ten minutes is a judgement, and is written down as one. The removal
// test passes in under a minute on a normal mirror, and its download is
// about 184 MB, which ten minutes allows at about 300 kB/s. It is short
// enough to fail on its own terms within the alarm. A slower mirror now
// fails the test naming apt and showing how far it got, where it used to
// pass slowly or take the package with it.
const kernelpkgCommandLimit = 10 * time.Minute

// runBounded runs a package command non-interactively, for at most limit,
// and returns everything it printed.
//
// On the limit it stops the command and every process the command
// started, and the error names the command, how long it ran, and the end
// of what it printed. Stopping only the command is not enough: apt runs
// dpkg, dpkg runs the maintainer scripts, a grandchild still holding the
// output pipe keeps CombinedOutput waiting for as long as it lives, and a
// dpkg left behind would hold the lock that the test's own cleanup needs
// next. WaitDelay is there for anything that escaped the group anyway.
func runBounded(limit time.Duration, argv ...string) (string, error) {
	return runBoundedIn("", limit, argv...)
}

// errStoppedAtLimit is what runBounded's error wraps when the command ran
// out of time, as against failing on its own.
var errStoppedAtLimit = errors.New("stopped at its time limit")

// runBoundedIn is runBounded in the directory dir ("" for this one).
func runBoundedIn(dir string, limit time.Duration, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	argv = aptNonInteractive(argv)
	cmd := osexec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	stopTreeOnCancel(cmd)
	cmd.WaitDelay = 10 * time.Second
	start := time.Now()
	out, err := cmd.CombinedOutput()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return string(out), fmt.Errorf("%s: still running after %s, so it and everything it started were "+
			"%w. The last of what it printed:\n%s", strings.Join(argv, " "),
			time.Since(start).Round(time.Second), errStoppedAtLimit, outputTail(string(out), 40))
	}
	if err != nil {
		return string(out), fmt.Errorf("%s: %v: %s", strings.Join(argv, " "), err, out)
	}
	return string(out), nil
}

// aptNonInteractive adds to an apt-get command what DEBIAN_FRONTEND does
// not cover. DEBIAN_FRONTEND answers debconf's questions; a changed
// configuration file is asked about by dpkg itself, which --force-confdef
// and --force-confold answer with the package's default where it has one
// and the installed copy where it does not. The lock timeout bounds how
// long apt waits for a dpkg lock that something else holds -- a runner's
// own unattended-upgrades, or a command this file stopped -- at two
// minutes, inside runBounded's own limit.
func aptNonInteractive(argv []string) []string {
	if len(argv) == 0 || argv[0] != "apt-get" {
		return argv
	}
	out := []string{argv[0],
		"-o", "DPkg::Lock::Timeout=120",
		"-o", "Dpkg::Options::=--force-confdef",
		"-o", "Dpkg::Options::=--force-confold",
	}
	return append(out, argv[1:]...)
}

// outputTail is the last n lines of s, line by line, saying how many came
// before. (pkg_zypper.go's lastLines joins them on one line, for a message.)
func outputTail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return fmt.Sprintf("[%d lines before these]\n%s", len(lines)-n, strings.Join(lines[len(lines)-n:], "\n"))
}

// ---- is the archive fast enough to run these tests at all? ----
//
// The ten-minute limit makes one slow command fail on its own terms, but it
// does not hold the leg on a night like issue 852's. The conformance case
// installs the newest kernel through the module, whose commands this file
// does not run, and it alone took 25 minutes; the removal test then needed
// another ten to fail. So before the first kernel is taken away or fetched,
// the archive is measured once, by apt itself, and on a slow one every
// test that would download a kernel refuses at once, saying how slow.
//
// apt does the fetching because only apt knows where it fetches from: the
// fleet's runners name their mirrors through `mirror+file:` lists, which a
// plain HTTP client would have to reimplement and could get wrong.

const (
	// The slowest archive these tests will start on. The 38 linux-leg runs
	// of DIVERGENCE 5.729 split cleanly: on a normal night the removal test
	// fetched its 184 MB and installed it in under 35 s, so faster than 5
	// MB/s; on 2026-11-24 the one that finished averaged about 0.17 MB/s.
	// 1 MB/s is well clear of both, and at it the kernel downloads of the
	// whole package, about 370 MB on Ubuntu 24.04's -aws kernels, take six
	// minutes.
	kernelArchiveMinRate = 1 << 20 // bytes a second

	// How long the measurement may take. The running kernel's image is
	// 16.7 MB on those runners, so three minutes is 95 kB/s: a measurement
	// that has not finished by then has its answer.
	kernelArchiveProbeLimit = 3 * time.Minute
)

var kernelArchive struct {
	once sync.Once
	note string
	err  error
}

// kernelArchiveCheck measures the package archive once per test binary and
// returns what it found, and an error when it is too slow for the kernel
// tests. A measurement that cannot be made -- the running kernel's image is
// no longer in the archive, say -- is noted and not refused: the ten-minute
// limit is still there behind it.
func kernelArchiveCheck() (string, error) {
	kernelArchive.once.Do(func() {
		kernelArchive.note, kernelArchive.err = measureKernelArchive()
	})
	return kernelArchive.note, kernelArchive.err
}

func measureKernelArchive() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return "the archive was not measured: " + err.Error(), nil
	}
	pkg := "linux-image-" + strings.TrimSpace(string(b))
	dir, err := os.MkdirTemp("", "halite-archive-probe-")
	if err != nil {
		return "the archive was not measured: " + err.Error(), nil
	}
	defer os.RemoveAll(dir)
	start := time.Now()
	_, err = runBoundedIn(dir, kernelArchiveProbeLimit, "apt-get", "download", pkg)
	took := time.Since(start)
	if errors.Is(err, errStoppedAtLimit) {
		return "", fmt.Errorf("the package archive is too slow for the kernel tests, which download about "+
			"370 MB: fetching %s, one kernel image, did not finish in %s (DIVERGENCE 5.729). %v",
			pkg, kernelArchiveProbeLimit, err)
	}
	if err != nil {
		return "the archive was not measured: " + err.Error(), nil
	}
	debs, _ := filepath.Glob(filepath.Join(dir, "*.deb"))
	var size int64
	for _, d := range debs {
		if fi, err := os.Stat(d); err == nil {
			size += fi.Size()
		}
	}
	return judgeKernelArchive(pkg, size, took)
}

// judgeKernelArchive says whether size bytes in took is fast enough.
func judgeKernelArchive(pkg string, size int64, took time.Duration) (string, error) {
	if size == 0 || took <= 0 {
		return fmt.Sprintf("the archive was not measured: apt fetched %d bytes of %s", size, pkg), nil
	}
	rate := float64(size) / took.Seconds()
	what := fmt.Sprintf("%s, %.1f MB, in %s: %.0f kB/s", pkg, float64(size)/1e6, took.Round(time.Millisecond), rate/1e3)
	if rate < kernelArchiveMinRate {
		return "", fmt.Errorf("the package archive is too slow for the kernel tests: it delivered %s, and they "+
			"download about 370 MB, which at that rate is %s. They need %d kB/s (DIVERGENCE 5.729)",
			what, (time.Duration(370e6/rate) * time.Second).Round(time.Minute), kernelArchiveMinRate/1000)
	}
	return "the package archive delivered " + what, nil
}
