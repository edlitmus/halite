//go:build unix

package sshexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The target's umask, which the tests otherwise inherit from whoever runs
// them: 022 in CI, and 002 for an ordinary account on Ubuntu, where
// pam_umask gives one to every ssh session (measured over `ssh
// localhost`). DIVERGENCE 5.402.

// asTarget makes h's ssh run its scripts under umask, with prefix ahead
// of PATH when it is not empty.
func (h *harness) asTarget(umask, prefix string) {
	h.t.Helper()
	shell := os.Getenv("HALITE_TEST_TARGET_SH")
	if shell == "" {
		shell = "/bin/sh"
	}
	path := ""
	if prefix != "" {
		path = "PATH='" + prefix + "':$PATH; export PATH\n"
	}
	writeExec(h.t, h.o.SSH, "#!/bin/sh\numask "+umask+"\n"+path+
		"for a; do last=$a; done\n"+
		"if [ \"$last\" = '/bin/sh -s' ]; then exec "+shell+" -s; fi\n"+
		"exec "+shell+" -c \"$last\"\n")
}

// A thin_dir under a directory that does not exist yet: `mkdir -p` made
// the parent under the login's umask and nothing tightened it, so under
// 002 the parent was group-writable and every run refused it.
func TestAThinDirUnderAMissingDirectoryIsMadePrivate(t *testing.T) {
	parent := filepath.Join(privateTempDir(t), "missing")
	h := newHarness(t, filepath.Join(parent, "halite-thin"))
	h.asTarget("002", "")
	if r := h.run(); r.Err != nil {
		t.Fatalf("a run under umask 002: %v", r.Err)
	}
	info, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("the parent mkdir -p made is %o, want 700", mode)
	}
}

// The race the concurrent test found one run in 300: between `mkdir` and
// `chmod 700`, thin_dir had the umask's mode, and a second run's check
// refused it as group-writable. Made deterministic by a `chmod` that
// records the directory's mode at the moment it is called, which is the
// end of that window.
func TestTheThinDirIsPrivateBeforeItsChmod(t *testing.T) {
	real, err := exec.LookPath("chmod")
	if err != nil {
		t.Skip("no chmod on PATH")
	}
	shims := privateTempDir(t)
	log := filepath.Join(shims, "modes")
	writeExec(t, filepath.Join(shims, "chmod"), "#!/bin/sh\n"+
		"for a; do last=$a; done\n"+
		"ls -ldn \"$last\" >> '"+log+"'\n"+
		"exec '"+real+"' \"$@\"\n")

	dir := filepath.Join(privateTempDir(t), "halite-thin")
	h := newHarness(t, dir)
	h.asTarget("002", shims)
	if r := h.run(); r.Err != nil {
		t.Fatalf("a run under umask 002: %v", r.Err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the chmod shim was never called, so this proves nothing: %v", err)
	}
	var seen bool
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[len(fields)-1] != dir {
			continue
		}
		seen = true
		if fields[0][:10] != "drwx------" {
			t.Errorf("thin_dir was %s before its chmod; a second run's check would refuse it", fields[0])
		}
	}
	if !seen {
		t.Fatalf("chmod was never called on %s:\n%s", dir, raw)
	}
}
