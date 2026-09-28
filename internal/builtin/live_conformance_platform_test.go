package builtin

import (
	"fmt"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// `win_task`, `win_service` and `mac_defaults`: the five of the nine
// Windows-and-macOS states that change the machine.
//
// The other four, `win_dacl`, act on a path the test owns and are in the
// in-process list — see conformance_windows_cases_test.go for the argument.
// These are here because a scheduled task, a service and a preference domain
// are the machine's, not a path's.
//
// # What runs them
//
// `fleet.yml`'s `macos` leg for `mac_defaults`, and a `windows` leg added for
// the other three. Both set `HALITE_CONFORMANCE_LIVE=1` alongside
// `HALITE_SYSTEM_LIVE=1`, because these apply a state four times rather than
// restoring what they found. The runners are fresh virtual machines discarded
// minutes later, which is the machine this asks for.
//
// # Windows and root
//
// `liveGate` insists on uid 0, which does not exist on Windows. The check is
// `os.Geteuid() != 0`, and Go returns -1 there — so the gate would fail a
// Windows runner that is a full administrator. `liveGate` therefore asks the
// question the platform can answer: on Windows, whether a privileged
// operation succeeds, rather than what a uid is.
//
// # The filename is a build constraint
//
// This was `live_conformance_windows_test.go`, and Go reads a trailing
// `_windows` before `_test.go` as "compile this on Windows only" -- so the
// five cases vanished from the list on every other platform and the build
// failed on the *name*, not on anything in the file. That is #154's defect in
// a new disguise: a case list that is a function of where it is built cannot
// be used to count what is covered. The compiler caught this one; the first
// one took a CI leg.
//
// DIVERGENCE 5.157.

// The names this suite makes. A task and a service that could be mistaken for
// something of the machine's would be the wrong kind of mistake to make on a
// host somebody else owns, so both carry the suite's prefix.
const (
	liveConformanceTask    = liveConformancePrefix + "-task"
	liveConformanceService = liveConformancePrefix + "svc"
)

// windowsCases are the three that change a Windows machine.
func windowsLiveCases() []liveCase {
	root := liveRoot()
	r := New()

	windows := func(lc liveCase) liveCase {
		lc.platforms = []string{"windows"}
		return lc
	}

	// ---- win_task ----
	//
	// The probe reads `schtasks /query`, which is what an operator would
	// type, rather than the module's own answer.
	taskProbe := func() (string, error) {
		res, err := root.Run(hexec.Command{
			Argv: []string{"schtasks", "/query", "/tn", liveConformanceTask,
				"/fo", "list"},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if res.Code != 0 {
			return "no task", nil
		}
		var kept []string
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(line)
			// Only the fields that say what the task *is*. `Next Run Time`
			// moves on its own and would make the probe differ for a reason
			// that is not a change.
			if strings.HasPrefix(line, "TaskName:") ||
				strings.HasPrefix(line, "Status:") {
				kept = append(kept, line)
			}
		}
		if len(kept) == 0 {
			return "task present, no fields read", nil
		}
		return strings.Join(kept, " | "), nil
	}
	dropTask := func() {
		_, _ = root.Run(hexec.Command{
			Argv:           []string{"schtasks", "/delete", "/tn", liveConformanceTask, "/f"},
			IgnoreExitCode: true,
		})
	}

	taskArgs := func() *value.Map {
		return value.MapOf("name", liveConformanceTask,
			"command", `C:\Windows\System32\cmd.exe`,
			"arguments", "/c exit 0",
			// A daily trigger at a fixed time rather than `boot` or `logon`:
			// neither of those will fire during the job, and a task that
			// might run while the harness is reading it would make the probe
			// a race.
			"trigger", "daily at 04:17",
			"description", "halite conformance probe",
		)
	}

	cases := []liveCase{
		windows(liveCase{Conformance: states.Conformance{
			Name:    "win_task.present",
			Args:    taskArgs(),
			Probe:   taskProbe,
			Setup:   func() error { dropTask(); return nil },
			Cleanup: dropTask,
		}}),
		windows(liveCase{Conformance: states.Conformance{
			Name:  "win_task.absent",
			Args:  value.MapOf("name", liveConformanceTask),
			Probe: taskProbe,
			Setup: func() error {
				dropTask()
				return applyForSetup(r, root, "win_task.present", taskArgs())
			},
			Cleanup: dropTask,
		}}),
	}

	// ---- win_service.start_type ----
	//
	// A service of this suite's own, made with `sc create` and deleted
	// after. Never an existing service: changing when something the machine
	// came with starts is exactly the change that outlives a test, and on a
	// runner that is discarded it would still be the wrong habit to build
	// into a case somebody may run on a real host.
	//
	// The binary is cmd.exe, which exists everywhere. The service will never
	// start correctly and does not need to: `start_type` is about when
	// Windows would start it, and is set and read without it running.
	serviceProbe := func() (string, error) {
		res, err := root.Run(hexec.Command{
			Argv:           []string{"sc", "qc", liveConformanceService},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if res.Code != 0 {
			return "no service", nil
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			if strings.Contains(line, "START_TYPE") {
				return strings.Join(strings.Fields(line), " "), nil
			}
		}
		return "service present, no START_TYPE line", nil
	}
	dropService := func() {
		_, _ = root.Run(hexec.Command{
			Argv:           []string{"sc", "delete", liveConformanceService},
			IgnoreExitCode: true,
		})
	}
	makeService := func(startType string) error {
		dropService()
		res, err := root.Run(hexec.Command{
			Argv: []string{"sc", "create", liveConformanceService,
				"binPath=", `C:\Windows\System32\cmd.exe /c exit 0`,
				"start=", startType,
				"DisplayName=", "halite conformance probe"},
			IgnoreExitCode: true,
		})
		if err != nil {
			return err
		}
		if res.Code != 0 {
			return fmt.Errorf("sc create exited %d: %s", res.Code,
				strings.TrimSpace(res.Stdout+res.Stderr))
		}
		return nil
	}

	cases = append(cases, windows(liveCase{Conformance: states.Conformance{
		Name: "win_service.start_type",
		// Created `disabled` and declared `manual`, so there is a change to
		// make whatever `sc` and this module each call the middle setting --
		// `sc` says `demand` where the module says `manual`, and starting
		// from `disabled` avoids resting the case on that translation.
		Args:    value.MapOf("name", liveConformanceService, "start_type", "manual"),
		Probe:   serviceProbe,
		Setup:   func() error { return makeService("disabled") },
		Cleanup: dropService,
	}}))

	return cases
}

// macCases is `mac_defaults`, in a preference domain of this suite's own.
//
// `live_mac_defaults_test.go` established the shape: a domain nothing else on
// the machine has, in the invoking user's own store, removed afterwards. No
// root is needed for a user domain, but the leg runs as root anyway and the
// gate asks for it, so the domain is root's.
func macLiveCases() []liveCase {
	root := liveRoot()
	r := New()

	domain := "com.halite.conformance"
	const key = "ConformanceProbe"

	darwin := func(lc liveCase) liveCase {
		lc.platforms = []string{"darwin"}
		lc.needs = []string{"defaults"}
		return lc
	}

	// Read with `defaults read`, which is the tool an operator uses, not the
	// module's own answer.
	probe := func() (string, error) {
		res, err := root.Run(hexec.Command{
			Argv:           []string{"defaults", "read", domain, key},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if res.Code != 0 {
			return "absent", nil
		}
		return strings.TrimSpace(res.Stdout), nil
	}
	dropDomain := func() {
		_, _ = root.Run(hexec.Command{
			Argv:           []string{"defaults", "delete", domain},
			IgnoreExitCode: true,
		})
	}

	writeArgs := value.MapOf("domain", domain, "key", key,
		"value", "conformance", "vtype", "string")

	return []liveCase{
		darwin(liveCase{Conformance: states.Conformance{
			Name:    "mac_defaults.write",
			Args:    writeArgs,
			Probe:   probe,
			Setup:   func() error { dropDomain(); return nil },
			Cleanup: dropDomain,
		}}),
		darwin(liveCase{Conformance: states.Conformance{
			Name:  "mac_defaults.absent",
			Args:  value.MapOf("domain", domain, "key", key),
			Probe: probe,
			Setup: func() error {
				dropDomain()
				return applyForSetup(r, root, "mac_defaults.write", writeArgs)
			},
			Cleanup: dropDomain,
		}}),
	}
}
