package builtin

import (
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The `pkg` module's **apk** provider, driven against a real apk.
//
// # What was assumed until now
//
// All of it. `evidence.go` says of `pkg` that "dnf, yum, zypper, apk,
// pacman and pkgng have not been driven at all", and plan.md §7 item 16
// ranked an Alpine host for exactly this: the apk provider implements
// the upgrader and owner capabilities and neither had ever run.
//
// # What it does, and why it is destructive on purpose
//
// It installs a real package, reads it back four ways, and removes it
// again. A parser that reads correctly says nothing about whether the
// write took — which is the lesson `pf` cost (DIVERGENCE 5.31) and the
// reason the Debian container leg is destructive too.
//
// The package is `tree`: small, in Alpine's main repository, depends on
// nothing surprising, and is not something the machine needs. The test
// refuses to run if it is already installed, rather than removing
// something the machine came with.
//
// # Why it skips everywhere else
//
// `HALITE_SYSTEM_LIVE=1` and root, plus an `apk` to drive. The lab's
// Alpine row is where that is true (`make lab-up
// LAB_DISTROS='["alpine"]'`, then `make lab-test`).

const liveAPKPackage = "tree"

func apkLive(t *testing.T) *exec.Context {
	t.Helper()
	c := system(t)
	if runtime.GOOS != "linux" {
		t.Skipf("apk is Alpine's, and this is %s", runtime.GOOS)
	}
	requireToolOfFamilies(t, "apk", c.Which("apk") != "", "Alpine")
	p, err := pickPkgProvider(c)
	if err != nil {
		t.Fatalf("no package manager was recognised on this Alpine host: %v", err)
	}
	if p.Name() != "apkpkg" {
		t.Fatalf("the pkg module picked the %s provider on an Alpine host; this file tests apk", p.Name())
	}
	return c
}

// apkInstalled asks apk directly, never the module.
func apkInstalled(t *testing.T, c *exec.Context, name string) (string, bool) {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv:           []string{"apk", "info", "-e", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("`apk info -e %s`: %v", name, err)
	}
	if res.Code != 0 || strings.TrimSpace(res.Stdout) == "" {
		return "", false
	}
	// `apk info -e` prints the package name; the version comes from the
	// verbose listing, which is a different question and a different
	// command.
	version, err := c.Run(exec.Command{
		Argv:           []string{"apk", "list", "--installed", name},
		IgnoreExitCode: true,
	})
	if err != nil {
		t.Fatalf("`apk list --installed %s`: %v", name, err)
	}
	return strings.TrimSpace(version.Stdout), true
}

// **install and remove, watched through apk itself.**
func TestLiveAPKInstallsAndRemovesARealPackage(t *testing.T) {
	c := apkLive(t)
	r := New()

	if _, installed := apkInstalled(t, c, liveAPKPackage); installed {
		t.Skipf("%s is already installed on this machine; this test will not remove it", liveAPKPackage)
	}
	t.Cleanup(func() {
		_, _ = c.Run(exec.Command{
			Argv:           []string{"apk", "del", "--no-progress", liveAPKPackage},
			IgnoreExitCode: true,
		})
		if _, left := apkInstalled(t, c, liveAPKPackage); left {
			t.Errorf("%s is still installed after cleanup", liveAPKPackage)
		}
	})

	out, err := r.Exec.Call(c, "pkg.install", value.MapOf("name", liveAPKPackage))
	if err != nil {
		t.Fatalf("pkg.install: %v", err)
	}
	line, installed := apkInstalled(t, c, liveAPKPackage)
	if !installed {
		t.Fatalf("pkg.install returned %v and apk does not have the package", out)
	}
	t.Logf("`apk list --installed %s` says: %s", liveAPKPackage, line)

	// `pkg.install` answers `true` rather than a change map -- the
	// per-package delta is `pkg.upgrade`'s shape, not install's -- so
	// what it did is read from apk above and from `list_pkgs` below
	// rather than from what it returned. This test asserted a map on
	// its first run and that assumption was its own, not the module's.
	if out != true {
		t.Errorf("pkg.install returned %#v, and this module answers true", out)
	}

	// list_pkgs and version, against the same tool.
	listed, err := r.Exec.Call(c, "pkg.list_pkgs", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	pkgs, _ := listed.(*value.Map)
	version, present := pkgs.Get(liveAPKPackage)
	if !present {
		t.Fatalf("pkg.list_pkgs does not carry %s, which apk has just installed", liveAPKPackage)
	}
	if v, _ := version.(string); v == "" {
		t.Errorf("pkg.list_pkgs gives %s an empty version", liveAPKPackage)
	} else if !strings.Contains(line, v) {
		t.Errorf("pkg.list_pkgs says %s is %q; `apk list --installed` says %q", liveAPKPackage, v, line)
	}

	one, err := r.Exec.Call(c, "pkg.version", value.MapOf("name", liveAPKPackage))
	if err != nil {
		t.Fatal(err)
	}
	if one != version {
		t.Errorf("pkg.version = %v and pkg.list_pkgs = %v for the same package", one, version)
	}

	// **The owner capability**, which apk answers with `apk info -L` and
	// which nothing had run.
	files, err := r.Exec.Call(c, "pkg.file_list", value.MapOf("name", liveAPKPackage))
	if err != nil {
		t.Fatalf("pkg.file_list: %v", err)
	}
	names, _ := files.([]any)
	if len(names) == 0 {
		t.Fatal("pkg.file_list reported no files for a package that is installed")
	}
	binary := false
	for _, f := range names {
		if s, _ := f.(string); strings.HasSuffix(s, "bin/"+liveAPKPackage) {
			binary = true
		}
	}
	if !binary {
		t.Errorf("pkg.file_list does not list the package's own binary: %v", names)
	}

	removed, err := r.Exec.Call(c, "pkg.remove", value.MapOf("name", liveAPKPackage))
	if err != nil {
		t.Fatalf("pkg.remove: %v", err)
	}
	if _, left := apkInstalled(t, c, liveAPKPackage); left {
		t.Fatalf("pkg.remove returned %v and apk still has the package", removed)
	}
}

// **latest_version and list_upgrades**, the two reads that parse apk's
// less obvious output.
func TestLiveAPKReadsVersionsFromTheRealApk(t *testing.T) {
	c := apkLive(t)
	r := New()

	// A package the machine certainly has: apk itself, in the
	// `apk-tools` package.
	latest, err := r.Exec.Call(c, "pkg.latest_version", value.MapOf("name", "apk-tools"))
	if err != nil {
		t.Fatalf("pkg.latest_version: %v", err)
	}
	if s, _ := latest.(string); s == "" {
		res, _ := c.Run(exec.Command{
			Argv: []string{"apk", "list", "--available", "apk-tools"}, IgnoreExitCode: true,
		})
		t.Errorf("pkg.latest_version is empty for apk-tools; `apk list --available` says:\n%s", res.Stdout)
	} else {
		t.Logf("pkg.latest_version says apk-tools is %q", s)
	}

	// list_upgrades parses `apk version -l '<'`, whose output is empty
	// on an up-to-date machine -- which is a real answer and not a
	// failure, so this asserts the shape rather than a count.
	ups, err := r.Exec.Call(c, "pkg.list_upgrades", value.NewMap(0))
	if err != nil {
		t.Fatalf("pkg.list_upgrades: %v", err)
	}
	m, ok := ups.(*value.Map)
	if !ok {
		t.Fatalf("pkg.list_upgrades returned %T", ups)
	}
	raw, _ := c.Run(exec.Command{
		Argv: []string{"apk", "version", "-l", "<"}, IgnoreExitCode: true,
	})
	t.Logf("pkg.list_upgrades found %d; `apk version -l '<'` says:\n%s", m.Len(), raw.Stdout)
	for _, name := range m.StringKeys() {
		if !strings.Contains(raw.Stdout, name) {
			t.Errorf("pkg.list_upgrades names %q, which is not in apk's own answer", name)
		}
	}
}

// **The two capabilities apk does not have**, refused by name rather
// than answered emptily: there is no hold in the dpkg sense and no
// command that lists apk's repositories.
func TestLiveAPKRefusesHoldAndRepos(t *testing.T) {
	c := apkLive(t)
	r := New()

	for _, call := range []struct{ fn, args string }{
		{"pkg.hold", liveAPKPackage},
		{"pkg.list_repos", ""},
	} {
		var err error
		if call.args == "" {
			_, err = r.Exec.Call(c, call.fn, value.NewMap(0))
		} else {
			_, err = r.Exec.Call(c, call.fn, value.MapOf("name", call.args))
		}
		if err == nil {
			t.Errorf("%s answered on apk, which has no such concept", call.fn)
			continue
		}
		if !strings.Contains(err.Error(), "apkpkg") {
			t.Errorf("%s's refusal does not name the provider: %v", call.fn, err)
		}
	}
}
