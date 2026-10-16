package builtin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The second run after a failure (DIVERGENCE 5.38, and the section that
// follows it for this change).
//
// `netplan.managed` wrote the file, had netplan refuse it, and returned
// False -- and left the refused file in /etc/netplan. The next run
// compared bytes, found them equal, and returned True "already holds the
// declared configuration" without asking netplan again. A failed
// `apply: true` converged the same way, and was never retried.
//
// These call `netplanManaged` directly rather than through the registry,
// which refuses the module off Debian and Ubuntu, so that the second run
// is checked on every platform CI builds on and not only on Linux. The
// logic under test is the module's own file handling; the tool is the
// fake below.

// netplanTreeRunner is a fake netplan that answers `netplan generate`
// from the directory as it stands, the way the real one does: it reads
// every file, and refuses the tree while any file carries the unknown key
// `addressess`.
//
// **The reading of the directory is synthetic.** What is captured is the
// refusal text, which is netplanFixture's -- the text 5.38 records as
// matching what real netplan 1.1.2 printed for that key -- and the fact
// that real `netplan generate` refused a document with that key at all.
// That netplan reads the whole directory on each generate is how 5.38's
// live test could validate the probe with a bare `netplan generate`; the
// fake makes it explicit so that putting a file back can be seen to
// change netplan's answer.
//
// `netplan apply` succeeds unless applyFails is set, in which case it
// fails with a message that is **invented**: no failed apply has ever
// been watched (5.38 says why none ever will be by a test), so nothing
// here depends on its wording.
type netplanTreeRunner struct {
	dir        string
	applyFails bool
	ran        []string
}

func (r *netplanTreeRunner) Run(_ context.Context, cmd exec.Command) (exec.Result, error) {
	r.ran = append(r.ran, cmd.String())
	switch cmd.String() {
	case "netplan generate":
		entries, _ := os.ReadDir(r.dir)
		for _, e := range entries {
			raw, err := os.ReadFile(filepath.Join(r.dir, e.Name()))
			if err == nil && strings.Contains(string(raw), "addressess") {
				return exec.Result{Code: 1, Stderr: "Error in network definition: unknown key 'addressess'\n"}, nil
			}
		}
		return exec.Result{}, nil
	case "netplan apply":
		if r.applyFails {
			return exec.Result{Code: 1, Stderr: "synthetic: apply failed\n"}, nil
		}
		return exec.Result{}, nil
	}
	return exec.Result{}, nil
}

func (r *netplanTreeRunner) count(command string) int {
	n := 0
	for _, ran := range r.ran {
		if ran == command {
			n++
		}
	}
	return n
}

func netplanTreeFixture(t *testing.T, applyFails bool) (string, *exec.Context, *netplanTreeRunner) {
	t.Helper()
	dir := t.TempDir()
	old := NetplanDir
	NetplanDir = dir
	t.Cleanup(func() { NetplanDir = old })
	run := &netplanTreeRunner{dir: dir, applyFails: applyFails}
	c := newCtx(false)
	c.Runner = run
	c.Lookup = func(name string) string { return "/usr/sbin/" + name }
	return dir, c, run
}

func netplanRefusedArgs() *value.Map {
	return value.MapOf(
		"name", "01-halite",
		"config", value.MapOf("network", value.MapOf(
			"version", int64(2),
			"ethernets", value.MapOf("eth0", value.MapOf(
				"addressess", []any{"10.0.0.1/24"},
			)),
		)),
	)
}

// A document netplan refuses fails, and fails again on the next run --
// it is never reported converged because it matches what is on disk --
// and no refused file is left where the boot-time generator would read
// it.
func TestARefusedDocumentFailsOnTheSecondRunToo(t *testing.T) {
	dir, c, run := netplanTreeFixture(t, false)
	path := filepath.Join(dir, "01-halite.yaml")

	for i := 1; i <= 2; i++ {
		res, err := netplanManaged(c, netplanRefusedArgs())
		if err != nil {
			t.Fatal(err)
		}
		if res.Succeeded() {
			t.Fatalf("run %d: a document netplan refuses was reported as a success: %s", i, res.Comment)
		}
		if !strings.Contains(res.Comment, "addressess") {
			t.Errorf("run %d: the failure does not carry netplan's message: %s", i, res.Comment)
		}
		if !strings.Contains(res.Comment, "nothing has been applied") {
			t.Errorf("run %d: the failure does not say nothing was applied: %s", i, res.Comment)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("run %d: the refused file is still in the directory netplan renders at boot (%v)", i, err)
		}
	}
	// Each run: generate the written tree, then generate the restored
	// one. The second render is what proves the /run files netplan may
	// have reached were rewritten from the tree as it now stands.
	if got := run.count("netplan generate"); got != 4 {
		t.Errorf("netplan generate ran %d times over two runs, want 4: %v", got, run.ran)
	}
	if got := run.count("netplan apply"); got != 0 {
		t.Errorf("a refused document was applied: %v", run.ran)
	}
}

// A refused document replacing one that was there puts the old one back,
// bytes and mode, and the tree renders again.
func TestARefusedDocumentPutsThePreviousFileBack(t *testing.T) {
	dir, c, _ := netplanTreeFixture(t, false)
	path := filepath.Join(dir, "01-halite.yaml")
	previous := "network:\n  version: 2\n"
	if err := os.WriteFile(path, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := netplanManaged(c, netplanRefusedArgs())
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatalf("a refused document was reported as a success: %s", res.Comment)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the previous file is gone: %v", err)
	}
	if string(got) != previous {
		t.Errorf("the file holds\n%s\nwant the previous\n%s", got, previous)
	}
	if !strings.Contains(res.Comment, "put back") {
		t.Errorf("the result does not say the previous file was put back: %s", res.Comment)
	}
	if !strings.Contains(res.Comment, "rendered again") {
		t.Errorf("the result does not say the restored tree rendered: %s", res.Comment)
	}
}

// A failed `apply: true` is retried by the next run rather than reported
// converged because the file it wrote is on disk.
func TestAFailedApplyIsRetriedOnTheNextRun(t *testing.T) {
	dir, c, run := netplanTreeFixture(t, true)
	args := netplanArgs(true)

	res, err := netplanManaged(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Fatalf("a failed apply was reported as a success: %s", res.Comment)
	}
	if _, err := os.Stat(filepath.Join(dir, "01-halite.yaml")); !os.IsNotExist(err) {
		t.Errorf("the unapplied file was left to read as converged next run (%v)", err)
	}

	// The apply now works, as it would once whatever stopped it is
	// fixed. The next run must reach it.
	run.applyFails = false
	res, err = netplanManaged(c, args)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("the second run did not write and apply: %+v", res)
	}
	if got := run.count("netplan apply"); got != 2 {
		t.Errorf("netplan apply ran %d times over two runs, want 2 -- the second run did not retry it: %v",
			got, run.ran)
	}
	if !strings.Contains(res.Comment, "applied") {
		t.Errorf("the second run does not say it applied: %s", res.Comment)
	}
}
