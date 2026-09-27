package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// SPEC 11.6's conformance harness, driven against a real machine.
//
// # Why this could not be part of the unit suite
//
// The harness *applies* a state, twice, for real. That is what makes
// idempotence observable and it is also its ceiling: eighty-two of the
// hundred and thirty-two state functions change the machine they run on, so
// a case for `pkg.installed` in `go test ./...` installs a package on
// whoever ran it — and on this project the development host is a node the
// fleet manages. DIVERGENCE 5.157 recorded them as belonging "to a live test
// on a disposable host". This is that test.
//
// # The gate, and why HALITE_SYSTEM_LIVE is not enough
//
// The existing live tests take `HALITE_SYSTEM_LIVE=1` to mean "this machine
// can be thrown away", and they are careful: each captures what it changed
// and restores it. These cannot make that promise as cheaply. A conformance
// case creates an account, installs a package, loads a kernel module — and
// then the harness runs the state a second and third time, so the machine
// passes through four states rather than two.
//
// So this suite needs `HALITE_CONFORMANCE_LIVE=1` as well, set by
// `contrib/tofu/lab.sh` and by the `fleet.yml` legs, which are the two
// places where the machine is genuinely disposable. A host with
// `HALITE_SYSTEM_LIVE=1` alone gets a skip that says which variable is
// missing. That is deliberate belt and braces: `HALITE_SYSTEM_LIVE` has been
// set by hand on real machines to answer a question about one module, and
// none of those sessions signed up for a package manager being driven.
//
// # What a case may do
//
// Confine the effect to something the case itself made, wherever that is
// possible: an account named after this suite, a group of its own, a service
// unit the case writes, a volume group on a loopback file. Where it is not
// possible — a kernel parameter, a firewall's ruleset — the case says so in
// its own comment and restores what it found.
//
// # Reading the output
//
// Every case that does not run says why, and the summary at the end counts
// them, because "the conformance suite passed" on a machine where sixty of
// them skipped is a sentence that means very little. `lab.sh` runs this with
// `-v`, so the skip lines are in the log an operator reads.

// liveGate reports whether this machine has been offered up for states to be
// applied to it, and skips with the reason when it has not.
func liveGate(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 on a machine you can throw away; " +
			"this applies state modules to it for real")
	}
	if os.Getenv("HALITE_CONFORMANCE_LIVE") != "1" {
		t.Skip("HALITE_SYSTEM_LIVE is set but HALITE_CONFORMANCE_LIVE is not. " +
			"This suite drives package managers, accounts and kernel modules through " +
			"four states each, which is more than the other live tests ask for, so it " +
			"needs the second variable as well. contrib/tofu/lab.sh and fleet.yml set both.")
	}
	if os.Geteuid() != 0 {
		t.Fatal("HALITE_CONFORMANCE_LIVE is set and this is not root; every state here needs it")
	}
	return realCtx(t)
}

// liveRoot is realCtx without a *testing.T.
//
// The builders below must be callable from the unit suite's accounting,
// which reads the case names to know what is covered -- and a builder that
// needed a `t` would tempt somebody into `t.Cleanup`, whose handlers here
// run `user.absent` against the machine. Construction touches nothing; every
// effect belongs to a case's Setup, its Cleanup, or the state under test.
func liveRoot() *exec.Context {
	c := newCtx(false)
	c.Runner = &exec.OSRunner{}
	return c
}

// liveCase is a conformance case together with the machine it needs.
type liveCase struct {
	states.Conformance
	// label distinguishes two cases for one function; the function's name
	// is used when it is empty.
	label string
	// platforms are the GOOS values this case applies to. Empty means any.
	platforms []string
	// needs are programs that must be on PATH for the case to mean
	// anything. A case whose subsystem is absent skips, saying which
	// program was missing -- `nftables` on a machine with only iptables is
	// not a failure.
	needs []string
	// requiresFile is a path that must exist, for a subsystem that is a
	// file rather than a program: `/proc/sys` for sysctl, a jail
	// configuration for jail.
	requiresFile string
	// requiresKmod is a kernel module this machine must be able to load.
	// Asked with `modprobe -n`, which answers without loading anything:
	// AlmaLinux 8's 4.18 kernel has no `netdevsim`, and a kernel built
	// without a module is a machine that cannot be asked the question
	// rather than a defect. DIVERGENCE 5.126 made the same allowance.
	requiresKmod string
	// needsNetns marks a case that must run inside a private network
	// namespace, because it edits a packet filter. The main suite skips
	// these and TestLiveConformanceNetfilter runs them after
	// re-executing itself into a namespace -- one list, two drivers, so
	// the accounting cannot miss them.
	needsNetns bool
}

// skipReason reports why this case cannot run here, or "" when it can.
func (lc liveCase) skipReason(c *exec.Context) string {
	if len(lc.platforms) > 0 {
		ok := false
		for _, p := range lc.platforms {
			if p == runtime.GOOS {
				ok = true
			}
		}
		if !ok {
			return fmt.Sprintf("this case is for %s and this is %s",
				strings.Join(lc.platforms, " or "), runtime.GOOS)
		}
	}
	for _, need := range lc.needs {
		if c.Which(need) == "" {
			return fmt.Sprintf("%s is not on this machine", need)
		}
	}
	if lc.requiresFile != "" {
		if _, err := os.Stat(lc.requiresFile); err != nil {
			return fmt.Sprintf("%s is not present here", lc.requiresFile)
		}
	}
	if lc.requiresKmod != "" {
		res, err := c.Run(exec.Command{
			Argv:           []string{"modprobe", "-n", lc.requiresKmod},
			IgnoreExitCode: true,
		})
		if err != nil || res.Code != 0 {
			return fmt.Sprintf("this kernel has no %s to load: %s", lc.requiresKmod,
				strings.TrimSpace(res.Stderr+res.Stdout))
		}
	}
	return ""
}

func (lc liveCase) name() string {
	if lc.label != "" {
		return lc.label
	}
	return lc.Name
}

// TestLiveConformanceOnThisMachine drives the machine-level states through
// SPEC 11.6's harness.
func TestLiveConformanceOnThisMachine(t *testing.T) {
	c := liveGate(t)
	r := New()

	// One context per phase, rebuilt so that nothing a previous phase put
	// on the context leaks into the next -- and with a real runner,
	// because a recorded command changes nothing and would make every case
	// here a pass about nothing.
	ctx := func(test bool) *exec.Context {
		n := liveRoot()
		n.Test = test
		return n
	}

	all := liveConformanceCases()
	var cases []liveCase
	for _, lc := range all {
		if !lc.needsNetns {
			cases = append(cases, lc)
		}
	}
	ran, skipped := 0, map[string]string{}
	for _, lc := range cases {
		lc := lc
		if lc.needsNetns {
			continue // TestLiveConformanceNetfilter drives these
		}
		t.Run(lc.name(), func(t *testing.T) {
			if why := lc.skipReason(c); why != "" {
				skipped[lc.name()] = why
				t.Skipf("%s", why)
			}
			ran++
			for _, f := range lc.Check(r.States, ctx) {
				t.Errorf("%s", f)
			}
		})
	}

	// What ran, and what did not and why. A conformance suite that skipped
	// most of itself is not a conformance suite, and the only way to know
	// is to count -- 5.124's lesson about a leg whose filter matched
	// nothing, and 5.151's about a test that skipped inside a green run.
	t.Run("what ran here", func(t *testing.T) {
		names := make([]string, 0, len(skipped))
		for n := range skipped {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			t.Logf("skipped %s: %s", n, skipped[n])
		}
		t.Logf("%s/%s: %d of %d live conformance cases ran, %d skipped",
			runtime.GOOS, runtime.GOARCH, ran, len(cases), len(skipped))
		if ran == 0 {
			t.Errorf("no live conformance case ran on this machine, so this suite "+
				"proved nothing: every one of the %d skipped", len(cases))
		}
	})
}

// liveConformanceCases builds every case. It must not touch the machine:
// everything a case does belongs in its Setup, its Probe or the state.
func liveConformanceCases() []liveCase {
	var cases []liveCase
	cases = append(cases, accountCases()...)
	cases = append(cases, hostsFileCases()...)
	cases = append(cases, scheduleCases()...)
	cases = append(cases, packageCases()...)
	cases = append(cases, kernelCases()...)
	cases = append(cases, serviceCases()...)
	cases = append(cases, rcConfCases()...)
	cases = append(cases, netfilterCases()...)
	return cases
}

// suffix keeps the names this suite makes distinct from anything on the
// machine, and recognisable when one is left behind by a crash.
const liveConformancePrefix = "halitecf"

// ---- accounts ----
//
// `user` and `group` change the machine's account database, which is why
// they are here rather than in the unit suite. The names are this suite's
// own, and every case removes what it made even when it failed: a machine
// left with an account is one somebody has to clean by hand, and in the lab
// it would be destroyed with the instance but these are meant to be
// runnable on a real host too.
func accountCases() []liveCase {
	r := New()
	root := liveRoot()

	user := liveConformancePrefix + "u"
	group := liveConformancePrefix + "g"

	// removeAccount and removeGroup go through the modules rather than
	// through useradd, so a cleanup cannot pass while the module is
	// broken -- and they are best effort, because a cleanup that fails the
	// test would hide the result the test came for.
	removeUser := func() {
		_, _ = r.States.Call(root, "user.absent", value.MapOf("name", user, "purge", true))
	}
	removeGroup := func() {
		_, _ = r.States.Call(root, "group.absent", value.MapOf("name", group))
	}

	accountProbe := func(name string) func() (string, error) {
		return func() (string, error) {
			out, err := r.Exec.Call(root, "user.info", value.MapOf("name", name))
			if err != nil {
				// An account that is not there is the ordinary case, not
				// an error, and the probe has to say so rather than fail.
				return "absent", nil
			}
			m, ok := out.(*value.Map)
			if !ok || m.Len() == 0 {
				return "absent", nil
			}
			uid, _ := m.Get("uid")
			shell, _ := m.Get("shell")
			return fmt.Sprintf("uid=%v shell=%v", uid, shell), nil
		}
	}
	groupProbe := func(name string) func() (string, error) {
		return func() (string, error) {
			out, err := r.Exec.Call(root, "group.info", value.MapOf("name", name))
			if err != nil {
				return "absent", nil
			}
			m, ok := out.(*value.Map)
			if !ok || m.Len() == 0 {
				return "absent", nil
			}
			gid, _ := m.Get("gid")
			return fmt.Sprintf("gid=%v", gid), nil
		}
	}

	return []liveCase{
		{
			Conformance: states.Conformance{
				Name: "group.present",
				Args: value.MapOf("name", group),
				// `system` so the gid comes from the range a platform
				// reserves, which keeps this out of the range a real
				// account would be given next.
				Probe:   groupProbe(group),
				Setup:   func() error { removeGroup(); return nil },
				Cleanup: removeGroup,
			},
		},
		{
			Conformance: states.Conformance{
				Name:  "group.absent",
				Args:  value.MapOf("name", group),
				Probe: groupProbe(group),
				Setup: func() error {
					_, err := r.States.Call(root, "group.present", value.MapOf("name", group))
					return err
				},
				Cleanup: removeGroup,
			},
		},
		{
			Conformance: states.Conformance{
				Name: "user.present",
				// No `groups`: supplementary membership is a second
				// subsystem and belongs to its own case, and getting it
				// wrong here would fail this one for a reason that is not
				// about `user.present`.
				Args:    value.MapOf("name", user, "shell", "/bin/sh", "createhome", false),
				Probe:   accountProbe(user),
				Setup:   func() error { removeUser(); return nil },
				Cleanup: removeUser,
			},
		},
		{
			Conformance: states.Conformance{
				Name:  "user.absent",
				Args:  value.MapOf("name", user, "purge", true),
				Probe: accountProbe(user),
				Setup: func() error {
					_, err := r.States.Call(root, "user.present",
						value.MapOf("name", user, "shell", "/bin/sh", "createhome", false))
					return err
				},
				Cleanup: removeUser,
			},
		},
	}
}

// ---- the hosts file ----
//
// `/etc/hosts` is the machine's, so this is a live case; but the name it
// manages is this suite's own and no resolution depends on it.
func hostsFileCases() []liveCase {
	r := New()
	root := liveRoot()

	name := liveConformancePrefix + ".invalid"
	const addr = "203.0.113.42" // TEST-NET-3, which routes nowhere.

	hostsProbe := func() (string, error) {
		b, err := os.ReadFile(HostsPath)
		if err != nil {
			return "", err
		}
		var found []string
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, name) {
				found = append(found, strings.TrimSpace(line))
			}
		}
		if len(found) == 0 {
			return "absent", nil
		}
		sort.Strings(found)
		return strings.Join(found, " | "), nil
	}
	remove := func() {
		_, _ = r.States.Call(root, "host.absent", value.MapOf("name", name))
	}
	return []liveCase{
		{
			Conformance: states.Conformance{
				Name:    "host.present",
				Args:    value.MapOf("name", name, "ip", addr),
				Probe:   hostsProbe,
				Setup:   func() error { remove(); return nil },
				Cleanup: remove,
			},
		},
		{
			Conformance: states.Conformance{
				Name:  "host.absent",
				Args:  value.MapOf("name", name),
				Probe: hostsProbe,
				Setup: func() error {
					_, err := r.States.Call(root, "host.present",
						value.MapOf("name", name, "ip", addr))
					return err
				},
				Cleanup: remove,
			},
		},
	}
}

// ---- cron and at ----
//
// Both write a real queue for a real account. The command is a true(1) that
// does nothing if it ever fires, and the schedule is far enough out that it
// will not.
func scheduleCases() []liveCase {
	r := New()
	root := liveRoot()

	const command = "/usr/bin/true # halite conformance"
	identifier := liveConformancePrefix + "-cron"

	cronProbe := func() (string, error) {
		out, err := r.Exec.Call(root, "cron.raw_cron", value.MapOf("user", "root"))
		if err != nil {
			return "absent", nil
		}
		raw, _ := out.(string)
		var found []string
		for _, line := range strings.Split(raw, "\n") {
			if strings.Contains(line, identifier) || strings.Contains(line, command) {
				found = append(found, strings.TrimSpace(line))
			}
		}
		if len(found) == 0 {
			return "absent", nil
		}
		sort.Strings(found)
		return strings.Join(found, " | "), nil
	}
	removeCron := func() {
		_, _ = r.States.Call(root, "cron.absent",
			value.MapOf("name", command, "user", "root", "identifier", identifier))
	}

	cronArgs := func() *value.Map {
		return value.MapOf("name", command, "user", "root",
			"identifier", identifier, "minute", "17", "hour", "4")
	}

	atIdentifier := liveConformancePrefix + "-at"
	atCommand := "/usr/bin/true"
	// The queue rendered stably. The first version of this returned
	// `fmt.Sprintf("%v", out)` over the list `at.atq` gives back, which is
	// a slice of maps -- so it printed Go pointer addresses, they differed
	// between two calls that had changed nothing, and the harness reported
	// "test mode changed the system: the probe went from
	// [0x3107ee2fc580] to [0x3107ee2fce00]". A false accusation by the
	// instrument, of exactly the kind this project keeps making: the
	// reading looked like evidence because it had the shape of one.
	atProbe := func() (string, error) {
		out, err := r.Exec.Call(root, "at.atq", value.NewMap(0))
		if err != nil {
			return "absent", nil
		}
		jobs, ok := out.([]any)
		if !ok {
			return "no queue", nil
		}
		var lines []string
		for _, j := range jobs {
			m, ok := j.(*value.Map)
			if !ok {
				continue
			}
			// The job id is deliberately left out: `at` allocates a new
			// one each time, so including it would make the probe differ
			// between runs for a reason that is not a change.
			when, _ := m.Get("date")
			queue, _ := m.Get("queue")
			lines = append(lines, fmt.Sprintf("queue=%v when=%v", queue, when))
		}
		if len(lines) == 0 {
			return "empty queue", nil
		}
		sort.Strings(lines)
		return strings.Join(lines, " | "), nil
	}
	removeAt := func() {
		_, _ = r.States.Call(root, "at.absent",
			value.MapOf("name", atCommand, "identifier", atIdentifier))
	}

	return []liveCase{
		{
			Conformance: states.Conformance{
				Name:    "cron.present",
				Args:    cronArgs(),
				Probe:   cronProbe,
				Setup:   func() error { removeCron(); return nil },
				Cleanup: removeCron,
			},
			needs: []string{"crontab"},
		},
		{
			Conformance: states.Conformance{
				Name: "cron.absent",
				Args: value.MapOf("name", command, "user", "root", "identifier", identifier),
				// The probe is the point here: `absent` that reported
				// success without editing the crontab would pass every
				// other phase.
				Probe: cronProbe,
				Setup: func() error {
					_, err := r.States.Call(root, "cron.present", cronArgs())
					return err
				},
				Cleanup: removeCron,
			},
			needs: []string{"crontab"},
		},
		{
			Conformance: states.Conformance{
				Name: "at.present",
				Args: value.MapOf("name", atCommand, "timespec", "now + 25 hours",
					"identifier", atIdentifier),
				Probe:   atProbe,
				Setup:   func() error { removeAt(); return nil },
				Cleanup: removeAt,
			},
			needs: []string{"at", "atq"},
		},
		{
			Conformance: states.Conformance{
				Name:  "at.absent",
				Args:  value.MapOf("name", atCommand, "identifier", atIdentifier),
				Probe: atProbe,
				Setup: func() error {
					_, err := r.States.Call(root, "at.present",
						value.MapOf("name", atCommand, "timespec", "now + 25 hours",
							"identifier", atIdentifier))
					return err
				},
				Cleanup: removeAt,
			},
			needs: []string{"at", "atq"},
		},
	}
}

// ---- packages ----
//
// The module the whole fleet rests on, and the one that cannot be driven
// anywhere but here: `pkg.installed` in a unit suite installs a package on
// whoever ran it.
//
// `tree` is the package, chosen because it is small, has no service, and
// exists in Debian, Ubuntu, the RedHat family, Alpine, Arch, openSUSE and
// FreeBSD ports. A machine whose repositories do not have it fails rather
// than skips, deliberately: "this distribution has no tree" is a thing worth
// discovering, and a skip would hide it behind the same silence as a machine
// with no package manager at all.
func packageCases() []liveCase {
	r := New()
	root := liveRoot()
	const pkg = "tree"

	installed := func() (string, error) {
		out, err := r.Exec.Call(root, "pkg.version", value.MapOf("name", pkg))
		if err != nil {
			return "absent", nil
		}
		v := fmt.Sprintf("%v", out)
		if strings.TrimSpace(v) == "" || v == "<nil>" {
			return "absent", nil
		}
		return "installed " + v, nil
	}
	remove := func() {
		_, _ = r.States.Call(root, "pkg.removed", value.MapOf("name", pkg))
	}
	install := func() error {
		_, err := r.States.Call(root, "pkg.installed", value.MapOf("name", pkg))
		return err
	}

	return []liveCase{
		{
			Conformance: states.Conformance{
				Name:    "pkg.installed",
				Args:    value.MapOf("name", pkg),
				Probe:   installed,
				Setup:   func() error { remove(); return nil },
				Cleanup: remove,
			},
		},
		{
			Conformance: states.Conformance{
				Name:    "pkg.removed",
				Args:    value.MapOf("name", pkg),
				Probe:   installed,
				Setup:   install,
				Cleanup: remove,
			},
		},
	}
}

// ---- the kernel ----
//
// A real module loaded into a real kernel, and a real kernel parameter set.
// Neither can be done in a container: `/proc/sys` is read-only there and a
// container that remounted it would be writing to the kernel of whoever ran
// the test, which is the reasoning `live_system_test.go` already records.
//
// `netdevsim` is the module, for the reason `live_modprobe_test.go` gives: it
// is a simulated network device with no hardware behind it, and `modprobe -n`
// can say whether this kernel has one before anything is loaded.
func kernelCases() []liveCase {
	r := New()
	root := liveRoot()

	const mod = "netdevsim"
	loaded := func() (string, error) {
		out, err := r.Exec.Call(root, "modprobe.is_loaded", value.MapOf("name", mod))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("loaded=%v", out), nil
	}
	unload := func() {
		_, _ = r.States.Call(root, "kmod.absent", value.MapOf("name", mod))
	}

	cases := []liveCase{
		{
			Conformance: states.Conformance{
				Name: "kmod.present",
				// No `persist`: writing where the machine loads modules at
				// boot is a change that outlives the test, and the boot
				// half has its own coverage in the modprobe tests.
				Args:    value.MapOf("name", mod),
				Probe:   loaded,
				Setup:   func() error { unload(); return nil },
				Cleanup: unload,
			},
			platforms:    []string{"linux"},
			needs:        []string{"modprobe"},
			requiresKmod: mod,
		},
		{
			Conformance: states.Conformance{
				Name:  "kmod.absent",
				Args:  value.MapOf("name", mod),
				Probe: loaded,
				Setup: func() error {
					_, err := r.States.Call(root, "kmod.present", value.MapOf("name", mod))
					return err
				},
				Cleanup: unload,
			},
			platforms:    []string{"linux"},
			needs:        []string{"modprobe"},
			requiresKmod: mod,
		},
	}

	// sysctl. The parameter is the harmless one `live_system_test.go`
	// already picked per platform, and `config` points at a file of this
	// suite's own so that the *persistent* half does not edit the
	// machine's sysctl configuration -- the running half is real, and is
	// put back.
	if key, want := conformanceSysctl(); key != "" {
		before := func() string {
			out, err := r.Exec.Call(root, "sysctl.get", value.MapOf("name", key))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(fmt.Sprintf("%v", out))
		}()
		restore := func() {
			if before == "" {
				return
			}
			_, _ = r.Exec.Call(root, "sysctl.assign", value.MapOf("name", key, "value", before))
		}
		cases = append(cases, liveCase{
			Conformance: states.Conformance{
				Name: "sysctl.present",
				Args: value.MapOf("name", key, "value", want,
					"config", filepath.Join(os.TempDir(), liveConformancePrefix+"-sysctl.conf")),
				Probe: func() (string, error) {
					out, err := r.Exec.Call(root, "sysctl.get", value.MapOf("name", key))
					if err != nil {
						return "", err
					}
					return strings.TrimSpace(fmt.Sprintf("%v", out)), nil
				},
				Setup:   func() error { restore(); return nil },
				Cleanup: restore,
			},
			needs: []string{"sysctl"},
		})
	}
	return cases
}

// conformanceSysctl is a parameter that is harmless to set and a value that
// differs from any default, per platform. The same choice
// `live_system_test.go` makes, kept beside it in spirit: a second opinion
// about which parameter is safe is the last thing this needs.
func conformanceSysctl() (name, value string) {
	switch runtime.GOOS {
	case "linux":
		return "vm.swappiness", "61"
	case "freebsd", "openbsd", "netbsd", "darwin":
		return "kern.ipc.somaxconn", "1023"
	}
	return "", ""
}

// ---- services ----
//
// The unit is this suite's own, written into `/run/systemd/system` so that a
// machine which is rebooted rather than destroyed comes back without it.
// `service.enabled` is the exception: enabling writes a symlink under
// `/etc/systemd/system`, which is what "at boot" means, and the case removes
// it again.
func serviceCases() []liveCase {
	r := New()
	root := liveRoot()

	name := liveConformancePrefix + "svc"
	unitPath := "/run/systemd/system/" + name + ".service"
	const unit = `[Unit]
Description=halite conformance probe

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/true

[Install]
WantedBy=multi-user.target
`
	writeUnit := func() error {
		if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
			return err
		}
		_, err := root.Run(exec.Command{Argv: []string{"systemctl", "daemon-reload"}})
		return err
	}
	stop := func() {
		for _, argv := range [][]string{
			{"systemctl", "stop", name},
			{"systemctl", "disable", name},
		} {
			_, _ = root.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		}
	}
	teardown := func() {
		stop()
		_ = os.Remove(unitPath)
		_, _ = root.Run(exec.Command{
			Argv:           []string{"systemctl", "daemon-reload"},
			IgnoreExitCode: true,
		})
	}

	// The probe reads systemctl rather than the module, so a module that
	// reported a change it did not make has nowhere to hide.
	probe := func() (string, error) {
		active, _ := root.Run(exec.Command{
			Argv: []string{"systemctl", "is-active", name}, IgnoreExitCode: true})
		enabled, _ := root.Run(exec.Command{
			Argv: []string{"systemctl", "is-enabled", name}, IgnoreExitCode: true})
		return fmt.Sprintf("active=%s enabled=%s",
			strings.TrimSpace(active.Stdout), strings.TrimSpace(enabled.Stdout)), nil
	}

	systemd := func(lc liveCase) liveCase {
		lc.platforms = []string{"linux"}
		lc.needs = []string{"systemctl"}
		lc.requiresFile = "/run/systemd/system"
		return lc
	}

	return []liveCase{
		systemd(liveCase{Conformance: states.Conformance{
			Name:  "service.running",
			Args:  value.MapOf("name", name),
			Probe: probe,
			Setup: func() error {
				if err := writeUnit(); err != nil {
					return err
				}
				stop()
				return nil
			},
			Cleanup: teardown,
		}}),
		systemd(liveCase{Conformance: states.Conformance{
			Name:  "service.dead",
			Args:  value.MapOf("name", name),
			Probe: probe,
			Setup: func() error {
				if err := writeUnit(); err != nil {
					return err
				}
				_, err := r.States.Call(root, "service.running", value.MapOf("name", name))
				return err
			},
			Cleanup: teardown,
		}}),
		systemd(liveCase{Conformance: states.Conformance{
			Name:  "service.enabled",
			Args:  value.MapOf("name", name),
			Probe: probe,
			Setup: func() error {
				if err := writeUnit(); err != nil {
					return err
				}
				_, _ = root.Run(exec.Command{
					Argv: []string{"systemctl", "disable", name}, IgnoreExitCode: true})
				return nil
			},
			Cleanup: teardown,
		}}),
		systemd(liveCase{Conformance: states.Conformance{
			Name:  "service.disabled",
			Args:  value.MapOf("name", name),
			Probe: probe,
			Setup: func() error {
				if err := writeUnit(); err != nil {
					return err
				}
				_, err := r.States.Call(root, "service.enabled", value.MapOf("name", name))
				return err
			},
			Cleanup: teardown,
		}}),
	}
}

// ---- rc.conf ----
//
// `sysrc` is FreeBSD's, and the `file` argument points these at an rc.conf of
// this suite's own. That makes the effect confined -- rc.conf is read at boot
// and nothing else -- while the tool doing the reading and writing is the
// real `sysrc`, which is the half a fixture cannot check. DIVERGENCE 5.101
// is the entry about `sysrc` output this project got wrong from
// documentation.
func rcConfCases() []liveCase {
	r := New()
	root := liveRoot()

	path := filepath.Join(os.TempDir(), liveConformancePrefix+"-rc.conf")
	const setting = "halite_conformance_enable"

	probe := func() (string, error) {
		b, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return "absent", nil
		}
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
	blank := func() error { return os.WriteFile(path, []byte("# halite conformance\n"), 0o644) }
	withValue := func() error {
		if err := blank(); err != nil {
			return err
		}
		_, err := r.States.Call(root, "sysrc.present",
			value.MapOf("name", setting, "value", "NO", "file", path))
		return err
	}
	remove := func() { _ = os.Remove(path) }

	freebsd := func(lc liveCase) liveCase {
		lc.platforms = []string{"freebsd"}
		lc.needs = []string{"sysrc"}
		return lc
	}

	return []liveCase{
		freebsd(liveCase{Conformance: states.Conformance{
			Name:    "sysrc.present",
			Args:    value.MapOf("name", setting, "value", "YES", "file", path),
			Probe:   probe,
			Setup:   blank,
			Cleanup: remove,
		}}),
		freebsd(liveCase{Conformance: states.Conformance{
			Name:    "sysrc.managed",
			Args:    value.MapOf("name", setting, "value", "YES", "file", path),
			Probe:   probe,
			Setup:   blank,
			Cleanup: remove,
		}}),
		freebsd(liveCase{Conformance: states.Conformance{
			Name:    "sysrc.absent",
			Args:    value.MapOf("name", setting, "file", path),
			Probe:   probe,
			Setup:   withValue,
			Cleanup: remove,
		}}),
	}
}
