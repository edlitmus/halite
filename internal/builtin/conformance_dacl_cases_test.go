package builtin

import (
	"os"
	"path/filepath"
	"strings"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// The four `win_dacl` states, in the in-process list rather than the live one.
//
// # Why these are not live cases
//
// `win_dacl` changes the access control list on a *path*, and a path in
// `t.TempDir()` is a path this test owns — the same reason the sixteen `file`
// states are in-process. Nothing about the machine changes.
// `win_dacl_windows_test.go` already drives the real Windows security API
// against a temporary file in the ordinary unit suite, which is the precedent.
//
// So these run in the `test (windows-2022)` and `race (windows-2022)` legs of
// `ci.yml`, on every pull request, rather than needing a leg of their own.
// `win_task` and `win_service` do change the machine and are live cases; see
// live_conformance_platform_test.go -- whose name has no platform suffix on
// purpose, because Go reads one as a build constraint.
//
// # Why the probes shell out
//
// `internal/winsec` is `_windows.go` only, and this file must compile on every
// platform — the unit suite builds the case list everywhere to count what is
// covered, and a case that vanished off Windows would read as uncovered
// there. That was the defect CI found on windows-2022 in #154, so the rule is
// now: construct unconditionally, gate by declaration.
//
// Shelling out to `icacls` is also the better probe. It is what an operator
// would type, and reading the tool rather than the module is what makes the
// probe independent of the thing it is checking.
//
// DIVERGENCE 5.157.

// winDACLProbe reports what icacls says about a path, with the inherited
// entries left out: an inherited entry is the parent's, and a state that
// manages this path did not put it there.
func winDACLProbe(path *string) func() (string, error) {
	root := liveRoot()
	return func() (string, error) {
		if *path == "" {
			return "no path", nil
		}
		res, err := root.Run(hexec.Command{
			Argv:           []string{"icacls", *path},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if res.Code != 0 {
			return "icacls could not read it: " + strings.TrimSpace(res.Stderr), nil
		}
		var kept []string
		for _, line := range strings.Split(res.Stdout, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "Successfully processed") {
				continue
			}
			// The first line carries the path; drop it so the probe does
			// not differ merely because the temporary directory does.
			line = strings.TrimPrefix(line, *path)
			if s := strings.TrimSpace(line); s != "" {
				kept = append(kept, s)
			}
		}
		sortStringsForProbe(kept)
		return strings.Join(kept, " | "), nil
	}
}

// winOwnerProbe reports a path's owner, asked of Windows rather than of the
// module. `icacls` does not print the owner, so this is PowerShell's Get-Acl.
func winOwnerProbe(path *string) func() (string, error) {
	root := liveRoot()
	return func() (string, error) {
		if *path == "" {
			return "no path", nil
		}
		res, err := root.Run(hexec.Command{
			Argv: []string{"powershell", "-NoProfile", "-NonInteractive", "-Command",
				"(Get-Acl -LiteralPath '" + *path + "').Owner"},
			IgnoreExitCode: true,
		})
		if err != nil {
			return "", err
		}
		if res.Code != 0 {
			return "owner unreadable: " + strings.TrimSpace(res.Stderr), nil
		}
		return strings.TrimSpace(res.Stdout), nil
	}
}

// winDACLCases builds the four. They are appended to the in-process list, so
// each needs its own temporary path and must clean it up itself: the harness
// gives a case Setup and Cleanup and nothing else.
func winDACLCases() []conformanceCase {
	windows := func(lc conformanceCase) conformanceCase {
		lc.platforms = []string{"windows"}
		return lc
	}

	// A directory per case, made in Setup and removed in Cleanup. Not
	// t.TempDir, because these builders take no *testing.T -- a builder with
	// one invites a cleanup that runs at the end of the whole test rather
	// than at the end of the case.
	newDir := func(dir *string) func() error {
		return func() error {
			if *dir != "" {
				_ = os.RemoveAll(*dir)
			}
			d, err := os.MkdirTemp("", liveConformancePrefix+"-dacl-")
			if err != nil {
				return err
			}
			*dir = d
			return nil
		}
	}
	dropDir := func(dir *string) func() {
		return func() {
			if *dir != "" {
				_ = os.RemoveAll(*dir)
				*dir = ""
			}
		}
	}

	var cases []conformanceCase

	// present: a file with no entry for the trustee, which the state grants.
	// `Everyone` is the trustee the existing win_dacl tests use, and on a
	// file inside a temporary directory it grants nothing anybody reaches.
	grantDir, grantPath := "", ""
	grantArgs := value.MapOf("trustee", "Everyone", "permission", "read",
		"applies_to", "this_folder_only")
	cases = append(cases, windows(conformanceCase{Conformance: states.Conformance{
		Name: "win_dacl.present",
		Args: grantArgs,
		Setup: func() error {
			if err := newDir(&grantDir)(); err != nil {
				return err
			}
			grantPath = filepath.Join(grantDir, "app.conf")
			grantArgs.Set("name", grantPath)
			return os.WriteFile(grantPath, []byte("x"), 0o600)
		},
		Probe:   winDACLProbe(&grantPath),
		Cleanup: dropDir(&grantDir),
	}}))

	// absent: the entry is there and the state removes it, so Setup grants
	// it through the state -- a setup that used a second implementation of
	// "grant" could pass while the state's own grant was broken.
	revokeDir, revokePath := "", ""
	revokeArgs := value.MapOf("trustee", "Everyone")
	cases = append(cases, windows(conformanceCase{Conformance: states.Conformance{
		Name: "win_dacl.absent",
		Args: revokeArgs,
		Setup: func() error {
			if err := newDir(&revokeDir)(); err != nil {
				return err
			}
			revokePath = filepath.Join(revokeDir, "app.conf")
			revokeArgs.Set("name", revokePath)
			if err := os.WriteFile(revokePath, []byte("x"), 0o600); err != nil {
				return err
			}
			r := New()
			_, err := r.States.Call(liveRoot(), "win_dacl.present",
				value.MapOf("name", revokePath, "trustee", "Everyone",
					"permission", "read", "applies_to", "this_folder_only"))
			return err
		},
		Probe:   winDACLProbe(&revokePath),
		Cleanup: dropDir(&revokeDir),
	}}))

	// inherit: a freshly made directory inherits from its parent, so
	// `enabled: false` is the change. `clear` is left alone: dropping the
	// inherited entries as well would leave a directory nobody can read,
	// and the state's own tests cover that half.
	inheritDir := ""
	inheritPath := ""
	inheritArgs := value.MapOf("enabled", false)
	cases = append(cases, windows(conformanceCase{Conformance: states.Conformance{
		Name: "win_dacl.inherit",
		Args: inheritArgs,
		Setup: func() error {
			if err := newDir(&inheritDir)(); err != nil {
				return err
			}
			inheritPath = filepath.Join(inheritDir, "sub")
			inheritArgs.Set("name", inheritPath)
			return os.MkdirAll(inheritPath, 0o755)
		},
		Probe:   winDACLProbe(&inheritPath),
		Cleanup: dropDir(&inheritDir),
	}}))

	// owner: set to SYSTEM.
	//
	// `Administrators` was the first choice and Windows refused it —
	// "This security ID may not be assigned as the owner of this object" —
	// on a runner that is elevated and where `winsec.SetOwner` had enabled
	// SeRestorePrivilege successfully. It says so itself: the error carries
	// no hint about a missing privilege, and the hint is printed exactly
	// when the privilege could not be enabled. So the module did its part
	// and the trustee was simply not assignable there.
	//
	// SYSTEM is, and it is not the account creating the file, so there is
	// still a change to make. Recorded rather than silently swapped,
	// because "pick a different trustee" is the kind of fix that looks
	// arbitrary a year later.
	ownerDir, ownerPath := "", ""
	ownerArgs := value.MapOf("owner", "SYSTEM")
	cases = append(cases, windows(conformanceCase{Conformance: states.Conformance{
		Name: "win_dacl.owner",
		Args: ownerArgs,
		Setup: func() error {
			if err := newDir(&ownerDir)(); err != nil {
				return err
			}
			ownerPath = filepath.Join(ownerDir, "owned.conf")
			ownerArgs.Set("name", ownerPath)
			return os.WriteFile(ownerPath, []byte("x"), 0o600)
		},
		Probe:   winOwnerProbe(&ownerPath),
		Cleanup: dropDir(&ownerDir),
	}}))

	return cases
}

// sortStringsForProbe keeps a probe's rendering stable. icacls prints the
// entries in the order the list holds them, and a state that reorders without
// changing the set is not a change an operator cares about -- but an unsorted
// probe would call it one.
func sortStringsForProbe(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
