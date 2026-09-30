package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The `pkg` module's **zypper** provider, and `zypperpkg`, driven against
// a real zypper.
//
// # What was assumed until now
//
// Nothing, because nothing existed: SUSE had no provider and `zypperpkg`
// was declared pending. The provider was written on the lab's openSUSE
// Leap 16.0 against output captured there (testdata/zypper), and these
// are what watched it change the machine. DIVERGENCE 5.176.
//
// # What it changes, and how it puts it back
//
// It installs `tree` (84 KiB, no dependencies the image lacks), locks and
// unlocks it, removes it; it upgrades `libX11-data` one release through
// `pkg.latest` and pins it back through `pkg.installed`; and it adds an
// unreachable repository and removes it. Every answer is checked against
// rpm, zypper, or /etc/zypp/repos.d directly -- never against another of
// this module's readers. Each test refuses to start from a state it would
// not know how to restore, and restores in a Cleanup.
//
// # Why it skips everywhere else
//
// `HALITE_SYSTEM_LIVE=1` and root, plus zypper and rpm. On a node whose
// os_family is not Suse and has no zypper, it skips saying so.

const liveZypperPackage = "tree"

func zypperLive(t *testing.T) *exec.Context {
	t.Helper()
	c := system(t)
	if runtime.GOOS != "linux" {
		t.Skipf("zypper is SUSE's, and this is %s", runtime.GOOS)
	}
	requireToolOfFamilies(t, "zypper and rpm", c.Which("zypper") != "" && c.Which("rpm") != "", "Suse")
	if fam := liveOSFamily(t); fam != "Suse" {
		t.Fatalf("this node has zypper and its os_family grain is %q, not Suse", fam)
	}
	p, err := pickPkgProvider(c)
	if err != nil {
		t.Fatalf("no package manager was recognised on this SUSE host: %v", err)
	}
	if p.Name() != "zypperpkg" {
		t.Fatalf("the pkg module picked the %s provider on a SUSE host; this file tests zypper", p.Name())
	}
	return c
}

// rpmVersion asks rpm directly, never the module.
func rpmVersion(t *testing.T, c *exec.Context, name string) (string, bool) {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv:           []string{"rpm", "-q", "--queryformat", "%{VERSION}-%{RELEASE}", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("rpm -q %s: %v", name, err)
	}
	if res.Code != 0 {
		return "", false
	}
	return strings.TrimSpace(res.Stdout), true
}

// zypperLockNames reads `zypper locks` directly, in its table form.
func zypperLockNames(t *testing.T, c *exec.Context) string {
	t.Helper()
	res, err := c.Run(exec.Command{Argv: []string{"zypper", "--non-interactive", "locks"}, IgnoreExitCode: true})
	if err != nil {
		t.Fatal(err)
	}
	return res.Stdout
}

func zypperDirect(c *exec.Context, args ...string) {
	_, _ = c.Run(exec.Command{
		Argv: append([]string{"zypper", "--non-interactive"}, args...), IgnoreExitCode: true,
	})
}

// **install, read back four ways, hold, and remove.**
func TestLiveZypperInstallsHoldsAndRemovesARealPackage(t *testing.T) {
	c := zypperLive(t)
	r := New()

	if _, installed := rpmVersion(t, c, liveZypperPackage); installed {
		t.Skipf("%s is already installed on this machine; this test will not remove it", liveZypperPackage)
	}
	if locks := zypperLockNames(t, c); strings.Contains(locks, "| "+liveZypperPackage+" ") {
		t.Skipf("%s is already locked on this machine; this test will not unlock it", liveZypperPackage)
	}
	t.Cleanup(func() {
		zypperDirect(c, "removelock", liveZypperPackage)
		zypperDirect(c, "remove", "--name", liveZypperPackage)
		if _, left := rpmVersion(t, c, liveZypperPackage); left {
			t.Errorf("%s is still installed after cleanup", liveZypperPackage)
		}
	})

	// What the repository offers, before, spelled the way pkg.latest
	// will compare it with what rpm reports afterwards.
	offered, err := r.Exec.Call(c, "pkg.latest_version", value.MapOf("name", liveZypperPackage))
	if err != nil {
		t.Fatalf("pkg.latest_version: %v", err)
	}
	if offered == "" {
		t.Fatalf("pkg.latest_version of %s, which is not installed, is empty", liveZypperPackage)
	}

	// Through the SPEC 15.3 name, which is the other half of this work.
	if _, err := r.Exec.Call(c, "zypperpkg.install", value.MapOf("name", liveZypperPackage)); err != nil {
		t.Fatalf("zypperpkg.install: %v", err)
	}
	got, installed := rpmVersion(t, c, liveZypperPackage)
	if !installed {
		t.Fatal("zypperpkg.install returned and rpm does not have the package")
	}
	if got != offered {
		t.Errorf("pkg.latest_version offered %q and rpm installed %q; pkg.latest would never converge", offered, got)
	}

	version, err := r.Exec.Call(c, "pkg.version", value.MapOf("name", liveZypperPackage))
	if err != nil || version != got {
		t.Errorf("pkg.version = %v, %v; rpm says %q", version, err, got)
	}
	if latest, err := r.Exec.Call(c, "pkg.latest_version", value.MapOf("name", liveZypperPackage)); err != nil || latest != "" {
		t.Errorf("pkg.latest_version of an up-to-date package = %v, %v; want empty", latest, err)
	}
	files, err := r.Exec.Call(c, "pkg.file_list", value.MapOf("name", liveZypperPackage))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(anyStrings(files), "/usr/bin/tree") {
		t.Errorf("pkg.file_list does not list /usr/bin/tree: %v", files)
	}
	if owner, err := r.Exec.Call(c, "pkg.owner", value.MapOf("path", "/usr/bin/tree")); err != nil || owner != liveZypperPackage {
		t.Errorf("pkg.owner /usr/bin/tree = %v, %v", owner, err)
	}

	// **The hold is real**: zypper lists the lock, and a removal fails.
	if _, err := r.Exec.Call(c, "pkg.hold", value.MapOf("name", liveZypperPackage)); err != nil {
		t.Fatalf("pkg.hold: %v", err)
	}
	if locks := zypperLockNames(t, c); !strings.Contains(locks, "| "+liveZypperPackage+" ") {
		t.Errorf("pkg.hold returned and `zypper locks` does not list %s:\n%s", liveZypperPackage, locks)
	}
	holds, err := r.Exec.Call(c, "pkg.list_holds", value.NewMap(0))
	if err != nil || !contains(anyStrings(holds), liveZypperPackage) {
		t.Errorf("pkg.list_holds = %v, %v", holds, err)
	}
	if _, err := r.Exec.Call(c, "pkg.remove", value.MapOf("name", liveZypperPackage)); err == nil {
		t.Error("pkg.remove of a held package succeeded")
	} else {
		t.Logf("pkg.remove of a held package: %v", err)
	}
	if _, still := rpmVersion(t, c, liveZypperPackage); !still {
		t.Fatal("the held package was removed")
	}
	if _, err := r.Exec.Call(c, "pkg.unhold", value.MapOf("name", liveZypperPackage)); err != nil {
		t.Fatalf("pkg.unhold: %v", err)
	}
	if locks := zypperLockNames(t, c); strings.Contains(locks, "| "+liveZypperPackage+" ") {
		t.Errorf("pkg.unhold returned and `zypper locks` still lists %s", liveZypperPackage)
	}

	if _, err := r.Exec.Call(c, "pkg.remove", value.MapOf("name", liveZypperPackage)); err != nil {
		t.Fatalf("pkg.remove: %v", err)
	}
	if _, left := rpmVersion(t, c, liveZypperPackage); left {
		t.Fatal("pkg.remove returned and rpm still has the package")
	}
}

// **list_repos against the files zypper reads, list_upgrades against
// rpm, and refresh_db.**
func TestLiveZypperReadsAgreeWithTheMachine(t *testing.T) {
	c := zypperLive(t)
	r := New()

	out, err := r.Exec.Call(c, "pkg.list_repos", value.NewMap(0))
	if err != nil {
		t.Fatalf("pkg.list_repos: %v", err)
	}
	repos := out.(*value.Map)
	files, _ := filepath.Glob("/etc/zypp/repos.d/*.repo")
	onDisk := map[string]bool{}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		alias, enabled := "", false
		for _, ln := range strings.Split(string(body), "\n") {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "[") && strings.HasSuffix(ln, "]") {
				alias = strings.Trim(ln, "[]")
			}
			if strings.ReplaceAll(ln, " ", "") == "enabled=1" {
				enabled = true
			}
		}
		onDisk[alias] = enabled
		got, ok := repos.Get(alias)
		if !ok {
			t.Errorf("%s configures %s, and pkg.list_repos does not list it", f, alias)
			continue
		}
		if e, _ := got.(*value.Map).Get("enabled"); e != enabled {
			t.Errorf("%s: enabled = %v, the file says %v", alias, e, enabled)
		}
	}
	if len(onDisk) == 0 || repos.Len() != len(onDisk) {
		t.Errorf("pkg.list_repos has %d repositories and /etc/zypp/repos.d has %d", repos.Len(), len(onDisk))
	}
	t.Logf("repositories and whether enabled, from /etc/zypp/repos.d: %v", onDisk)

	if _, err := r.Exec.Call(c, "pkg.refresh_db", value.NewMap(0)); err != nil {
		t.Errorf("pkg.refresh_db: %v", err)
	}

	ups, err := r.Exec.Call(c, "pkg.list_upgrades", value.MapOf("refresh", false))
	if err != nil {
		t.Fatalf("pkg.list_upgrades: %v", err)
	}
	m := ups.(*value.Map)
	t.Logf("pkg.list_upgrades found %d", m.Len())
	if m.Len() == 0 {
		t.Log("nothing to upgrade here, so the comparison below checked nothing")
	}
	for _, name := range m.StringKeys() {
		newer, _ := m.Get(name)
		res, err := c.Run(exec.Command{
			Argv:           []string{"rpm", "-q", "--queryformat", "%{VERSION}-%{RELEASE}\\n", name},
			IgnoreExitCode: true,
		})
		if err != nil || res.Code != 0 {
			t.Errorf("list_upgrades names %s, which rpm says is not installed", name)
			continue
		}
		// rpm prints every installed kernel; the newest is the one an
		// upgrade moves.
		newest := ""
		for _, v := range strings.Fields(res.Stdout) {
			if newest == "" || CompareRPM(v, newest) > 0 {
				newest = v
			}
		}
		if CompareRPM(value.KeyString(newer), newest) <= 0 {
			t.Errorf("list_upgrades says %s goes to %v, and rpm has %s installed", name, newer, newest)
		}
	}
}

// **pkg.latest upgrades, and a pin below the installed version brings it
// back** -- the second is the `--oldpackage` path, which without the flag
// is a silent exit 0.
const liveZypperUpgradable = "libX11-data"

func TestLiveZypperLatestUpgradesAndAPinDowngrades(t *testing.T) {
	c := zypperLive(t)
	r := New()

	before, installed := rpmVersion(t, c, liveZypperUpgradable)
	if !installed {
		t.Skipf("%s is not installed here", liveZypperUpgradable)
	}
	newer, err := r.Exec.Call(c, "pkg.latest_version", value.MapOf("name", liveZypperUpgradable))
	if err != nil {
		t.Fatal(err)
	}
	if newer == "" {
		t.Skipf("%s %s is already the newest; there is nothing to upgrade on this machine", liveZypperUpgradable, before)
	}
	t.Cleanup(func() {
		if now, _ := rpmVersion(t, c, liveZypperUpgradable); now != before {
			zypperDirect(c, "install", "--oldpackage", "--name", liveZypperUpgradable+"="+before)
		}
		if now, _ := rpmVersion(t, c, liveZypperUpgradable); now != before {
			t.Errorf("%s is %s after cleanup, and was %s", liveZypperUpgradable, now, before)
		}
	})

	res, err := r.States.Call(c, "pkg.latest", value.MapOf("name", liveZypperUpgradable))
	if err != nil || res.Failed() {
		t.Fatalf("pkg.latest: %v %v", err, res.Comment)
	}
	if now, _ := rpmVersion(t, c, liveZypperUpgradable); now != newer {
		t.Fatalf("pkg.latest said %q and rpm has %s at %s, offered %v", res.Comment, liveZypperUpgradable, now, newer)
	}
	again, err := r.States.Call(c, "pkg.latest", value.MapOf("name", liveZypperUpgradable))
	if err != nil || again.Failed() || again.Changes.Len() != 0 {
		t.Errorf("a second pkg.latest did not converge: %v %v %v", err, again.Comment, again.Changes)
	}

	res, err = r.States.Call(c, "pkg.installed", value.MapOf("name", liveZypperUpgradable, "version", before))
	if err != nil || res.Failed() {
		t.Fatalf("pkg.installed with a pin: %v %v", err, res.Comment)
	}
	if now, _ := rpmVersion(t, c, liveZypperUpgradable); now != before {
		t.Fatalf("pkg.installed pinned to %s said %q, and rpm has %s", before, res.Comment, now)
	}
}

// **One unreachable repository among good ones**: zypper exits 106 and
// did the work. The install succeeds because rpm says it did, and the
// refresh fails because it did not.
func TestLiveZypperInstallsPastAnUnreachableRepository(t *testing.T) {
	c := zypperLive(t)
	r := New()
	const repo = liveConformancePrefix + "-unreachable"

	if _, installed := rpmVersion(t, c, liveZypperPackage); installed {
		t.Skipf("%s is already installed on this machine; this test will not remove it", liveZypperPackage)
	}
	zypperDirect(c, "addrepo", "--no-gpgcheck", "http://127.0.0.1:9/", repo)
	t.Cleanup(func() {
		zypperDirect(c, "removerepo", repo)
		zypperDirect(c, "remove", "--name", liveZypperPackage)
		if _, err := os.Stat("/etc/zypp/repos.d/" + repo + ".repo"); err == nil {
			t.Errorf("the repository %s is still configured after cleanup", repo)
		}
		if _, left := rpmVersion(t, c, liveZypperPackage); left {
			t.Errorf("%s is still installed after cleanup", liveZypperPackage)
		}
	})

	if latest, err := r.Exec.Call(c, "pkg.latest_version", value.MapOf("name", liveZypperPackage)); err != nil || latest == "" {
		t.Errorf("pkg.latest_version with one repository unreachable = %v, %v", latest, err)
	}
	if _, err := r.Exec.Call(c, "pkg.install", value.MapOf("name", liveZypperPackage)); err != nil {
		t.Fatalf("pkg.install with one repository unreachable: %v", err)
	}
	if _, installed := rpmVersion(t, c, liveZypperPackage); !installed {
		t.Fatal("pkg.install succeeded and rpm does not have the package")
	}
	_, err := r.Exec.Call(c, "pkg.refresh_db", value.NewMap(0))
	if err == nil || !strings.Contains(err.Error(), repo) {
		t.Errorf("pkg.refresh_db with %s unreachable = %v; want a failure naming it", repo, err)
	}
	if _, err := r.Exec.Call(c, "pkg.remove", value.MapOf("name", liveZypperPackage)); err != nil {
		t.Fatalf("pkg.remove: %v", err)
	}
	if _, left := rpmVersion(t, c, liveZypperPackage); left {
		t.Fatal("pkg.remove returned and rpm still has the package")
	}
}
