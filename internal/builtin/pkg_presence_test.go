package builtin

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// Issue 321, section 5: pkg.installed reports Changed without verifying
// the package is present or at the pin. The pin half was M-33; this is the
// other half. A package manager that exits 0 and installs nothing (a
// held, excluded or filtered package, a repository that lost it) left the
// state "changed" with the package still absent, and the next run changed
// it again, for ever.

func rhGrains() *value.Map { return value.MapOf("os", "Rocky", "os_family", "RedHat") }

func dnfCtx(r exec.CommandRunner) *exec.Context {
	c := newCtx(false)
	c.Grains = rhGrains()
	c.Runner = r
	c.Lookup = func(name string) string {
		if name == "dnf" || name == "rpm" {
			return "/usr/bin/" + name
		}
		return ""
	}
	return c
}

// fakePkgDB's dnf install is recorded and not acted on, which here is the
// manager that exited 0 and installed nothing.
func TestAnInstallThatLeavesThePackageAbsentIsNotChanged(t *testing.T) {
	db := &fakePkgDB{repo: map[string]string{}, installed: map[string]string{}}
	res, err := New().States.Call(dnfCtx(db), "pkg.installed", value.MapOf("name", "tree"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result {
		t.Fatalf("a package still absent after its install was reported %+v", res)
	}
	if !strings.Contains(res.Comment, "tree") || !strings.Contains(res.Comment, "not installed") {
		t.Errorf("the failure does not name the package and say it is absent: %q", res.Comment)
	}
}

// Only the absent one fails the state, and the others' changes are still
// reported: a partial result is information.
func TestOnlyThePackageThatIsAbsentIsNamed(t *testing.T) {
	db := &fakePkgDB{repo: map[string]string{}, installed: map[string]string{"curl": "8.0"}}
	res, err := New().States.Call(dnfCtx(db), "pkg.installed",
		value.MapOf("pkgs", []any{"curl", "tree"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || *res.Result || !strings.Contains(res.Comment, "tree") || strings.Contains(res.Comment, "curl") {
		t.Fatalf("%+v", res)
	}
}

// An install that does what it says is still a change.
func TestAnInstallThatLandsIsStillChanged(t *testing.T) {
	db := &fakePkgDB{repo: map[string]string{"tree": "2.3.2"}, installed: map[string]string{}}
	res, err := New().States.Call(pinCtx(db, "pkg", freebsdGrains()), "pkg.installed", value.MapOf("name", "tree"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result || !res.HasChanges() {
		t.Fatalf("%+v", res)
	}
}

// rereadFails lists normally until the install, then cannot.
type rereadFails struct {
	*fakePkgDB
	listed int
}

func (r *rereadFails) Run(ctx context.Context, cmd exec.Command) (exec.Result, error) {
	if len(cmd.Argv) >= 2 && cmd.Argv[0] == "rpm" && cmd.Argv[1] == "-qa" {
		r.listed++
		if r.listed > 1 {
			return exec.Result{Code: 1, Stderr: "rpmdb is locked"}, fmt.Errorf("rpm -qa: rpmdb is locked")
		}
	}
	return r.fakePkgDB.Run(ctx, cmd)
}

// If the list cannot be read back, nothing can be said about presence;
// the state does not claim a failure it did not see, and does not claim a
// verification it did not make.
func TestAnUnreadableListAfterTheInstallIsSaidNotHidden(t *testing.T) {
	db := &rereadFails{fakePkgDB: &fakePkgDB{repo: map[string]string{}, installed: map[string]string{}}}
	res, err := New().States.Call(dnfCtx(db), "pkg.installed", value.MapOf("name", "tree"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result || !res.HasChanges() {
		t.Fatalf("an unverifiable install was reported %+v", res)
	}
	if !strings.Contains(res.Comment, "could not be read back") {
		t.Errorf("the comment does not say presence was not confirmed: %q", res.Comment)
	}
}
