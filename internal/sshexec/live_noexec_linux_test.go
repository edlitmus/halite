//go:build linux

package sshexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/roster"
)

// The staging directory refused by a filesystem that really is mounted
// `noexec`.
//
// # What was missing
//
// prepare_test.go drives the refusal through a stand-in ssh that is
// told to exit 126, which proves the message is right for a status and
// proves nothing about where that status comes from. The claim underneath
// it -- that a `noexec` mount is what a shell answers 126 to, and that
// the probe reaches that answer rather than succeeding or failing
// earlier -- had never been tested, because making such a mount needs a
// hardened host and root. That was plan.md item 19, and its own test
// file said so.
//
// # Why it needs no privilege
//
// A mount namespace entered through `unshare --mount --map-root-user`
// is uid 0 over a mount table that is discarded when the process exits,
// and a tmpfs can be mounted in one without any privilege on the host.
// So this runs wherever unprivileged user namespaces do, which is the
// same precondition the iptables and nftables live tests already
// depend on, and nothing outside the namespace can see the mount. If
// the kernel refuses, the test skips rather than pretending.
//
// # What it establishes
//
// That the real script, through a real `/bin/sh`, against a real
// `noexec` filesystem, gets as far as the probe and is refused there --
// exit 126, not the 1 of a directory that could not be made or the 2 of
// one that could not be written to -- and that `prepare` turns that into
// the message naming `noexec` and `thin_dir`. The control case is the
// half that makes it mean something: the identical directory, mounted
// without `noexec`, is accepted.

// mountnsReexec re-runs the calling test inside a private mount
// namespace and reports the child's result as this test's. It returns
// only when the process is already inside the namespace.
func mountnsReexec(t *testing.T) {
	t.Helper()
	if os.Getenv("HALITE_MOUNTNS_INNER") == "1" {
		return // already inside; run the body
	}
	unshare, err := exec.LookPath("unshare")
	if err != nil {
		t.Skip("this host has no `unshare`")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("cannot find the test binary: %v", err)
	}
	cmd := exec.Command(unshare, "--mount", "--map-root-user", "--",
		self, "-test.run", "^"+t.Name()+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), "HALITE_MOUNTNS_INNER=1")
	out, err := cmd.CombinedOutput()
	text := string(out)

	// unshare failing to set up the namespace is an environment
	// limitation, not a test failure: a kernel with user namespaces
	// turned off, or an AppArmor profile that withholds them.
	if err != nil && !strings.Contains(text, "--- FAIL") &&
		(strings.Contains(text, "unshare: ") ||
			strings.Contains(text, "Operation not permitted") ||
			strings.Contains(text, "clone failed")) {
		t.Skipf("this kernel will not give an unprivileged mount namespace:\n%s", text)
	}
	if err != nil {
		t.Fatalf("inside the mount namespace:\n%s", text)
	}
	t.Logf("inside the mount namespace:\n%s", text)
}

// mountTmpfs mounts a tmpfs over dir with the given options, or skips.
//
// The unmount is registered rather than left to the namespace going
// away. Both are true -- the mount is invisible outside and disappears
// with the process -- but `t.TempDir` removes its directory while the
// process is still alive, and a mount point that is still mounted is
// busy, which fails the test after its body has passed.
func mountTmpfs(t *testing.T, dir, opts string) {
	t.Helper()
	cmd := exec.Command("mount", "-t", "tmpfs", "-o", opts, "tmpfs", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot mount a tmpfs with %q in this namespace: %v\n%s", opts, err, out)
	}
	t.Cleanup(func() {
		if out, err := exec.Command("umount", dir).CombinedOutput(); err != nil {
			t.Errorf("unmounting %s: %v\n%s", dir, err, out)
		}
	})
}

func TestANoexecStagingDirectoryIsRefusedByTheProbe(t *testing.T) {
	mountnsReexec(t)

	dir := filepath.Join(t.TempDir(), "halite-thin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("making the mount point: %v", err)
	}
	mountTmpfs(t, dir, "noexec,nosuid,nodev")

	out, code := runScript(t, prepareScript(dir))

	// Not 0: the probe must not appear to have run. Not 1 or 2 either:
	// those are the directory failing to be made or written to, and
	// both of those succeed here -- which is the entire point, and the
	// reason `mkdir -p` was never an answer.
	switch code {
	case 0:
		t.Fatalf("a noexec staging directory was accepted:\n%s", out)
	case 1:
		t.Fatalf("the directory could not be made, so the probe was never reached:\n%s", out)
	case 2:
		t.Fatalf("the probe could not be written, so it was never executed:\n%s", out)
	}
	// 126 is what a POSIX shell reports for a file it found and could
	// not execute. It is asserted rather than merely accepted, because
	// it is the status the switch in `prepare` falls through on.
	if code != 126 {
		t.Errorf("a noexec mount produced status %d, want 126:\n%s", code, out)
	}

	// The probe is removed whatever happened, even on the filesystem
	// that refused to run it.
	if _, err := os.Stat(filepath.Join(dir, ".halite-probe")); !os.IsNotExist(err) {
		t.Errorf("the probe file was left behind on a noexec mount: %v", err)
	}
}

// The control. Without `noexec` and with everything else identical, the
// same directory on the same kind of filesystem is accepted -- so the
// refusal above is about the mount option and not about tmpfs, the
// namespace, or the directory.
func TestTheSameDirectoryWithoutNoexecIsAccepted(t *testing.T) {
	mountnsReexec(t)

	dir := filepath.Join(t.TempDir(), "halite-thin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("making the mount point: %v", err)
	}
	mountTmpfs(t, dir, "nosuid,nodev")

	if out, code := runScript(t, prepareScript(dir)); code != 0 {
		t.Fatalf("an executable staging directory exited %d:\n%s", code, out)
	}
}

// localSH stands in for ssh by running the script it is handed on this
// machine, so `prepare` drives the real shell against the real mount
// instead of a canned exit status.
func localSH(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "local-ssh")
	// The last argument is the remote command -- "/bin/sh -s" -- and
	// the script arrives on stdin. Everything before it is ssh's own
	// options and the destination, which mean nothing here.
	script := "#!/bin/sh\nexec /bin/sh -s\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing the stand-in: %v", err)
	}
	return path
}

// End to end: prepare, against a real noexec filesystem, produces the
// message an operator is meant to act on.
func TestPrepareNamesNoexecOnARealMount(t *testing.T) {
	mountnsReexec(t)

	dir := filepath.Join(t.TempDir(), "halite-thin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("making the mount point: %v", err)
	}
	mountTmpfs(t, dir, "noexec,nosuid,nodev")

	o := &Options{SSH: localSH(t)}
	target := roster.Target{ID: "web1.example", Host: "web1.example", ThinDir: dir}

	err := o.prepare(context.Background(), target, dir)
	if err == nil {
		t.Fatal("a target whose staging directory is noexec was accepted")
	}
	for _, want := range []string{"web1.example", dir, "noexec", "thin_dir"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q:\n%v", want, err)
		}
	}
}
