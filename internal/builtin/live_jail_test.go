package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	liveJailConfWith(t, name, root, "")
}

// liveJailConfWith is liveJailConf with extra parameters in the block.
func liveJailConfWith(t *testing.T, name, root, extra string) {
	t.Helper()
	const path = "/etc/jail.conf"
	block := fmt.Sprintf("\n%s {\n\tpath = \"%s\";\n\tmount.devfs = 0;\n\tmount.fstab = \"\";\n%s\tpersist;\n}\n",
		name, root, extra)

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

// `jail.restart` really stops and starts, rather than doing nothing.
//
// A restart that quietly did nothing would pass any check that only
// asked whether the jail is running afterwards, because it was running
// before. The jid is the discriminator: the kernel allocates a new one
// on every create, so a jail that came back with the jid it had was
// never removed.
func TestLiveJailRestartAllocatesANewJid(t *testing.T) {
	c := liveJailSetup(t)
	name := "halite_live_restart"
	liveJailConf(t, name, t.TempDir())
	r := New()

	args := value.MapOf("name", name)
	if _, err := r.Exec.Call(c, "jail.start", args); err != nil {
		t.Fatalf("jail.start: %v", err)
	}
	t.Cleanup(func() {
		if liveJailRunning(t, c, name) {
			if err := liveQuotaRun(c, "jail", "-r", name); err != nil {
				t.Errorf("the test's jail could not be removed -- run `jail -r %s`: %v", name, err)
			}
		}
	})
	before := liveJailJID(t, c, r, name)

	if _, err := r.Exec.Call(c, "jail.restart", args); err != nil {
		t.Fatalf("jail.restart: %v", err)
	}
	if !liveJailRunning(t, c, name) {
		t.Fatal("jail.restart returned success and the jail is not running")
	}
	after := liveJailJID(t, c, r, name)
	if after == before {
		t.Errorf("the jail came back with the jid it had (%d), so it was never removed -- "+
			"`jail -rc` did not restart it", before)
	}
}

// A jail with an address of its own starts, and its parameters read back.
//
// This is the gap 5.70 left named: every jail this suite had started was
// a bare `persist` jail sharing the host's network stack, so nothing had
// exercised a jail with any network configuration at all. The address is
// a loopback alias on 127/8, which needs no interface of its own and
// collides with nothing an operator is using.
func TestLiveJailWithAnAddressOfItsOwn(t *testing.T) {
	c := liveJailSetup(t)
	name := "halite_live_net"
	root := t.TempDir()
	const addr = "127.0.44.1"
	liveJailConfWith(t, name, root, fmt.Sprintf("\tip4.addr = \"%s\";\n\tip4 = new;\n", addr))
	r := New()

	if _, err := r.Exec.Call(c, "jail.start", value.MapOf("name", name)); err != nil {
		t.Skipf("a jail with its own address could not be started here, which is a property of "+
			"the host's network rather than of this module: %v", err)
	}
	t.Cleanup(func() {
		if liveJailRunning(t, c, name) {
			if err := liveQuotaRun(c, "jail", "-r", name); err != nil {
				t.Errorf("the test's jail could not be removed -- run `jail -r %s`: %v", name, err)
			}
		}
	})
	if !liveJailRunning(t, c, name) {
		t.Fatal("jail.start returned success and the jail is not in `jls`")
	}

	// The address is read back through the module's own config reader,
	// against the real `jail -e`, which is the parser 5.70 rewrote.
	shown, err := r.Exec.Call(c, "jail.show_config", value.MapOf("name", name))
	if err != nil {
		t.Fatalf("jail.show_config: %v", err)
	}
	got, _ := shown.(*value.Map).GetString("ip4.addr")
	if fmt.Sprint(got) != addr {
		t.Errorf("the jail's ip4.addr reads %v, want %q", got, addr)
	}
}

// A jail with a process still in it is stopped, and the process goes.
//
// The other gap 5.70 named. `jail -r` kills what is inside; a stop that
// reported success while leaving the process running would be the
// dangerous failure, because the jail is gone from `jls` either way.
func TestLiveJailStopsAJailWithAProcessInIt(t *testing.T) {
	c := liveJailSetup(t)
	name := "halite_live_busy"
	root := t.TempDir()
	liveJailConf(t, name, root)
	r := New()

	if _, err := r.Exec.Call(c, "jail.start", value.MapOf("name", name)); err != nil {
		t.Fatalf("jail.start: %v", err)
	}
	t.Cleanup(func() {
		if liveJailRunning(t, c, name) {
			if err := liveQuotaRun(c, "jail", "-r", name); err != nil {
				t.Errorf("the test's jail could not be removed -- run `jail -r %s`: %v", name, err)
			}
		}
	})
	jid := liveJailJID(t, c, r, name)

	// **`jexec` runs a binary that is inside the jail, not one on the
	// host**, so an empty jail root has nothing to execute:
	//
	//	jexec: execvp: /bin/sleep: No such file or directory
	//
	// The first version of this test assumed otherwise and passed
	// anyway, which is the part worth keeping. `ps -J` caught the
	// short-lived `jexec` process itself, which really is in the jail
	// for the instant between attaching and failing to exec -- so the
	// check saw a process, the test went green, and nothing had ever
	// been running in the jail when the stop arrived. It passed on a
	// race, and skipped the one time the race went the other way.
	//
	// `/rescue/sleep` is statically linked, so it needs no libraries,
	// no runtime linker and no /lib inside the jail. Copying that one
	// file in is the whole userland this test needs.
	liveJailInstallSleep(t, root)
	sleeper := exec.Command{Argv: []string{"jexec", name, "/sleep", "600"}}
	go func() { _, _ = c.Run(sleeper) }()
	if !liveJailHasProcess(t, c, jid) {
		t.Fatal("nothing is running inside the jail, so this would be testing a stop with " +
			"nothing to stop -- which is what the first version of this test did")
	}

	if _, err := r.Exec.Call(c, "jail.stop", value.MapOf("name", name)); err != nil {
		t.Fatalf("jail.stop on a jail with a process in it: %v", err)
	}
	if liveJailRunning(t, c, name) {
		t.Error("jail.stop returned success and the jail is still in `jls`")
	}
	if liveJailHasProcess(t, c, jid) {
		t.Error("the jail is gone and a process is still running in it, which is the failure " +
			"that would otherwise be invisible: `jls` reports no jail either way")
	}
}

// liveJailInstallSleep puts a statically linked sleep inside the jail.
//
// Static on purpose: /rescue is FreeBSD's own set of statically linked
// recovery tools, so one file is a complete userland for this. A
// dynamically linked /bin/sleep would need the runtime linker and libc
// in the jail as well, which is a base system rather than a fixture.
func liveJailInstallSleep(t *testing.T, root string) {
	t.Helper()
	const src = "/rescue/sleep"
	binary, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("%s could not be read, so nothing can be run inside the jail: %v", src, err)
	}
	if err := os.WriteFile(filepath.Join(root, "sleep"), binary, 0o755); err != nil {
		t.Fatalf("the static sleep could not be put in the jail: %v", err)
	}
}

// liveJailJID reads a running jail's jid through the module.
func liveJailJID(t *testing.T, c *exec.Context, r *Registries, name string) int {
	t.Helper()
	entry := liveJailFromModule(t, c, r, name)
	if entry == nil {
		t.Fatalf("%s is not running, so it has no jid", name)
	}
	jid, _ := entry.GetString("jid")
	n, ok := jid.(int)
	if !ok || n == 0 {
		t.Fatalf("%s has no usable jid: %v", name, jid)
	}
	return n
}

// liveJailHasProcess asks `ps` whether anything is in the jail.
//
// `ps -J <jid>` selects by jail, which is the question being asked, and
// it is a raw `ps` rather than this build's own module so that the check
// does not depend on the code under test.
func liveJailHasProcess(t *testing.T, c *exec.Context, jid int) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		res, err := c.Run(exec.Command{
			Argv:           []string{"ps", "-J", fmt.Sprint(jid), "-o", "pid="},
			IgnoreExitCode: true,
		})
		if err == nil && strings.TrimSpace(res.Stdout) != "" {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}
