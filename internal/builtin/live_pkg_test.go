package builtin

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// The apt provider's 5.40 functions, driven against real apt and dpkg.
//
// # What was assumed until now
//
// `info_installed`, `file_dict`, `download` and `list_downloaded` are new
// (DIVERGENCE §3, 5.40). Their parsers were written from what
// `dpkg-query`, `dpkg-deb` and `apt-get download` are documented to
// print. These run the real tools and compare field by field.
//
// `autoremove` is not here: it removes packages, and this is the host
// the Linux work is done on. Its live leg is in the fleet container
// (`live_debian_test.go`), which is thrown away.
//
// # Why it skips everywhere else
//
// `HALITE_SYSTEM_LIVE=1`, the gate `hostname`, `sysctl`, `apparmor`,
// `netplan` and `service` share.

func pkgLive(t *testing.T) *exec.Context {
	t.Helper()
	c := system(t)
	if runtime.GOOS != "linux" {
		t.Skipf("the apt provider is Linux's, and this is %s", runtime.GOOS)
	}
	if c.Which("dpkg-query") == "" || c.Which("apt-get") == "" {
		t.Fatal("HALITE_SYSTEM_LIVE is set and there is no apt/dpkg; this is not a Debian-family node")
	}
	return c
}

func dpkgField(t *testing.T, c *exec.Context, pkg, field string) string {
	t.Helper()
	res, err := c.Run(exec.Command{
		Argv: []string{"dpkg-query", "-W", "-f=" + field, pkg},
	})
	if err != nil {
		t.Fatalf("dpkg-query %s %s: %v", field, pkg, err)
	}
	return strings.TrimSpace(res.Stdout)
}

// **info_installed, field by field against dpkg-query.**
func TestLivePkgInfoInstalledAgreesWithDpkg(t *testing.T) {
	c := pkgLive(t)
	r := New()

	out, err := r.Exec.Call(c, "pkg.info_installed", value.MapOf("pkgs", []any{"dpkg", "tzdata"}))
	if err != nil {
		t.Fatal(err)
	}
	info, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("info_installed returned %T", out)
	}

	for _, pkg := range []string{"dpkg", "tzdata"} {
		m, ok := info.Get(pkg)
		if !ok {
			t.Errorf("%s is not in the result: %v", pkg, info.StringKeys())
			continue
		}
		fields := m.(*value.Map)
		for _, tc := range []struct{ key, dpkg string }{
			{"version", "${Version}"},
			{"architecture", "${Architecture}"},
			{"section", "${Section}"},
		} {
			want := dpkgField(t, c, pkg, tc.dpkg)
			if got, _ := fields.Get(tc.key); got != want {
				t.Errorf("%s.%s = %q, dpkg-query says %q", pkg, tc.key, got, want)
			}
			if want == "" {
				t.Errorf("dpkg-query returned nothing for %s of %s", tc.dpkg, pkg)
			}
		}
	}

	// A package that is not installed is absent from the result rather
	// than present with empty fields.
	out, err = r.Exec.Call(c, "pkg.info_installed", value.MapOf("pkgs", []any{"this-package-is-not-installed-anywhere"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(*value.Map); got.Len() != 0 {
		t.Errorf("info_installed of an absent package returned %v", got.StringKeys())
	}
}

// **file_dict against `dpkg -L`, per package.**
func TestLivePkgFileDictAgreesWithDpkg(t *testing.T) {
	c := pkgLive(t)
	r := New()

	pkgs := []string{"tzdata", "dpkg"}
	out, err := r.Exec.Call(c, "pkg.file_dict", value.MapOf("pkgs", toAnyList(pkgs)))
	if err != nil {
		t.Fatal(err)
	}
	dict, ok := out.(*value.Map)
	if !ok {
		t.Fatalf("file_dict returned %T", out)
	}

	for _, pkg := range pkgs {
		v, ok := dict.Get(pkg)
		if !ok {
			t.Errorf("%s missing from file_dict", pkg)
			continue
		}
		list, _ := v.([]any)
		got := map[string]bool{}
		for _, e := range list {
			if s, _ := e.(string); s != "" {
				got[s] = true
			}
		}

		res, err := c.Run(exec.Command{Argv: []string{"dpkg", "-L", pkg}})
		if err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, ln := range strings.Split(res.Stdout, "\n") {
			ln = strings.TrimSpace(ln)
			// file_dict returns paths, not the directory or /. entries
			// dpkg also lists; only compare the ones that are real files.
			if !strings.HasPrefix(ln, "/") || ln == "/." {
				continue
			}
			if fi, err := os.Stat(ln); err != nil || fi.IsDir() {
				continue
			}
			checked++
			if !got[ln] {
				t.Errorf("file_dict[%s] is missing %q, which `dpkg -L` lists", pkg, ln)
			}
		}
		if checked == 0 {
			t.Errorf("no regular files found for %s; the comparison proved nothing", pkg)
		}
	}
}

// **download a real package, then read it back with list_downloaded.**
//
// The archive directory is redirected, so nothing is written near
// /var/cache/apt and the cleanup is a directory removal.
func TestLivePkgDownloadAndListDownloaded(t *testing.T) {
	c := pkgLive(t)
	r := New()

	if res, err := c.Run(exec.Command{
		Argv:           []string{"apt-cache", "show", "hello"},
		IgnoreExitCode: true,
	}); err != nil || res.Code != 0 {
		t.Skip("the `hello` package is not available to this node's apt")
	}

	dir := t.TempDir()
	old := aptArchivesDir
	aptArchivesDir = dir
	t.Cleanup(func() { aptArchivesDir = old })

	out, err := r.Exec.Call(c, "pkg.download", value.MapOf("pkgs", []any{"hello"}))
	if err != nil {
		t.Fatalf("pkg.download: %v", err)
	}
	got := out.(*value.Map)
	m, ok := got.Get("hello")
	if !ok {
		t.Fatalf("download did not report hello: %v", got.StringKeys())
	}
	fields := m.(*value.Map)
	ver, _ := fields.Get("version")
	if v, _ := ver.(string); v == "" {
		t.Error("downloaded hello has no version")
	}
	path, _ := fields.Get("path")
	ps, _ := path.(string)
	if !strings.HasPrefix(ps, dir) {
		t.Errorf("downloaded path %q is not under the redirected cache %q", ps, dir)
	}
	if _, err := os.Stat(ps); err != nil {
		t.Errorf("the reported .deb is not on disk: %v", err)
	}

	// list_downloaded reads the same cache and reports the same package
	// and version.
	out, err = r.Exec.Call(c, "pkg.list_downloaded", value.NewMap(0))
	if err != nil {
		t.Fatal(err)
	}
	listed := out.(*value.Map)
	lm, ok := listed.Get("hello")
	if !ok {
		t.Fatalf("list_downloaded does not see the package download just fetched: %v", listed.StringKeys())
	}
	if lv, _ := lm.(*value.Map).Get("version"); lv != ver {
		t.Errorf("list_downloaded version %q != download version %q", lv, ver)
	}
}
