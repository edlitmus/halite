//go:build unix

package builtin

import (
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// stopTreeOnCancel puts the command in a process group of its own and
// makes a cancel kill the whole group, so that dpkg and the maintainer
// scripts apt started go with it. internal/exec does the same for the
// commands states run (confineTree); this is the test's copy, because
// that one is not exported and carries the node's other limits with it.
func stopTreeOnCancel(cmd *osexec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// A package command that outlives its limit is stopped with everything it
// started, and says what it was and what it printed. Not a live test: it
// runs anywhere there is a shell, because it is the part of issue 852 that
// can be demonstrated without a runner whose mirror is slow.
//
// The command stands in for apt: a shell that prints, then starts a child
// that holds the output pipe and outlives it, as dpkg and a maintainer
// script do. Killing the shell alone leaves the child holding the pipe, and
// CombinedOutput then waits out WaitDelay -- ten seconds here, and without
// WaitDelay for as long as the child lived, which is how the leg met its
// 40-minute alarm.
func TestKernelpkgLiveCommandsStopAtTheirLimitWithEverythingTheyStarted(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	script := "echo fetching linux-image; sleep 300 & echo $! > " + pidFile + "; wait"
	start := time.Now()
	out, err := runBounded(time.Second, "sh", "-c", script)
	took := time.Since(start)

	if err == nil {
		t.Fatalf("a command that sleeps for five minutes finished inside a one-second limit: %q", out)
	}
	for _, want := range []string{"sh -c " + script, "still running after", "fetching linux-image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
	if took > 5*time.Second {
		t.Errorf("runBounded returned %s after a one-second limit; the child holding the pipe was not "+
			"stopped with the command, and only WaitDelay let it return", took.Round(time.Millisecond))
	}

	b, rerr := os.ReadFile(pidFile)
	if rerr != nil {
		t.Fatalf("the child never wrote its pid: %v", rerr)
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
	if perr != nil {
		t.Fatalf("the child's pid %q: %v", b, perr)
	}
	// Once killed, the child is reparented and reaped; until then signal 0
	// still finds it, so this waits a moment rather than asking once.
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the command's child %d was still alive after the command was stopped", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A command that fails inside its limit says how it failed, as before, and
// is not reported as stopped.
func TestKernelpkgLiveCommandsReportAnOrdinaryFailureAsOne(t *testing.T) {
	_, err := runBounded(time.Minute, "sh", "-c", "echo no such package; exit 100")
	if err == nil || !strings.Contains(err.Error(), "exit status 100") ||
		!strings.Contains(err.Error(), "no such package") || strings.Contains(err.Error(), "still running") {
		t.Errorf("an ordinary failure: %v", err)
	}
}

// apt-get, and nothing else, is given the options DEBIAN_FRONTEND does not
// cover, in front of its own arguments.
func TestKernelpkgLiveAptGetIsMadeNonInteractive(t *testing.T) {
	got := strings.Join(aptNonInteractive([]string{"apt-get", "install", "-y", "linux-image-x"}), " ")
	want := "apt-get -o DPkg::Lock::Timeout=120 -o Dpkg::Options::=--force-confdef " +
		"-o Dpkg::Options::=--force-confold install -y linux-image-x"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	for _, argv := range [][]string{{"dpkg-query", "-W"}, {"dnf", "install", "-y", "kernel"}} {
		if got := aptNonInteractive(argv); strings.Join(got, " ") != strings.Join(argv, " ") {
			t.Errorf("%v became %v", argv, got)
		}
	}
}

// A command stopped at its limit is told apart from one that failed, which
// is how the archive check knows a slow fetch from an absent package.
func TestKernelpkgLiveCommandsMarkAStopAsOne(t *testing.T) {
	if _, err := runBounded(time.Second, "sleep", "30"); !errors.Is(err, errStoppedAtLimit) {
		t.Errorf("a command stopped at its limit: %v", err)
	}
	if _, err := runBounded(time.Minute, "false"); err == nil || errors.Is(err, errStoppedAtLimit) {
		t.Errorf("a command that failed by itself: %v", err)
	}
}
