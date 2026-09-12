//go:build unix

// The tests here stand a POSIX shell in for the target, which is what
// the script being tested talks to, so they build where there is one.
// A Windows operator can drive an agentless run -- the target is the
// unix machine, not the one running the command -- and what CI's
// Windows leg found by running these anyway is in `prepare`: a failure
// to start ssh at all was being reported as a filesystem mounted
// `noexec`, which is a confident answer to a question nobody asked.
// That defect is fixed and its test runs everywhere this file does.

package sshexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/roster"
)

// The staging directory of SPEC 21.1, and the question `mkdir -p` does
// not answer.
//
// An agentless run caches the pushed binary under `thin_dir` and
// executes it from there. A host hardened to a benchmark commonly
// mounts `/var/tmp` `noexec`, which is where that directory lives by
// default, and every step of the run up to the last one succeeds on
// such a host: the directory is made, the binary copies, the digest
// verifies, and then it does not run.
//
// The script is run here through a real `/bin/sh` against a real
// directory, because the shell is the thing being programmed and a
// script checked only against what its author meant is what DIVERGENCE
// 5.31 is about. What cannot be checked here is a `noexec` mount, which
// needs a hardened host and root to create; that half is plan.md's item
// 19, and what this file covers is that the refusal, when it comes,
// says something an operator can act on.

// runScript runs a prepared script the way the target would: over stdin
// to /bin/sh.
func runScript(t *testing.T, script string) (string, int) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("running the script: %v", err)
	}
	return out.String(), cmd.ProcessState.ExitCode()
}

func TestThePrepareScriptMakesTheDirectoryAndProvesItRuns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "halite-thin")

	out, code := runScript(t, prepareScript(dir))
	if code != 0 {
		t.Fatalf("the script exited %d: %s", code, out)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("the directory was not made: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the directory is mode %o, want 700", perm)
	}

	// The probe is removed whatever happened, so a run leaves nothing
	// behind in a directory an operator may be watching.
	if _, err := os.Stat(filepath.Join(dir, ".halite-probe")); !os.IsNotExist(err) {
		t.Errorf("the probe file is still there: %v", err)
	}
}

// A directory that cannot be made is a different failure from one that
// can be made and not written to, and both are different from one that
// will not execute. The exit statuses are what tell them apart, so they
// are asserted rather than assumed.
func TestThePrepareScriptSeparatesItsFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		// Root writes through the permission bits, so neither failure
		// below can be produced. Skipping is honest here, and the cases
		// are covered wherever the suite runs unprivileged, which is
		// every machine this project develops on.
		t.Skip("running as root, which writes through the permission bits")
	}

	// Cannot be created: a directory under one that permits no writing.
	readOnly := t.TempDir()
	if err := os.Chmod(readOnly, 0o500); err != nil {
		t.Fatalf("making a read-only directory: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })

	if _, code := runScript(t, prepareScript(filepath.Join(readOnly, "nested", "thin"))); code != 1 {
		t.Errorf("an uncreatable directory exited %d, want 1", code)
	}

	// Can be created and not written into. Worth saying how this is
	// reached, because the obvious way does not work: `chmod 700` on a
	// directory the caller owns *repairs* it, so a staging directory
	// whose permissions are wrong and whose owner is the caller is not
	// a failure at all. It is a failure when the directory belongs to
	// somebody else, which cannot be arranged here without root -- so
	// the write is blocked instead by the probe's own name already
	// being taken by something that cannot be written over.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".halite-probe"), 0o700); err != nil {
		t.Fatalf("blocking the probe name: %v", err)
	}
	if _, code := runScript(t, prepareScript(dir)); code != 2 {
		t.Errorf("a directory that cannot hold the probe exited %d, want 2", code)
	}
}

// fakeSSH writes a stand-in for ssh that exits with a status of the
// test's choosing, so the refusal can be read without a target.
func fakeSSH(t *testing.T, status int, stderr string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ssh")
	script := "#!/bin/sh\n" +
		"cat >/dev/null\n" +
		"echo " + shellQuote(stderr) + " >&2\n" +
		"exit " + strconv.Itoa(status) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stand-in: %v", err)
	}
	return path
}

// A target that will not execute out of its staging directory has to be
// told so, in terms that name the directory and the usual cause. The
// alternative, which is what this used to do, is a bare "Permission
// denied" about a binary that was installed successfully a moment
// earlier.
func TestATargetThatWillNotExecuteIsToldWhy(t *testing.T) {
	// 126 is what a shell reports for a file it found and could not
	// execute, which is what a `noexec` mount produces.
	o := &Options{SSH: fakeSSH(t, 126, "/var/tmp/halite-thin/.halite-probe: Permission denied")}
	target := roster.Target{ID: "web1.example", Host: "web1.example", ThinDir: "/var/tmp/halite-thin"}

	err := o.prepare(context.Background(), target, target.ThinDir)
	if err == nil {
		t.Fatal("a target that refused to run the probe was accepted")
	}
	for _, want := range []string{"web1.example", "/var/tmp/halite-thin", "noexec", "thin_dir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%v", want, err)
		}
	}
}

// A target that could not be reached at all is a different answer
// again. Nothing has been learned about the directory when the command
// never ran, and saying `noexec` there is the mistake CI found.
func TestATargetThatCouldNotBeReachedIsNotDiagnosed(t *testing.T) {
	o := &Options{SSH: filepath.Join(t.TempDir(), "no-such-ssh")}
	target := roster.Target{ID: "web1.example", Host: "web1.example", ThinDir: "/var/tmp/halite-thin"}

	err := o.prepare(context.Background(), target, target.ThinDir)
	if err == nil {
		t.Fatal("a target whose ssh does not exist was accepted")
	}
	if strings.Contains(err.Error(), "noexec") {
		t.Errorf("an ssh that never ran was diagnosed as a filesystem: %v", err)
	}
	if !strings.Contains(err.Error(), "preparing /var/tmp/halite-thin") {
		t.Errorf("the error does not say what was being attempted: %v", err)
	}
}

// The two earlier statuses keep their own messages, so an operator is
// not told about `noexec` when the directory could not be made at all.
func TestTheEarlierFailuresKeepTheirOwnMessages(t *testing.T) {
	target := roster.Target{ID: "web1.example", Host: "web1.example", ThinDir: "/var/tmp/halite-thin"}

	for _, tc := range []struct {
		status  int
		want    string
		notWant string
	}{
		{1, "could not be created", "noexec"},
		{2, "nothing could be written", "noexec"},
	} {
		o := &Options{SSH: fakeSSH(t, tc.status, "mkdir: Permission denied")}
		err := o.prepare(context.Background(), target, target.ThinDir)
		if err == nil {
			t.Fatalf("status %d was accepted", tc.status)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("status %d reads %q, want it to mention %q", tc.status, err, tc.want)
		}
		if strings.Contains(err.Error(), tc.notWant) {
			t.Errorf("status %d mentions %q, which is the wrong diagnosis: %v", tc.status, tc.notWant, err)
		}
	}
}
