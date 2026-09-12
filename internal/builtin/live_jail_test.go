package builtin

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `jail`, driven against a jail this test starts and stops.
//
// # What this closes
//
// 5.32 shipped `jail` reading `jls --libxo=json`, and 5.66 checked every
// field it reads against the list `jls -h` publishes -- which found one
// the module had invented. Both of those are reads. What neither settles
// is the mutating half: `jail.start`, `jail.stop` and the `jail.running`
// state were argument vectors that nothing had watched take effect, and
// `jail`'s evidence entry said so.
//
// This starts a real jail through the module, finds it in a real `jls`,
// stops it through the module, and confirms it is gone.
//
// # It is gated, and it writes to /etc/jail.conf
//
// `HALITE_SYSTEM_LIVE=1` and root.
//
// The jail.conf entry is the part to read carefully, and it is the same
// shape as the quota leg's fstab entry. `jailRun` runs `jail -c <name>`
// with no `-f`, so the jail must be defined in the default configuration
// for the module to start it at all -- and using an alternative file
// would exercise a path the module does not have rather than the one it
// does.
//
// What makes that acceptable here is `jail_list`. Jails start at boot
// from the names in that rc.conf variable, not from everything jail.conf
// defines, so an entry this test fails to remove starts nothing at the
// next boot. The test refuses to run on a host where `jail_list` is not
// empty, because that reasoning does not hold there.
//
// The block overrides every inherited default that would reach outside
// the test: its own path, no devfs mount, and no `mount.fstab` lookup.
// Without those it would inherit `path = /jail/${name}` and a fstab file
// per jail from the global block at the top of this host's jail.conf.
func TestLiveJailStartsAndStopsARealJail(t *testing.T) {
	c := liveJailSetup(t)
	name := "halite_live_test"
	root := t.TempDir()
	liveJailConf(t, name, root)
	r := New()

	// Not running to begin with. If it is, something is wrong with the
	// name rather than with the module, and going on would stop a jail
	// this test did not start.
	if liveJailRunning(t, c, name) {
		t.Fatalf("a jail called %q is already running; this test will not touch it", name)
	}

	start := value.NewMap(1)
	start.Set("name", name)
	if _, err := r.Exec.Call(c, "jail.start", start); err != nil {
		t.Fatalf("jail.start: %v", err)
	}
	t.Cleanup(func() {
		// Unconditional, because a failure between here and the stop
		// below would otherwise leave a jail running on a real host.
		if liveJailRunning(t, c, name) {
			if err := liveQuotaRun(c, "jail", "-r", name); err != nil {
				t.Errorf("the test's jail could not be removed -- run `jail -r %s`: %v", name, err)
			}
		}
	})

	// The module's own reader must see it, and so must a raw `jls`.
	// Checking only the module's reader would let a start that silently
	// did nothing pass, so long as the reader was equally wrong.
	if !liveJailRunning(t, c, name) {
		t.Fatal("jail.start returned success and the jail is not in `jls`")
	}
	listed := liveJailFromModule(t, c, r, name)
	if listed == nil {
		t.Fatal("the jail is running and `jail.list` does not report it")
	}
	if got, _ := listed.GetString("path"); got != root {
		t.Errorf("the jail's path reads %v, want %q -- the entry inherited a path from the "+
			"global block instead of taking the one this test set", got, root)
	}
	if got, _ := listed.GetString("state"); got != "ACTIVE" {
		t.Errorf("a running jail's state reads %v, want ACTIVE", got)
	}
	if jid, _ := listed.GetString("jid"); jid == 0 {
		t.Errorf("a running jail has no jid: %v", listed)
	}

	stop := value.NewMap(1)
	stop.Set("name", name)
	if _, err := r.Exec.Call(c, "jail.stop", stop); err != nil {
		t.Fatalf("jail.stop: %v", err)
	}
	if liveJailRunning(t, c, name) {
		t.Error("jail.stop returned success and the jail is still in `jls`")
	}
	if liveJailFromModule(t, c, r, name) != nil {
		t.Error("the jail is stopped and `jail.list` still reports it")
	}
}

// The `jail.running` state converges and then reports no change.
//
// A state that starts a jail every time it runs is one nobody can put in
// a highstate, so the second application is the assertion that matters.
func TestLiveJailRunningStateIsIdempotent(t *testing.T) {
	c := liveJailSetup(t)
	name := "halite_live_state"
	liveJailConf(t, name, t.TempDir())
	r := New()

	args := value.NewMap(2)
	args.Set("name", name)
	args.Set("running", true)
	t.Cleanup(func() {
		if liveJailRunning(t, c, name) {
			if err := liveQuotaRun(c, "jail", "-r", name); err != nil {
				t.Errorf("the test's jail could not be removed -- run `jail -r %s`: %v", name, err)
			}
		}
	})

	first, err := r.States.Call(c, "jail.running", args)
	if err != nil {
		t.Fatalf("the first jail.running: %v", err)
	}
	if !first.HasChanges() {
		t.Errorf("starting a jail that was not running reported no change: %+v", first)
	}
	if !liveJailRunning(t, c, name) {
		t.Fatal("jail.running reported success and the jail is not in `jls`")
	}

	second, err := r.States.Call(c, "jail.running", args)
	if err != nil {
		t.Fatalf("the second jail.running: %v", err)
	}
	if second.HasChanges() {
		t.Errorf("a jail that was already running was started again: %+v", second)
	}

	args.Set("running", false)
	third, err := r.States.Call(c, "jail.running", args)
	if err != nil {
		t.Fatalf("jail.running with running=false: %v", err)
	}
	if !third.HasChanges() {
		t.Errorf("stopping a running jail reported no change: %+v", third)
	}
	if liveJailRunning(t, c, name) {
		t.Error("jail.running was asked for running=false and the jail is still up")
	}
}

// liveJailSetup gates the leg, or skips saying what is missing.
func liveJailSetup(t *testing.T) *exec.Context {
	t.Helper()
	if runtime.GOOS != "freebsd" {
		t.Skipf("jails are FreeBSD; this is %s", runtime.GOOS)
	}
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to let this start a jail and write to /etc/jail.conf")
	}
	if os.Geteuid() != 0 {
		t.Skip("starting and stopping a jail needs root")
	}
	c := &exec.Context{}
	for _, tool := range []string{"jail", "jls"} {
		if c.Which(tool) == "" {
			t.Skipf("this host has no `%s`", tool)
		}
	}
	// The argument for writing to jail.conf at all is that `jail_list`
	// is empty, so a leftover entry starts nothing at the next boot.
	// Where that is not true, the argument does not hold and this does
	// not run.
	rc, err := os.ReadFile("/etc/rc.conf")
	if err != nil {
		t.Skipf("/etc/rc.conf could not be read, so jail_list cannot be checked: %v", err)
	}
	for _, line := range strings.Split(string(rc), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "jail_list") {
			continue
		}
		if value := strings.Trim(strings.SplitN(line, "=", 2)[1], `" `); value != "" {
			t.Skipf("this host's jail_list is %q rather than empty, so an entry left behind "+
				"in jail.conf could start a jail at the next boot; this leg will not write here", value)
		}
	}
	return c
}

// liveJailConf appends the test's jail definition and removes it again.
//
// Every inherited default that would reach outside the test is
// overridden: this host's global block sets `path = /jail/${name}`,
// a `mount.fstab` file per jail and `mount.devfs`, none of which exist
// for a jail invented by a test.
func liveJailConf(t *testing.T, name, root string) {
	t.Helper()
	const path = "/etc/jail.conf"
	block := fmt.Sprintf("\n%s {\n\tpath = \"%s\";\n\tmount.devfs = 0;\n\tmount.fstab = \"\";\n\tpersist;\n}\n",
		name, root)

	before, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("%s could not be read: %v", path, err)
	}
	if strings.Contains(string(before), name+" {") {
		t.Fatalf("%s already defines a jail called %q; this test will not touch it", path, name)
	}
	if err := os.WriteFile(path, append(before, block...), 0o644); err != nil {
		t.Skipf("%s could not be written: %v", path, err)
	}

	t.Cleanup(func() {
		current, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("%s could not be read back, so the test's entry may still be in it: %v", path, err)
			return
		}
		trimmed := strings.Replace(string(current), block, "", 1)
		if trimmed == string(current) {
			t.Errorf("this test's jail.conf entry is not there to remove; %s reads:\n%s", path, current)
			return
		}
		if err := os.WriteFile(path, []byte(trimmed), 0o644); err != nil {
			t.Errorf("this test's entry could not be removed from %s -- remove the %q block by "+
				"hand: %v", path, name, err)
		}
	})
}

// liveJailRunning asks a raw `jls` rather than the module.
//
// Deliberately independent of the code under test: if the module's
// reader and its writer were wrong in the same direction, a check that
// went through the reader would agree with both.
func liveJailRunning(t *testing.T, c *exec.Context, name string) bool {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: []string{"jls", "-N", "name"}, IgnoreExitCode: true})
	if err != nil {
		t.Fatalf("`jls -N name` could not be run: %v", err)
	}
	for _, line := range strings.Fields(res.Stdout) {
		if line == name {
			return true
		}
	}
	return false
}

// liveJailFromModule returns what `jail.list` says about one jail.
func liveJailFromModule(t *testing.T, c *exec.Context, r *Registries, name string) *value.Map {
	t.Helper()
	out, err := r.Exec.Call(c, "jail.list", value.NewMap(0))
	if err != nil {
		t.Fatalf("jail.list: %v", err)
	}
	entry, ok := out.(*value.Map).GetString(name)
	if !ok {
		return nil
	}
	return entry.(*value.Map)
}
