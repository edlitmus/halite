package buildpolicy

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// `make lab-down` forgets the host keys the lab recorded, and only after a
// destroy that succeeded.
//
// lab.sh trusts an instance's SSH host key on first use and writes it to
// contrib/tofu/.known_hosts (DIVERGENCE 5.491). Nothing ever removed the
// file: seven entries from earlier labs were in it when this was written.
// Vultr hands addresses out again, so a stale key is a refusal waiting for
// a reused address, and the file only ever grew.
//
// This runs the real recipe with TOFU pointing at a stand-in that exits 0 or
// 1 and TOFU_DIR at a temporary directory, so nothing is destroyed and the
// repository's own file is not touched. It shows the ordering, which is the
// part that matters -- a failed destroy leaves instances running, and
// forgetting their keys then would leave the next `lab-test` trusting
// whatever answers -- and not that a real tofu destroy behaves.
func TestLabDownForgetsKnownHostsOnlyAfterASuccessfulDestroy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lab-down is a POSIX shell recipe")
	}
	var mk string
	for _, name := range []string{"bmake", "gmake", "make"} {
		p, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		probe := exec.Command(p, "make-supports-bang")
		probe.Dir, probe.Env = repoRoot(t), withoutMakeFlags(os.Environ())
		if probe.Run() == nil {
			mk = p
			break
		}
	}
	if mk == "" {
		t.Skip("no make on this machine can evaluate the Makefile")
	}

	for _, tc := range []struct {
		name     string
		exit     string
		wantFile bool
	}{
		{"destroy succeeds", "0", false},
		{"destroy fails", "1", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fake := filepath.Join(dir, "tofu")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit "+tc.exit+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			hosts := filepath.Join(dir, ".known_hosts")
			if err := os.WriteFile(hosts, []byte("203.0.113.9 ssh-ed25519 AAAA\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(mk, "lab-down", "TOFU="+fake, "TOFU_DIR="+dir)
			cmd.Dir = repoRoot(t)
			// The key is only checked for being set, never used.
			cmd.Env = append(withoutMakeFlags(os.Environ()), "VULTR_API_KEY=not-a-key")
			out, err := cmd.CombinedOutput()
			if (err != nil) != (tc.exit != "0") {
				t.Fatalf("lab-down with a destroy exiting %s: err = %v\n%s", tc.exit, err, out)
			}
			_, statErr := os.Stat(hosts)
			if kept := statErr == nil; kept != tc.wantFile {
				t.Errorf("%s: .known_hosts kept = %v, want %v\n%s", tc.name, kept, tc.wantFile, out)
			}
		})
	}
}
