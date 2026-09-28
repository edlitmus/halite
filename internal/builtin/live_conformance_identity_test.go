package builtin

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The three states that act on the machine as a whole: what it is called,
// what time it thinks it is, and whether it is about to go away.
//
// # Why these were the last to get cases
//
// Every other conformance case can name something it made -- a directory, a
// loop device, a network namespace, a jail -- and confine its effect to that.
// These three cannot. There is one hostname, one time zone and one shutdown
// schedule per machine, so a case for any of them changes the machine itself
// and the only safety available is to put it back.
//
// That is the same trade `live_system_test.go` and `live_mac_timezone_test.go`
// already made, and the same reasoning: `HALITE_CONFORMANCE_LIVE=1` is a
// statement that this machine is disposable, and both of those tests have been
// renaming CI runners for months. What the harness adds over them is the two
// phases a hand-written test almost never has -- a *second* test-mode run
// against a system that already matches, and the check that the real run's
// change set matched what test mode predicted.
//
// # reboot.scheduled keeps a gate of its own
//
// It is the one case in this suite behind a second variable,
// `HALITE_REBOOT_LIVE=1`, and `needsRebootGate` is the field that says so.
// The reason is not that a reboot cannot be undone -- `reboot.cancel` undoes
// it, and the existing `TestLiveReboot*` tests have always been careful about
// that -- but that the *failure mode* is unlike any other here. A case that
// leaves a package installed leaves a package installed. A case that leaves a
// shutdown pending takes the machine down later, when nobody is watching, and
// that gets worse on its own.
//
// Two things follow from that, and both are done rather than promised:
//
//   - The delay is two hours, matching
//     `TestLiveRebootSchedulesAndCancelsWithoutRebooting`'s reasoning: long
//     enough that if every safeguard here failed at once a person would have
//     two hours to find the `shutdown` and kill it, and far beyond any leg's
//     own timeout.
//   - **The workflow asserts nothing was left pending**, because the harness
//     cannot. `Conformance.Cleanup` is a `func()` with no `*testing.T`, so a
//     cancel that failed there would be silent. The linux leg's "the machine
//     was put back" step reads
//     /run/systemd/shutdown/scheduled and the process table and fails the job.
//
// And the gate is set on the linux leg only. On FreeBSD a pending shutdown is
// not cancelled by a flag -- `shutdown -c` there power cycles the machine, as
// 5.99 recorded the hard way -- it is cancelled by finding the pid and sending
// it TERM. That is a fine thing for a test to do on a hosted VM that is
// thrown away in minutes; it is not a thing to do to the emulated FreeBSD VM
// the whole freebsd leg runs inside, where losing the machine ends the run.
// So `reboot.scheduled` is demonstrated on Linux and its FreeBSD branch stays
// covered by unit tests and by the manual-page test, which is stated here
// rather than left for somebody to infer from a green leg.
//
// DIVERGENCE 5.157.

// ---- hostname ----
//
// Both halves of the name, which is what makes this worth a case at all: the
// running name and the one in the file a boot reads. `hostnameSystem` reports
// them separately, and the change set carrying only one of them is the case an
// operator most needs to see.
//
// The original is captured on the first Setup and put back by every subsequent
// Setup and by Cleanup. Through the module's own `hostname.set_hostname`,
// deliberately -- restoring it by hand would be a second implementation of the
// thing under test, and one that agreed with a broken module would hide it.
func hostnameCases() []liveCase {
	r := New()
	root := liveRoot()
	want := liveConformancePrefix + "-host"

	// The name to go back to, read once. Empty until the first Setup, which
	// is why construction touches nothing.
	var original string
	capture := func() error {
		if original != "" {
			return nil
		}
		persistent, err := r.Exec.Call(root, "hostname.get_persistent", value.NewMap(0))
		if err != nil {
			return fmt.Errorf("this machine's persistent hostname could not be read: %w", err)
		}
		if s, _ := persistent.(string); strings.TrimSpace(s) != "" {
			original = strings.TrimSpace(s)
			return nil
		}
		// macOS has no /etc/hostname and nothing reads one, so the
		// persistent name can legitimately be empty there; the running
		// name is then the only thing to go back to.
		running, err := r.Exec.Call(root, "hostname.get_hostname", value.NewMap(0))
		if err != nil {
			return fmt.Errorf("this machine's hostname could not be read: %w", err)
		}
		s, _ := running.(string)
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("this machine reports no hostname at all, so there is nothing to put back")
		}
		original = strings.TrimSpace(s)
		return nil
	}
	restore := func() error {
		if original == "" {
			return nil
		}
		if _, err := r.Exec.Call(root, "hostname.set_hostname",
			value.MapOf("hostname", original)); err != nil {
			return fmt.Errorf("this machine could not be renamed back to %s: %w", original, err)
		}
		return nil
	}

	// Both halves, in one string, so that a module that changed one and
	// reported both is visible.
	probe := func() (string, error) {
		running, err := r.Exec.Call(root, "hostname.get_hostname", value.NewMap(0))
		if err != nil {
			return "", err
		}
		persistent, err := r.Exec.Call(root, "hostname.get_persistent", value.NewMap(0))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("running=%v persistent=%v", running, persistent), nil
	}

	return []liveCase{{
		Conformance: states.Conformance{
			Name:  "hostname.system",
			Args:  value.MapOf("name", want),
			Probe: probe,
			Setup: func() error {
				if err := capture(); err != nil {
					return err
				}
				return restore()
			},
			Cleanup: func() { _ = restore() },
		},
		// The module declares unixOnly and the registry enforces it.
		platforms: []string{"linux", "freebsd", "darwin"},
	}}
}

// ---- timezone ----
//
// The zone is chosen from the machine's own list rather than written down
// here, and that is not tidiness. A Mac takes `systemsetup -listtimezones`'s
// names and not the tz tree's: it has no `UTC` at all, and none of the
// `backward` aliases, which is what 5.131 found when the state's
// unknown-zone refusal waved through a name the tool then rejected. Windows
// takes its own display names. Reading the list and picking from it is the
// only spelling that is right on all four platforms, and it means the case
// fails for the right reason on a machine with no zone database instead of
// failing on the name.
//
// The candidates are tried in order and the first one this machine both has
// and is not already in is used. `Etc/UTC` before `UTC` because Debian has
// both and systemd prefers the former; `GMT` for the Mac, which has neither.
//
// # On a Mac the zone is put back by relinking, not through the state
//
// Which looks like a shortcut and is the opposite. The macos runner is in
// `UTC`, and `systemsetup -settimezone UTC` refuses it -- "UTC is not a valid
// timezone" -- so this machine's own zone is one the tool will not set. A
// restore through the state would therefore fail, and leave the Mac in the
// zone this case moved it to. 5.131 is where that was found, and
// `TestLiveMacTimezoneSetsTheZoneAndPutsItBack` already restores by relinking
// /etc/localtime and asserts afterwards that it worked, on this same runner.
//
// Everywhere else the state is used, and must be: on a systemd node a link
// written behind `timedatectl`'s back is reverted, which the module's own
// comment says at length.
func timezoneCases() []liveCase {
	r := New()
	root := liveRoot()
	args := value.NewMap(1)

	var original, originalLink string
	probe := func() (string, error) {
		out, err := r.Exec.Call(root, "timezone.get_zone", value.NewMap(0))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%v", out), nil
	}
	restore := func() error {
		if runtime.GOOS == "darwin" {
			if originalLink == "" {
				return nil
			}
			// Written beside the target and renamed over it, because
			// os.Symlink refuses a path that exists and removing
			// /etc/localtime first would leave the Mac with no zone at all
			// if the symlink then failed.
			tmp := localtimePath + ".halite-conformance"
			_ = os.Remove(tmp)
			if err := os.Symlink(originalLink, tmp); err != nil {
				return fmt.Errorf("this Mac's zone link could not be restored: %w", err)
			}
			if err := os.Rename(tmp, localtimePath); err != nil {
				return fmt.Errorf("this Mac's zone link could not be restored: %w", err)
			}
			return nil
		}
		if original == "" {
			return nil
		}
		// Checked rather than assumed: the state reports a refusal as a
		// failed result and not as an error, so ignoring the result is how
		// a machine gets left in the wrong zone quietly.
		res, err := r.States.Call(root, "timezone.system", value.MapOf("name", original))
		if err != nil {
			return err
		}
		if res.Failed() {
			return fmt.Errorf("this machine could not be put back into %s: %s", original, res.Comment)
		}
		return nil
	}

	// A zone this machine has, that it is not in. Both halves matter: a
	// candidate the machine does not have would fail, and one it is
	// already in would leave the harness nothing to change and it says so
	// rather than passing.
	pick := func() (string, error) {
		listed, err := r.Exec.Call(root, "timezone.list_zones", value.NewMap(0))
		if err != nil {
			return "", fmt.Errorf("this machine's zone list could not be read: %w", err)
		}
		have := map[string]bool{}
		switch zones := listed.(type) {
		case []any:
			for _, z := range zones {
				have[fmt.Sprintf("%v", z)] = true
			}
		case []string:
			for _, z := range zones {
				have[z] = true
			}
		default:
			return "", fmt.Errorf("timezone.list_zones returned %T", listed)
		}
		for _, candidate := range []string{
			"Etc/UTC", "UTC", "GMT", "Europe/London", "America/New_York",
			"Pacific Standard Time",
		} {
			if have[candidate] && candidate != original {
				return candidate, nil
			}
		}
		return "", fmt.Errorf(
			"none of this case's candidate zones is one this machine has and is not "+
				"already in; it is in %s and its list has %d names", original, len(have))
	}

	return []liveCase{{
		Conformance: states.Conformance{
			Name:  "timezone.system",
			Args:  args,
			Probe: probe,
			Setup: func() error {
				if original == "" {
					now, err := probe()
					if err != nil {
						return fmt.Errorf("this machine's time zone could not be read: %w", err)
					}
					original = now
				}
				if runtime.GOOS == "darwin" && originalLink == "" {
					target, err := os.Readlink(localtimePath)
					if err != nil {
						return fmt.Errorf("%s is not a link on this Mac, so there is no "+
							"target to put back: %w", localtimePath, err)
					}
					originalLink = target
				}
				// Back to where it started, so the second Setup stages the
				// same change the first did.
				if err := restore(); err != nil {
					return err
				}
				if _, set := args.Get("name"); !set {
					zone, err := pick()
					if err != nil {
						return err
					}
					args.Set("name", zone)
				}
				return nil
			},
			Cleanup: func() { _ = restore() },
		},
	}}
}

// ---- reboot ----
//
// The file comment says why this one has a gate of its own and why the
// workflow, not the harness, is what asserts nothing was left pending.
func rebootCases() []liveCase {
	r := New()
	root := liveRoot()

	// Only whether one is pending. The module's own comment carries the
	// scheduled time, and a probe holding a clock reading would differ
	// between the two calls the harness makes for reasons that are not
	// the module's fault -- the same shape as the `at.atq` probe that
	// printed a Go pointer and accused test mode of changing the system.
	probe := func() (string, error) {
		out, err := r.Exec.Call(root, "reboot.scheduled", value.NewMap(0))
		if err != nil {
			return "", err
		}
		m, ok := out.(*value.Map)
		if !ok {
			return "", fmt.Errorf("reboot.scheduled returned %T", out)
		}
		pending, _ := m.GetString("scheduled")
		if pending == true {
			return "a shutdown is pending", nil
		}
		return "no shutdown is pending", nil
	}
	cancel := func() error {
		if _, err := r.Exec.Call(root, "reboot.cancel", value.NewMap(0)); err != nil {
			return fmt.Errorf("a pending shutdown could not be cancelled: %w", err)
		}
		return nil
	}

	return []liveCase{{
		Conformance: states.Conformance{
			Name: "reboot.scheduled",
			// `only_if_required: false`, because the default asks the
			// machine whether a reboot is needed and a runner that has
			// not been patched says no -- which is a legitimate True and
			// therefore a case that tests nothing. The flag a package
			// manager would drop could be faked, and that would be a case
			// about a file this test wrote rather than about the state.
			Args: value.MapOf("delay", int64(120), "only_if_required", false,
				"message", "halite conformance case -- cancelled immediately"),
			Probe: probe,
			// Cancelling before each phase rather than refusing to run on a
			// machine that has one pending, which is what
			// TestLiveRebootSchedulesAndCancelsWithoutRebooting does. The
			// difference is deliberate: that test can skip, and a phase of
			// this harness cannot -- it needs the absence in order to stage
			// a change. The variable that permits scheduling a reboot here
			// is the same one that permits cancelling somebody's.
			Setup:   cancel,
			Cleanup: func() { _ = cancel() },
		},
		platforms:       []string{"linux"},
		needs:           []string{"shutdown"},
		needsRebootGate: true,
	}}
}

// conformanceIdentityCases is the three families above, in one list.
func conformanceIdentityCases() []liveCase {
	var cases []liveCase
	cases = append(cases, hostnameCases()...)
	cases = append(cases, timezoneCases()...)
	cases = append(cases, rebootCases()...)
	return cases
}
