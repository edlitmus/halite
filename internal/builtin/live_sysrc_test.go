package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `sysrc`, driven for real against the real `sysrc(8)`.
//
// # Why this exists
//
// The evidence note said this module "reads real rc.conf through the real
// `sysrc` on CI's FreeBSD runner". It did not. Its only test swaps in a
// `RecordingRunner`, so on the FreeBSD runner it ran *beside* a real sysrc
// and never called it — the `c.Which("sysrc")` guard decided whether the
// test ran, and the recorder decided what it saw. A skip guard on the real
// tool reads like a test that uses it. DIVERGENCE 5.139.
//
// # It touches no rc.conf that matters
//
// `sysrc -f <file>` is the tool's own way of working on something other
// than /etc/rc.conf, and the module already exposes it. So this drives the
// real binary against a file in a directory the test owns: root is needed
// only because the module declares it, not because anything here writes
// outside the temporary directory.
func TestLiveSysrcWritesAndReadsBackARealRCConf(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to drive the real sysrc")
	}
	if runtime.GOOS != "freebsd" {
		t.Skipf("sysrc is FreeBSD's, and this is %s", runtime.GOOS)
	}
	c := newCtx(false)
	c.Runner = &exec.OSRunner{}
	if c.Which("sysrc") == "" {
		t.Skip("this FreeBSD has no sysrc, which is itself worth reading in the log")
	}

	rc := filepath.Join(t.TempDir(), "rc.conf")
	if err := os.WriteFile(rc, []byte("# written by a test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New()

	// Absent to begin with, through the module's own reader.
	//
	// `sysrc.get` documents "an empty string when it is not set", and that
	// is the contract being pinned here. The first version of this test
	// asserted an *error* instead, from an assumption about what a reader
	// does with a missing key, and the FreeBSD leg refused it on the first
	// run it ever did -- which is the whole argument for the leg. A test
	// written from an assumption tests the assumption.
	//
	// Worth knowing rather than fixing: because absence is flattened to
	// the empty string, `sysrc.get` cannot tell `halite_probe_enable` unset
	// from `halite_probe_enable=""`, and on FreeBSD the second is a real
	// and meaningful state -- `ifconfig_em0=""` is how an interface is
	// declared with no options. The state functions read `sysrcGet`'s
	// second return value and do tell them apart; only the exec function
	// discards it.
	before, err := r.Exec.Call(c, "sysrc.get",
		value.MapOf("name", "halite_probe_enable", "file", rc))
	if err != nil {
		t.Fatalf("sysrc.get on a setting that is not there: %v", err)
	}
	if s, _ := before.(string); s != "" {
		t.Errorf("sysrc.get returned %#v for a setting that is not there, and the "+
			"function documents an empty string", before)
	}

	// Set it, and read it back with the real tool rather than with the
	// module — a module that agrees with itself proves nothing.
	if _, err := r.Exec.Call(c, "sysrc.set",
		value.MapOf("name", "halite_probe_enable", "value", "YES", "file", rc)); err != nil {
		t.Fatalf("sysrc.set: %v", err)
	}
	out, err := c.Run(exec.Command{Argv: []string{"sysrc", "-f", rc, "-n", "halite_probe_enable"}})
	if err != nil {
		t.Fatalf("reading it back with sysrc: %v", err)
	}
	if got := strings.TrimSpace(out.Stdout); got != "YES" {
		t.Errorf("sysrc reports %q after the module set YES", got)
	}
	// And the file on disk says so, which is the half a tool's own
	// read-back cannot establish.
	body, err := os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `halite_probe_enable="YES"`) {
		t.Errorf("the rc.conf does not carry the setting:\n%s", body)
	}

	// The module's own reader agrees with the tool.
	got, err := r.Exec.Call(c, "sysrc.get", value.MapOf("name", "halite_probe_enable", "file", rc))
	if err != nil {
		t.Fatalf("sysrc.get after set: %v", err)
	}
	if s, _ := got.(string); strings.TrimSpace(s) != "YES" {
		t.Errorf("sysrc.get returned %#v", got)
	}

	// A dry run changes nothing, which is this module's TestReliable
	// claim measured against the real tool rather than against a
	// recorder.
	dry := newCtx(true)
	dry.Runner = &exec.OSRunner{}
	if _, err := r.Exec.Call(dry, "sysrc.set",
		value.MapOf("name", "halite_probe_enable", "value", "NO", "file", rc)); err != nil {
		t.Fatalf("sysrc.set under test mode: %v", err)
	}
	after, err := c.Run(exec.Command{Argv: []string{"sysrc", "-f", rc, "-n", "halite_probe_enable"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(after.Stdout); got != "YES" {
		t.Errorf("a dry run changed the setting to %q", got)
	}

	// Remove it through the state, which is where removal lives -- the
	// module has no `remove` execution function, and a test that assumed
	// one would have been testing its own assumption.
	res, err := r.States.Call(c, "sysrc.absent",
		value.MapOf("name", "halite_probe_enable", "file", rc))
	if err != nil {
		t.Fatalf("sysrc.absent: %v", err)
	}
	if !res.Succeeded() || !res.HasChanges() {
		t.Fatalf("sysrc.absent did not remove it: %+v", res)
	}
	body, err = os.ReadFile(rc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "halite_probe_enable") {
		t.Errorf("the setting survived removal:\n%s", body)
	}
}
