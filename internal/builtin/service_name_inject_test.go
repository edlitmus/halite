package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// **A service name cannot become an option or a path** (issue 321, the
// M-45 class, item A12). Every provider puts the name into argv as an
// argument of its own, which keeps the shell out but not the tool's own
// option parser: `service -R status` is service(8) stopping and starting
// every enabled service on the machine and exiting 0, never reading the
// word `status` at all. And service(8) execs `<dir>/<name>` from its rc.d
// directories, so a name with `../` in it is a path to any executable,
// run as root.
//
// The test walks **every** `service.*` function and state the registry
// holds, and the mod_watch reaction, on a node that looks like FreeBSD,
// rather than naming the paths one by one. The defect is the commonest
// one here -- two paths that must agree and do not -- and a list of the
// paths in the test is exactly the list that goes stale when somebody
// adds a function. A function that takes no name has nothing to check
// and is passed over by its signature, not by a list.
func TestAServiceNameThatIsAnOptionOrAPathNeverReachesArgv(t *testing.T) {
	bad := []string{"-R", "-j", "--", "../../../tmp/payload", "/tmp/payload", "sshd\nrestart", ""}
	freebsd := func() (*exec.Context, *exec.RecordingRunner) {
		c := newCtx(false)
		rec := &exec.RecordingRunner{}
		c.Runner = rec
		// service and sysrc are present and systemctl is not, so the
		// FreeBSD provider is the one picked, on any machine running
		// the test.
		c.Lookup = func(name string) string {
			if name == "service" || name == "sysrc" {
				return "/usr/sbin/" + name
			}
			return ""
		}
		return c, rec
	}
	reached := func(rec *exec.RecordingRunner, name string) []string {
		var out []string
		for _, cmd := range rec.Ran {
			for _, a := range cmd.Argv[1:] {
				if a == name || (name != "" && strings.Contains(a, name)) {
					out = append(out, cmd.String())
				}
			}
		}
		return out
	}

	r := New()

	var calledExec, calledStates int
	for _, fn := range r.Exec.Names() {
		if !strings.HasPrefix(fn, "service.") {
			continue
		}
		sig, _ := r.Exec.Signatures().Lookup(fn)
		if !hasParam(sig.Params, "name") {
			continue
		}
		calledExec++
		for _, name := range bad {
			c, rec := freebsd()
			_, err := r.Exec.Call(c, fn, value.MapOf("name", name))
			if got := reached(rec, name); len(got) > 0 {
				t.Errorf("%s(%q) put the name in argv: %q", fn, name, got)
			}
			if err == nil {
				t.Errorf("%s(%q) was not refused", fn, name)
			}
		}
	}
	for _, fn := range r.States.Names() {
		if !strings.HasPrefix(fn, "service.") {
			continue
		}
		calledStates++
		for _, name := range bad {
			c, rec := freebsd()
			res, err := r.States.Call(c, fn, value.MapOf("name", name))
			if got := reached(rec, name); len(got) > 0 {
				t.Errorf("state %s(%q) put the name in argv: %q", fn, name, got)
			}
			if err == nil && res.Succeeded() {
				t.Errorf("state %s(%q) succeeded: %s", fn, name, res.Comment)
			}
		}
	}
	for _, name := range bad {
		c, rec := freebsd()
		res, err := r.States.CallWatch(c, "service.running", value.MapOf("name", name))
		if got := reached(rec, name); len(got) > 0 {
			t.Errorf("mod_watch(%q) put the name in argv: %q", name, got)
		}
		if err == nil && res.Succeeded() {
			t.Errorf("mod_watch(%q) succeeded: %s", name, res.Comment)
		}
	}
	// The walk itself is the claim, so it must have found something to
	// walk: a registry that had renamed the module would pass vacuously.
	if calledExec < 10 || calledStates < 4 {
		t.Fatalf("walked %d exec functions and %d states; the registry has moved", calledExec, calledStates)
	}

	// And the ordinary names still go through, hyphen, dot, `@` and
	// systemd's escaping included.
	if runtime.GOOS == "windows" {
		// The service control manager comes first there and is an API,
		// so a good name would be asked of the real one.
		return
	}
	for _, name := range []string{"sshd", "avahi-daemon", "getty@tty1", "com.apple.example", `systemd-cryptsetup@luks\x2dabc`} {
		c, rec := freebsd()
		if _, err := r.Exec.Call(c, "service.status", value.MapOf("name", name)); err != nil {
			t.Errorf("%q was refused: %v", name, err)
		}
		if len(rec.Ran) != 1 || strings.Join(rec.Ran[0].Argv, "|") != "service|"+name+"|status" {
			t.Errorf("%q ran %q, want service %s status", name, rec.RanCommands(), name)
		}
	}
}

func hasParam(params []signature.Param, name string) bool {
	for _, p := range params {
		if p.Name == name {
			return true
		}
	}
	return false
}
