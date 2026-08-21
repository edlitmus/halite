package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// pkgCtx answers pkg's queries from scripted output, so the parsing can
// be exercised without a package database.
func pkgCtx(t *testing.T, responses map[string]exec.Result) *exec.Context {
	t.Helper()
	c := newCtx(false)
	c.Grains = value.MapOf("os", "FreeBSD", "os_family", "FreeBSD")
	c.Runner = &exec.RecordingRunner{Responses: responses}
	return c
}

func TestPkgListUpgradesParsesTheDryRun(t *testing.T) {
	c := pkgCtx(t, map[string]exec.Result{
		"pkg upgrade --dry-run --quiet": {Stdout: `
curl: 8.5.0 -> 8.6.0
git: 2.43.0_1 -> 2.44.0
Number of packages to be upgraded: 2
`},
	})
	got, err := pkgngProvider{}.ListUpgrades(c, true)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("curl"); v != "8.6.0" {
		t.Errorf("curl = %#v", v)
	}
	if v, _ := got.Get("git"); v != "2.44.0" {
		t.Errorf("git = %#v", v)
	}
	// The summary line is not a package.
	if got.Len() != 2 {
		t.Errorf("upgrades = %v", got.StringKeys())
	}
}

func TestPkgListHoldsStripsTheVersion(t *testing.T) {
	c := pkgCtx(t, map[string]exec.Result{
		"pkg lock --list --quiet": {Stdout: "nginx-1.24.0_2\npython311-3.11.9\n"},
	})
	got, err := pkgngProvider{}.ListHolds(c)
	if err != nil {
		t.Fatal(err)
	}
	// A hold is on the name, so the version has to come off or an unhold
	// of "nginx" would never match.
	want := map[string]bool{"nginx": true, "python311": true}
	if len(got) != 2 {
		t.Fatalf("holds = %v", got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected hold %q", n)
		}
	}
}

func TestPkgOwnerOfAnUnownedPathIsEmptyNotAnError(t *testing.T) {
	c := pkgCtx(t, map[string]exec.Result{})
	c.Runner.(*exec.RecordingRunner).Default = exec.Result{Code: 1}
	got, err := pkgngProvider{}.OwnerOf(c, "/etc/rc.conf")
	if err != nil {
		t.Fatalf("a path no package owns should not be an error: %v", err)
	}
	if got != "" {
		t.Errorf("owner = %q", got)
	}
}

func TestPkgListReposParsesTheBlocks(t *testing.T) {
	c := pkgCtx(t, map[string]exec.Result{
		"pkg -vv": {Stdout: `
Repositories:
  FreeBSD-ports: {
    url             : "pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/latest",
    enabled         : yes,
    priority        : 0
  }
  local: {
    url             : "file:///usr/local/repo",
    enabled         : no
  }
`},
	})
	got, err := pkgngProvider{}.ListRepos(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() != 2 {
		t.Fatalf("repos = %v", got.StringKeys())
	}
	first, _ := got.Get("FreeBSD-ports")
	m := first.(*value.Map)
	if url, _ := m.Get("url"); url != "pkg+https://pkg.FreeBSD.org/FreeBSD:15:amd64/latest" {
		t.Errorf("url = %#v; the quotes belong to the format, not the value", url)
	}
	if enabled, _ := m.Get("enabled"); enabled != "yes" {
		t.Errorf("enabled = %#v", enabled)
	}
}

// A provider that cannot answer says so and names itself, rather than
// returning nothing, which a tree would read as "there are none".
//
// The optional capabilities are implemented for pkgng and not yet for the
// others, so the assertion is on the type rather than on a node that has
// apt: this host has neither apt nor dnf to pick.
func TestPkgOptionalCapabilitiesAreOptional(t *testing.T) {
	if _, ok := any(pkgngProvider{}).(pkgHolder); !ok {
		t.Error("pkgng should be able to hold; pkg lock is its hold")
	}
	for _, p := range []pkgProvider{aptProvider{}, dnfProvider{binary: "dnf"}, apkProvider{}} {
		if _, ok := p.(pkgHolder); ok {
			t.Errorf("%s claims to hold; if that is now true, remove this case", p.Name())
		}
	}

	// And the error a caller gets names the provider rather than saying
	// only that something went wrong.
	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{}
	if _, err := pickHolder(c); err != nil && !strings.Contains(err.Error(), "hold") {
		t.Errorf("the error should say what could not be done: %v", err)
	}
}

func TestPkgDeltaReportsWhatActuallyHappened(t *testing.T) {
	before := value.MapOf("curl", "8.5.0", "removed-me", "1.0")
	// The listing is the only command this runs, so the default response
	// is the listing: matching on the command's rendered form would pin
	// the test to how Command.String quotes a tab.
	c := pkgCtx(t, nil)
	c.Runner.(*exec.RecordingRunner).Default = exec.Result{
		Stdout: "curl\t8.6.0\nbrand-new\t2.0\n",
	}
	got, err := pkgDelta(c, pkgngProvider{}, before)
	if err != nil {
		t.Fatal(err)
	}
	// An upgrade, a removal, and an addition, each with the pair a
	// dashboard parses.
	for name, want := range map[string][2]any{
		"curl":       {"8.5.0", "8.6.0"},
		"removed-me": {"1.0", nil},
		"brand-new":  {nil, "2.0"},
	} {
		v, ok := got.Get(name)
		if !ok {
			t.Errorf("%s is missing from the changes: %v", name, got.StringKeys())
			continue
		}
		m := v.(*value.Map)
		old, _ := m.Get("old")
		nw, _ := m.Get("new")
		if old != want[0] || nw != want[1] {
			t.Errorf("%s = %v -> %v, want %v -> %v", name, old, nw, want[0], want[1])
		}
	}
	if got.Len() != 3 {
		t.Errorf("changes = %v", got.StringKeys())
	}
}
