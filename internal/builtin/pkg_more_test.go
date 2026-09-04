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

// A provider that cannot answer an optional capability says so and names
// itself, rather than returning nothing, which a tree would read as "there
// are none". This pins which provider answers what, so a capability added
// or dropped shows up here.
func TestPkgOptionalCapabilities(t *testing.T) {
	holds := map[string]bool{"pkgng": true, "aptpkg": true, "dnfpkg": true, "apkpkg": false, "chocolatey": true}
	repos := map[string]bool{"pkgng": true, "aptpkg": true, "dnfpkg": true, "apkpkg": false, "chocolatey": true}
	upgrades := map[string]bool{"pkgng": true, "aptpkg": true, "dnfpkg": true, "apkpkg": true, "chocolatey": true}
	// Chocolatey has no command that maps a file to the package that
	// installed it, so it implements neither half of pkgOwner and a
	// caller gets the refusal that names it.
	owners := map[string]bool{"pkgng": true, "aptpkg": true, "dnfpkg": true, "apkpkg": true, "chocolatey": false}

	for _, p := range []pkgProvider{
		pkgngProvider{}, aptProvider{}, dnfProvider{binary: "dnf"}, apkProvider{}, chocoProvider{},
	} {
		if _, ok := p.(pkgHolder); ok != holds[p.Name()] {
			t.Errorf("%s pkgHolder = %v, want %v", p.Name(), ok, holds[p.Name()])
		}
		if _, ok := p.(pkgRepos); ok != repos[p.Name()] {
			t.Errorf("%s pkgRepos = %v, want %v", p.Name(), ok, repos[p.Name()])
		}
		if _, ok := p.(pkgUpgrader); ok != upgrades[p.Name()] {
			t.Errorf("%s pkgUpgrader = %v, want %v", p.Name(), ok, upgrades[p.Name()])
		}
		if _, ok := p.(pkgOwner); ok != owners[p.Name()] {
			t.Errorf("%s pkgOwner = %v, want %v", p.Name(), ok, owners[p.Name()])
		}
	}

	// And the error a caller gets when a provider cannot answer names what
	// could not be done rather than only that something went wrong.
	//
	// The node is told which package manager it has, rather than left to
	// PATH: this asserted whatever the machine running the tests happened
	// to have installed, so on a host with no package manager at all it
	// failed with "no package manager was found on this node", which is a
	// different error about a different thing.
	c := newCtx(false)
	c.Runner = &exec.RecordingRunner{}
	c.Lookup = func(name string) string {
		if name == "apk" {
			return "/sbin/apk"
		}
		return ""
	}
	_, err := pickHolder(c)
	if err == nil {
		t.Fatal("apk has no hold, and pickHolder did not say so")
	}
	if !strings.Contains(err.Error(), "hold") || !strings.Contains(err.Error(), "apkpkg") {
		t.Errorf("the error should name the capability and the provider: %v", err)
	}

	// And with no package manager at all, the error is about that
	// instead, and offers the providers this build ships.
	c.Lookup = func(string) string { return "" }
	if _, err := pickHolder(c); err == nil || !strings.Contains(err.Error(), "no package manager") {
		t.Errorf("with no package manager the error should say so: %v", err)
	}
}

// aptCtx answers apt's queries from scripted output on a node whose grains
// say Ubuntu, so the parsing is exercised without a package database.
func aptCtx(t *testing.T, responses map[string]exec.Result) *exec.Context {
	t.Helper()
	c := newCtx(false)
	c.Grains = value.MapOf("os", "Ubuntu", "os_family", "Debian")
	c.Runner = &exec.RecordingRunner{Responses: responses}
	return c
}

func TestAptListUpgradesParsesTheSimulatedRun(t *testing.T) {
	c := aptCtx(t, map[string]exec.Result{
		"apt-get --simulate -q -o Debug::NoLocking=true dist-upgrade": {Stdout: `
Inst bsdutils [1:2.39.3-9ubuntu6.5] (1:2.39.3-9ubuntu6.6 Ubuntu:24.04/noble-updates [amd64]) []
Inst coreutils [9.4-3ubuntu6.2] (9.4-3ubuntu6.3 Ubuntu:24.04/noble-updates, Ubuntu:24.04/noble-security [amd64])
Conf bsdutils (1:2.39.3-9ubuntu6.6 Ubuntu:24.04/noble-updates [amd64])
`},
	})
	got, err := aptProvider{}.ListUpgrades(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Get("coreutils"); v != "9.4-3ubuntu6.3" {
		t.Errorf("coreutils = %#v", v)
	}
	if v, _ := got.Get("bsdutils"); v != "1:2.39.3-9ubuntu6.6" {
		t.Errorf("bsdutils = %#v", v)
	}
	// `Conf` lines are not a second upgrade of the same package.
	if got.Len() != 2 {
		t.Errorf("upgrades = %v", got.StringKeys())
	}
}

func TestAptOwnerOf(t *testing.T) {
	c := aptCtx(t, map[string]exec.Result{
		"dpkg-query -S /usr/bin/ls": {Stdout: "coreutils: /usr/bin/ls\n"},
	})
	got, err := aptProvider{}.OwnerOf(c, "/usr/bin/ls")
	if err != nil {
		t.Fatal(err)
	}
	if got != "coreutils" {
		t.Errorf("owner = %q", got)
	}

	// A path no package owns is the empty string, not an error.
	c.Runner.(*exec.RecordingRunner).Default = exec.Result{
		Code: 1, Stderr: "dpkg-query: no path found matching pattern /etc/motd\n",
	}
	got, err = aptProvider{}.OwnerOf(c, "/etc/motd")
	if err != nil || got != "" {
		t.Errorf("unowned path: got %q, %v", got, err)
	}
}

func TestAptListReposGroupsComponentsByURIAndSuite(t *testing.T) {
	// Two indextargets blocks for the same repository, one per component,
	// plus a translation target that is not a repository.
	c := aptCtx(t, map[string]exec.Result{
		"apt-get indextargets": {Stdout: `MetaKey: main/binary-amd64/Packages
Created-By: Packages
Repo-URI: http://archive.ubuntu.com/ubuntu/
Codename: noble
Component: main
Architecture: amd64
Trusted: yes

MetaKey: universe/binary-amd64/Packages
Created-By: Packages
Repo-URI: http://archive.ubuntu.com/ubuntu/
Codename: noble
Component: universe
Architecture: amd64
Trusted: yes

MetaKey: main/i18n/Translation-en
Created-By: Translations
Repo-URI: http://archive.ubuntu.com/ubuntu/
Codename: noble
Component: main
`},
	})
	got, err := aptProvider{}.ListRepos(c)
	if err != nil {
		t.Fatal(err)
	}
	if got.Len() != 1 {
		t.Fatalf("repos = %v; the translation target is not a repo and the two components are one repo", got.StringKeys())
	}
	repo, _ := got.Get("deb http://archive.ubuntu.com/ubuntu/ noble")
	m := repo.(*value.Map)
	comps, _ := m.Get("comps")
	list := comps.([]any)
	if len(list) != 2 || list[0] != "main" || list[1] != "universe" {
		t.Errorf("comps = %#v", list)
	}
	if v, _ := m.Get("trusted"); v != true {
		t.Errorf("trusted = %#v", v)
	}
}

func TestDnfListHoldsStripsTheNEVRA(t *testing.T) {
	c := pkgCtx(t, map[string]exec.Result{
		"dnf versionlock list --quiet": {Stdout: `Last metadata expiration check: 0:12:01 ago.
kernel-0:6.6.8-100.fc39.x86_64
nginx-1:1.24.0-1.fc39.x86_64
`},
	})
	got, err := dnfProvider{binary: "dnf"}.ListHolds(c)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"kernel": true, "nginx": true}
	if len(got) != 2 {
		t.Fatalf("holds = %v", got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected hold %q", n)
		}
	}
}

func TestApkOwnerOfStripsTheVersion(t *testing.T) {
	c := pkgCtx(t, map[string]exec.Result{
		"apk info -W etc/ssh/sshd_config": {Stdout: "etc/ssh/sshd_config is owned by openssh-server-9.7_p1-r4\n"},
	})
	got, err := apkProvider{}.OwnerOf(c, "/etc/ssh/sshd_config")
	if err != nil {
		t.Fatal(err)
	}
	if got != "openssh-server" {
		t.Errorf("owner = %q", got)
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
