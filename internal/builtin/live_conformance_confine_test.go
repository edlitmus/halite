package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The three states that configure a confinement: an AppArmor profile's mode, a
// netplan document, and whether a jail is running.
//
// All three bring their own subject. None of them touches a profile, a network
// interface or a jail the machine came with, and that is what makes them
// runnable on a machine at all -- the alternative for each would be to change
// something the runner depends on to stay reachable or to keep working.
//
//   - `apparmor.mode` writes a profile of its own into /etc/apparmor.d, which
//     is where it has to be because the `aa-*` tools look profiles up by name
//     in that directory. It confines nothing: it is attached to no executable.
//   - `netplan.managed` writes a document naming an interface that does not
//     exist, with `apply` left off -- so real `netplan generate` validates it
//     and nothing reaches an interface. `netplan apply` is the path that would,
//     and no test in this repository runs it (5.38).
//   - `jail.running` writes its own jail configuration and passes it as
//     `config`, so /etc/jail.conf is never opened. That is worth noticing as
//     the opposite of the usual finding: the module takes a `config` argument
//     for an operator's benefit, and it is what lets a case stage a jail
//     without editing the file the host's real jails are defined in.
//
// DIVERGENCE 5.157.

// ---- apparmor ----
//
// The profile is this suite's own, not `live_apparmor_test.go`'s, so the two
// cannot collide on a machine that runs both: that file's cleanup removes the
// profile it wrote and would take this one's with it if they shared a name.
const (
	conformanceAppArmorProfile = liveConformancePrefix + "-aa"
	conformanceAppArmorBody    = "profile " + conformanceAppArmorProfile + " {\n  file,\n}\n"
)

func apparmorCases() []liveCase {
	r := New()
	root := liveRoot()
	path := filepath.Join(liveAppArmorDir, conformanceAppArmorProfile)

	// The kernel's own answer, not the tool's. A mode change that reported
	// success and moved nothing is the failure being looked for, and
	// securityfs is where it would show.
	probe := func() (string, error) {
		profiles, err := apparmorProfiles()
		if err != nil {
			return "", err
		}
		mode, loaded := profiles[conformanceAppArmorProfile]
		if !loaded {
			return "not loaded", nil
		}
		return "in " + mode + " mode", nil
	}

	// Written and parsed on every Setup. `apparmor_parser --replace` on a
	// profile whose file names no flags leaves it in enforce mode, which is
	// what makes this idempotent: the second Setup puts it back to enforce
	// whatever the phase before left it in.
	setup := func() error {
		if err := os.WriteFile(path, []byte(conformanceAppArmorBody), 0o644); err != nil {
			return fmt.Errorf("the case's profile could not be written to %s: %w", path, err)
		}
		if _, err := r.Exec.Call(root, "apparmor.reload", value.MapOf("path", path)); err != nil {
			return fmt.Errorf("the case's profile could not be loaded: %w", err)
		}
		return nil
	}
	cleanup := func() {
		_, _ = root.Run(hexec.Command{
			Argv:           []string{"apparmor_parser", "--remove", path},
			IgnoreExitCode: true,
		})
		// `aa-disable` leaves a symlink behind, which is the whole point of
		// it -- and leaving one on the machine would keep the profile
		// unloaded across a boot that never comes.
		_ = os.Remove(filepath.Join(liveAppArmorDir, "disable", conformanceAppArmorProfile))
		_ = os.Remove(path)
	}

	return []liveCase{{
		Conformance: states.Conformance{
			Name: "apparmor.mode",
			// enforce to complain, rather than to `disable`. Both are
			// changes the state makes; this is the one whose convergence
			// can be read off securityfs, because a disabled profile is
			// absent from it entirely and "absent" is also what a profile
			// that failed to load looks like.
			Args:    value.MapOf("name", conformanceAppArmorProfile, "mode", "complain"),
			Probe:   probe,
			Setup:   setup,
			Cleanup: cleanup,
		},
		platforms: []string{"linux"},
		needs:     []string{"apparmor_parser", "aa-enforce", "aa-complain"},
		// The `aa-*` tools are Python and parse *every* profile under
		// /etc/apparmor.d before doing anything, with their own parser. One
		// file that parser does not understand therefore breaks every mode
		// change on the machine, whatever profile was asked about (5.37,
		// 5.133). The module's own probe answers that, and asking the
		// module rather than carrying a second copy of the answer is the
		// correction `requireModeChanges` already records: two lists of
		// "what counts as broken" were kept in step by hand and were both
		// wrong on the same day.
		unavailable: func(c *hexec.Context) string {
			if usable, why := apparmorToolsUsable(c); !usable {
				return why
			}
			return ""
		},
	}}
}

// ---- netplan ----
//
// The name sorts after anything a machine ships and before
// `live_netplan_test.go`'s, and the interface is deliberately not a real one.
const (
	conformanceNetplanName  = "97-" + liveConformancePrefix + "-probe"
	conformanceNetplanIface = "halcf0probe"
)

func netplanCases() []liveCase {
	root := liveRoot()
	path := filepath.Join(NetplanDir, conformanceNetplanName+".yaml")

	// The file's own bytes, summarised. The mode is part of it because the
	// state manages the mode as well as the contents: netplan refuses to
	// read a document others can read, since it can hold a passphrase.
	probe := func() (string, error) {
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			return "no document", nil
		}
		if err != nil {
			return "", err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d bytes, mode %v", len(body), info.Mode().Perm()), nil
	}
	// Removing the document and regenerating, so the backend files netplan
	// rendered from it go too. This is `generate`, not `apply`: it rewrites
	// /run and touches no interface.
	clear := func() error {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%s could not be removed: %w", path, err)
		}
		res, err := root.Run(hexec.Command{
			Argv:           []string{"netplan", "generate"},
			IgnoreExitCode: true,
		})
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("`netplan generate` failed after removing %s: %s",
				path, strings.TrimSpace(res.Stderr+res.Stdout))
		}
		return nil
	}

	return []liveCase{{
		Conformance: states.Conformance{
			Name: "netplan.managed",
			Args: value.MapOf("name", conformanceNetplanName,
				"config", value.MapOf("network", value.MapOf(
					"version", int64(2),
					"renderer", "networkd",
					"ethernets", value.MapOf(conformanceNetplanIface, value.MapOf(
						"dhcp4", false,
						"optional", true,
					)),
				))),
			Probe:   probe,
			Setup:   clear,
			Cleanup: func() { _ = clear() },
		},
		platforms: []string{"linux"},
		needs:     []string{"netplan"},
	}}
}

// ---- jail ----
//
// Two cases, because `running: false` is a different path through the module
// and the one an estate uses to take a jail down. `jailRunningState` refuses to
// start a jail its configuration does not define, and the refusal names the
// file -- so a case that got the configuration wrong fails with that message
// rather than with jail(8)'s.
func jailCases() []liveCase {
	r := New()
	root := liveRoot()
	name := liveConformancePrefix + "_jail"
	conf := filepath.Join(os.TempDir(), liveConformancePrefix+"-jail.conf")
	jailRoot := filepath.Join(os.TempDir(), liveConformancePrefix+"-jailroot")

	// Every inherited default that would reach outside the case is
	// overridden, for the reason `liveJailConf` records: a host's global
	// block sets `path`, a per-jail `mount.fstab` and `mount.devfs`, none of
	// which exist for a jail invented by a test. Here the file is the case's
	// own and has no global block at all -- but the overrides stay, because
	// jail(8) reads /etc/jail.conf's defaults for a jail named on the command
	// line even when `-f` names another file on some versions, and the cost
	// of being wrong about that is a jail that will not start for a reason
	// nobody reads.
	write := func() error {
		if err := os.MkdirAll(jailRoot, 0o755); err != nil {
			return fmt.Errorf("the case's jail root could not be made: %w", err)
		}
		body := fmt.Sprintf("%s {\n\tpath = \"%s\";\n\tmount.devfs = 0;\n\tmount.fstab = \"\";\n\tpersist;\n}\n",
			name, jailRoot)
		if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
			return fmt.Errorf("the case's jail configuration could not be written: %w", err)
		}
		return nil
	}
	// `jls`, through the module's own reader. A jail that jail(8) claimed to
	// start and that is not in the list is the failure being looked for.
	probe := func() (string, error) {
		jails, err := jailList(root)
		if err != nil {
			return "", err
		}
		if _, up := jails[name]; up {
			return "running", nil
		}
		return "not running", nil
	}
	stop := func() {
		_, _ = root.Run(hexec.Command{
			Argv:           []string{"jail", "-f", conf, "-r", name},
			IgnoreExitCode: true,
		})
	}
	tidy := func() {
		stop()
		_ = os.Remove(conf)
		_ = os.RemoveAll(jailRoot)
	}

	args := func(running bool) *value.Map {
		return value.MapOf("name", name, "running", running, "config", conf)
	}
	onFreeBSD := func(lc liveCase) liveCase {
		lc.platforms = []string{"freebsd"}
		lc.needs = []string{"jail", "jls"}
		return lc
	}

	return []liveCase{
		onFreeBSD(liveCase{
			label: "jail.running (starting)",
			Conformance: states.Conformance{
				Name:  "jail.running",
				Args:  args(true),
				Probe: probe,
				Setup: func() error {
					if err := write(); err != nil {
						return err
					}
					stop()
					return nil
				},
				Cleanup: tidy,
			},
		}),
		onFreeBSD(liveCase{
			label: "jail.running (stopping)",
			Conformance: states.Conformance{
				Name:  "jail.running",
				Args:  args(false),
				Probe: probe,
				Setup: func() error {
					if err := write(); err != nil {
						return err
					}
					// Started through the state, the module's own way in.
					_, err := r.States.Call(root, "jail.running", args(true))
					return err
				},
				Cleanup: tidy,
			},
		}),
	}
}

// conformanceConfineCases is the three families above, in one list.
func conformanceConfineCases() []liveCase {
	var cases []liveCase
	cases = append(cases, apparmorCases()...)
	cases = append(cases, netplanCases()...)
	cases = append(cases, jailCases()...)
	return cases
}
