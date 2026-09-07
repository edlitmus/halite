package builtin

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// `hostname` and `sysctl`, driven against the machine this runs on.
//
// # Why these are not in the container
//
// The Debian image of `live_debian_test.go` covers the modules whose
// subsystem is a program. These two are not that. `sysctl` is the
// kernel, and a container shares the host's — `/proc/sys` is mounted
// read-only, and remounting it would reach *the developer's own kernel*
// rather than a disposable one. `hostname` is closer: a container has
// its own UTS namespace, so setting the running name there is real, but
// `hostnamectl` is not running and that is the branch a systemd node
// takes.
//
// So these run on a machine that can be thrown away and has a real
// kernel: a GitHub runner, which is a fresh virtual machine per job, and
// the FreeBSD virtual machine CI already boots. Both are discarded
// minutes later, which is what makes it acceptable to change their
// hostname and their kernel parameters.
//
// The container was tried for `hostname` first, and it cannot do it
// either: Docker bind-mounts `/etc/hostname` from outside, so the
// atomic replace this module does — write a temporary file beside it,
// rename over it — fails with `device or resource busy`. That is the
// container rather than the module, on a file that is an ordinary one
// everywhere halite actually runs, and it is why both of these need a
// machine rather than an image.
//
// # They put everything back
//
// Not because the runner needs it — it is deleted — but because these
// are also meant to be runnable on a real host by somebody who wants the
// answer for their own platform, and a test that leaves a machine
// renamed is one nobody runs twice. Every change is captured first and
// restored in a cleanup.
//
// # Why they skip everywhere else
//
// `HALITE_SYSTEM_LIVE=1`, and nothing sets it by accident. Renaming the
// machine somebody is working on is not a thing to do because they typed
// `go test ./...`.

// system skips unless this is a machine that has been offered up.
func system(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; these change the kernel and the hostname")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_SYSTEM_LIVE is set and this is not root; both modules here declare that they need it")
	}
	return realCtx(t)
}

// ---- hostname ----

// **The machine is really renamed, and both halves are checked.**
//
// `hostname` keeps two facts apart that an operator has to keep apart
// too: the name the kernel is running under now, and the name the
// machine will come up with after a reboot. A module that set one and
// reported both is the defect this test exists for, and neither half can
// be established without changing a real machine.
//
// The branch taken differs by platform and by what is installed —
// `hostnamectl` where systemd is running, `sysrc` on FreeBSD, and
// `/etc/hostname` plus `hostname(1)` otherwise. The test logs which one
// it exercised, because "hostname is verified" without that is a claim
// about one branch of three.
func TestLiveHostnameRenamesTheMachineAndPutsItBack(t *testing.T) {
	c := system(t)
	r := New()

	before, err := r.Exec.Call(c, "hostname.get_hostname", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	running, _ := before.(string)
	if running == "" {
		t.Fatal("this machine reports no running hostname")
	}

	persistedBefore, err := r.Exec.Call(c, "hostname.get_persistent", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	wasPersisted, _ := persistedBefore.(string)
	t.Logf("running=%q persistent=%q; hostnamectl=%q sysrc=%q",
		running, wasPersisted, c.Which("hostnamectl"), c.Which("sysrc"))

	// Put it back whatever happens, including a panic. The persistent
	// name is restored through the module too, because restoring it by
	// hand would be a second implementation of the thing under test.
	t.Cleanup(func() {
		name := running
		if wasPersisted != "" {
			name = wasPersisted
		}
		if _, err := r.Exec.Call(c, "hostname.set_hostname", value.MapOf("hostname", name)); err != nil {
			t.Errorf("this machine was left named something else: restoring %q failed: %v", name, err)
		}
	})

	const want = "halite-live-probe"
	if _, err := r.Exec.Call(c, "hostname.set_hostname", value.MapOf("hostname", want)); err != nil {
		t.Fatalf("set_hostname: %v", err)
	}

	// The running name, read back through the module and through the
	// kernel. Through both, because the module reading its own write is
	// the round trip and not the effect.
	after, err := r.Exec.Call(c, "hostname.get_hostname", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	if after != want {
		t.Errorf("get_hostname = %v after setting %q", after, want)
	}
	res, err := c.Run(exec.Command{Argv: []string{"hostname"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(res.Stdout); !strings.HasPrefix(got, want) {
		t.Errorf("the kernel reports %q and the module set %q", got, want)
	}

	// The persistent name, which is the half that survives a reboot and
	// the half a fixture cannot establish.
	persistedAfter, err := r.Exec.Call(c, "hostname.get_persistent", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	if persistedAfter != want {
		t.Errorf("the persistent name is %v and the module set %q; this machine "+
			"would come back as %v after a reboot", persistedAfter, want, persistedAfter)
	}
}

// The state converges rather than reporting a change every run.
//
// A hostname state that reports a change on every run is one an operator
// stops reading, and the comparison it rests on is not obvious: a node
// configured `web1.example` and running as `web1` is the same node.
func TestLiveHostnameStateConvergesOnTheNameTheMachineHas(t *testing.T) {
	c := system(t)
	r := New()

	before, err := r.Exec.Call(c, "hostname.get_hostname", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	running, _ := before.(string)

	// Asked for the name it already has, the state reports nothing.
	out, err := r.States.Call(c, "hostname.system", value.MapOf("name", running))
	if err != nil {
		t.Fatal(err)
	}
	if out.Changes.Len() != 0 {
		t.Errorf("asked for the name this machine already has, the state reported %+v", out.Changes)
	}
	if !out.Succeeded() {
		t.Errorf("the state failed on the name the machine has: %s", out.Comment)
	}
}

// ---- sysctl ----

// liveSysctlKey is the parameter these tests move, per platform.
//
// Harmless and always present on the platform it names. `vm.swappiness`
// is a hint to the page reclaimer, `kern.ipc.somaxconn` a cap on a
// listen backlog; nothing here depends on either, and both are restored.
// Overridable, because the right answer on a platform nobody has run
// this on yet is not knowable from here.
func liveSysctlKey(t *testing.T) (name, probe string) {
	t.Helper()
	if v := os.Getenv("HALITE_SYSCTL_KEY"); v != "" {
		want := os.Getenv("HALITE_SYSCTL_VALUE")
		if want == "" {
			t.Fatalf("HALITE_SYSCTL_KEY names %q and HALITE_SYSCTL_VALUE is unset; "+
				"a test with no value to set would pass by doing nothing", v)
		}
		return v, want
	}
	switch runtime.GOOS {
	case "linux":
		return "vm.swappiness", "61"
	case "freebsd", "openbsd", "netbsd", "darwin":
		return "kern.ipc.somaxconn", "1023"
	}
	t.Skipf("no known-harmless sysctl for %s; set HALITE_SYSCTL_KEY and HALITE_SYSCTL_VALUE", runtime.GOOS)
	return "", ""
}

// **A real kernel parameter, really set.**
//
// This is the one that could not be done in a container: `/proc/sys` is
// read-only there, and a container that remounted it would be writing to
// the kernel of whoever ran the test. So it runs on a machine that gets
// thrown away, and what it changes is a real running kernel.
func TestLiveSysctlSetsARealKernelParameter(t *testing.T) {
	c := system(t)
	r := New()
	name, want := liveSysctlKey(t)

	before, err := r.Exec.Call(c, "sysctl.get", value.MapOf("name", name))
	if err != nil {
		t.Fatalf("sysctl.get(%s): %v", name, err)
	}
	original, _ := before.(string)
	t.Logf("%s is %q on this %s kernel", name, original, runtime.GOOS)
	if original == want {
		t.Fatalf("%s is already %q, so setting it would prove nothing; "+
			"choose another value with HALITE_SYSCTL_VALUE", name, want)
	}
	t.Cleanup(func() {
		if _, err := r.Exec.Call(c, "sysctl.assign",
			value.MapOf("name", name, "value", original)); err != nil {
			t.Errorf("%s was left at something other than %q: %v", name, original, err)
		}
	})

	if _, err := r.Exec.Call(c, "sysctl.assign", value.MapOf("name", name, "value", want)); err != nil {
		t.Fatalf("sysctl.assign: %v", err)
	}

	// Through the module, and through the kernel. `sysctlAssign` tries
	// two spellings -- `-w name=value` and the bare BSD form -- and
	// which one a platform accepts is exactly what has never been
	// checked anywhere.
	got, err := r.Exec.Call(c, "sysctl.get", value.MapOf("name", name))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("sysctl.get = %v after assigning %q", got, want)
	}
	res, err := c.Run(exec.Command{Argv: []string{"sysctl", "-n", name}})
	if err != nil {
		t.Fatal(err)
	}
	if kernel := strings.TrimSpace(res.Stdout); kernel != want {
		t.Errorf("the kernel reports %s = %q and the module assigned %q", name, kernel, want)
	}
}

// **The persistent half, written where this platform reads it.**
//
// `sysctl.present` is the state that matters, because the running value
// alone is lost at the next boot. Where it writes differs by platform —
// a drop-in on Linux, `/etc/sysctl.conf` elsewhere — and that choice has
// never been exercised on either.
//
// The file is written under a temporary path rather than the platform's
// real one. That is the single deliberate step away from realism here,
// and the reason is specific: appending to the machine's own
// `/etc/sysctl.conf` and then editing it back out is a rewrite of a file
// an operator may have hand-maintained, and getting the restore wrong
// costs them something a test has no business costing. The path is the
// only difference; `writeSysctlConf` does not branch on it.
func TestLiveSysctlPresentSetsAndPersists(t *testing.T) {
	c := system(t)
	r := New()
	name, want := liveSysctlKey(t)
	conf := t.TempDir() + "/99-halite-live.conf"

	before, err := r.Exec.Call(c, "sysctl.get", value.MapOf("name", name))
	if err != nil {
		t.Fatal(err)
	}
	original, _ := before.(string)
	t.Cleanup(func() {
		if _, err := r.Exec.Call(c, "sysctl.assign",
			value.MapOf("name", name, "value", original)); err != nil {
			t.Errorf("%s was left at something other than %q: %v", name, original, err)
		}
	})

	args := value.MapOf("name", name, "value", want, "config", conf)

	// Test mode first: it must predict the change and make none. On a
	// module that changes a kernel, "predicts and does nothing" is the
	// promise an operator relies on before they touch production.
	predicted, err := r.States.Call(testCtx(t), "sysctl.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if predicted.Result != nil {
		t.Errorf("test mode decided rather than predicted: %+v", predicted)
	}
	if predicted.Changes.Len() == 0 {
		t.Error("test mode predicted no change for a parameter it would have moved")
	}
	if still, _ := r.Exec.Call(c, "sysctl.get", value.MapOf("name", name)); still != original {
		t.Fatalf("test mode changed the kernel: %s is %v and was %q", name, still, original)
	}
	if _, err := os.Stat(conf); err == nil {
		t.Error("test mode wrote the configuration file")
	}

	// Then for real.
	applied, err := r.States.Call(c, "sysctl.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Succeeded() {
		t.Fatalf("sysctl.present failed: %s", applied.Comment)
	}
	if got, _ := r.Exec.Call(c, "sysctl.get", value.MapOf("name", name)); got != want {
		t.Errorf("the kernel has %s = %v after the state set %q", name, got, want)
	}
	body, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("the state reported success and wrote no configuration: %v", err)
	}
	if !strings.Contains(string(body), name) || !strings.Contains(string(body), want) {
		t.Errorf("%s does not carry the setting:\n%s", conf, body)
	}

	// And it converges: run again and nothing moves.
	again, err := r.States.Call(c, "sysctl.present", args)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changes.Len() != 0 {
		t.Errorf("a second run reported %+v; this state does not converge", again.Changes)
	}
	if !strings.Contains(again.Comment, "already") {
		t.Errorf("the converged comment reads %q", again.Comment)
	}
}

// A parameter this kernel does not have is refused, rather than
// persisted into a file where it will never take effect.
//
// The module says so explicitly, and this is the assertion that it does
// so against a real kernel's idea of what exists rather than against a
// fixture's.
func TestLiveSysctlRefusesAParameterThisKernelDoesNotHave(t *testing.T) {
	c := system(t)
	r := New()
	conf := t.TempDir() + "/99-halite-live.conf"

	out, err := r.States.Call(c, "sysctl.present", value.MapOf(
		"name", "halite.no.such.parameter", "value", "1", "config", conf))
	if err != nil {
		t.Fatal(err)
	}
	if out.Succeeded() {
		t.Fatalf("a parameter this kernel does not have was accepted: %+v", out)
	}
	if !strings.Contains(out.Comment, "halite.no.such.parameter") {
		t.Errorf("the refusal does not name the parameter: %q", out.Comment)
	}
	if _, err := os.Stat(conf); err == nil {
		t.Error("a refused parameter was written to the configuration file anyway")
	}
}
