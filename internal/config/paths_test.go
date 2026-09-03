package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultRootFollowsTheLocalConvention(t *testing.T) {
	// SPEC 27.3 states the layout in Linux FHS terms. A BSD keeps a
	// package's configuration under /usr/local/etc, durable state in
	// /var/db, and has no /run at all, so following the text literally
	// puts three sets of files where no BSD administrator looks.
	switch runtime.GOOS {
	case "freebsd", "openbsd", "netbsd", "dragonfly":
		if DefaultRoot != "/usr/local/etc/halite" {
			t.Errorf("DefaultRoot = %q on %s", DefaultRoot, runtime.GOOS)
		}
		if DefaultStateDir != "/var/db/halite" {
			t.Errorf("DefaultStateDir = %q; a BSD has no /var/lib", DefaultStateDir)
		}
		if DefaultSocketDir != "/var/run/halite" {
			t.Errorf("DefaultSocketDir = %q; a BSD has no /run", DefaultSocketDir)
		}
	case "windows":
		// SPEC 27.3 puts Windows configuration under %PROGRAMDATA%,
		// which is what the .msi registers. The FHS paths used to be
		// taken literally here, and filepath.Join turns "/etc/halite"
		// into "\\etc\\halite": configuration, keys, and cache in
		// three directories off the root of whatever drive the process
		// happened to start on.
		want := windowsRoot()
		if DefaultRoot != want {
			t.Errorf("DefaultRoot = %q, want %q", DefaultRoot, want)
		}
		if strings.HasPrefix(DefaultRoot, "/") || strings.HasPrefix(DefaultRoot, `\`) {
			t.Errorf("DefaultRoot = %q, which is a unix path on Windows", DefaultRoot)
		}
		for _, dir := range []string{DefaultStateDir, DefaultCacheDir, DefaultSocketDir} {
			if !strings.HasPrefix(dir, want) {
				t.Errorf("%q is not under %q; Windows has no /var", dir, want)
			}
		}
	default:
		if DefaultRoot != "/etc/halite" {
			t.Errorf("DefaultRoot = %q on %s", DefaultRoot, runtime.GOOS)
		}
		if DefaultStateDir != "/var/lib/halite" {
			t.Errorf("DefaultStateDir = %q", DefaultStateDir)
		}
	}

	// Whatever the platform, the derived paths hang off the root rather
	// than repeating it, so a packager who moves one moves all of them.
	if !strings.HasPrefix(DefaultPKIDir, DefaultRoot) || !strings.HasPrefix(DefaultPolicy, DefaultRoot) {
		t.Errorf("pki %q and policy %q should sit under the root %q", DefaultPKIDir, DefaultPolicy, DefaultRoot)
	}
}

// TestServiceFilesAgreeWithTheDefaultRoot is the check that would have
// caught this: contrib/rc.d already said /usr/local/etc/halite while the
// binary defaulted to /etc/halite, so on FreeBSD a service and a
// hand-run command read different configuration.
func TestServiceFilesAgreeWithTheDefaultRoot(t *testing.T) {
	cases := []struct {
		dir      string
		want     string
		wrong    string
		platform string
	}{
		{"../../contrib/rc.d", "/usr/local/etc/halite", "/etc/halite", "FreeBSD"},
		{"../../contrib/systemd", "/etc/halite", "/usr/local/etc/halite", "systemd"},
	}
	for _, c := range cases {
		// Walked rather than listed: the FIPS drop-ins live in a
		// subdirectory, and a check that skipped directories would have
		// left every one of them unread.
		var files []string
		err := filepath.WalkDir(c.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", c.dir, err)
		}
		found := false
		for _, path := range files {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			// The wrong prefix appears inside the right one, so the
			// check is for the wrong one standing alone.
			for _, line := range strings.Split(text, "\n") {
				if strings.Contains(line, c.wrong) && !strings.Contains(line, c.want) {
					t.Errorf("%s references %s, but a %s host reads %s",
						path, c.wrong, c.platform, c.want)
				}
			}
			if strings.Contains(text, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no file in %s names %s", c.dir, c.want)
		}
		if len(files) == 0 {
			t.Errorf("%s holds no files; this check has stopped checking", c.dir)
		}
	}
}

// windowsRoot's env handling is testable everywhere, which matters
// because the platform it is for is the one this build has never run on.
// The separator no longer differs off Windows: a Windows path is built
// with a backslash on every host, so this asserts the exact string
// rather than whatever the host's filepath.Join would have produced.
func TestWindowsRootFollowsProgramData(t *testing.T) {
	t.Setenv("PROGRAMDATA", `D:\Data`)
	if got, want := windowsRoot(), `D:\Data\Halite`; got != want {
		t.Errorf("windowsRoot() = %q, want %q", got, want)
	}

	// A bare drive keeps its root separator rather than naming that
	// drive's working directory.
	t.Setenv("PROGRAMDATA", "D:")
	if got, want := windowsRoot(), `D:\Halite`; got != want {
		t.Errorf("windowsRoot() with a bare drive = %q, want %q", got, want)
	}

	t.Setenv("PROGRAMDATA", "")
	got := windowsRoot()
	if !strings.Contains(got, "ProgramData") || !strings.HasSuffix(got, "Halite") {
		t.Errorf("with no PROGRAMDATA, windowsRoot() = %q", got)
	}
	if got == "Halite" {
		t.Error("windowsRoot() fell back to a relative path")
	}
}

// The platform table in docs/getting-started.md is what an operator
// reads before installing on a machine this build has never run on.
// Three of the four platforms are in that position, so the table is
// checked against the code rather than trusted.
func TestTheDocumentedPlatformTableMatchesTheCode(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "getting-started.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	// Windows is skipped: its paths are built from %PROGRAMDATA% at
	// runtime and the document names the variable, not a resolved path.
	cases := []struct{ goos, label string }{
		{"linux", "Linux"},
		{"freebsd", "FreeBSD"},
		{"darwin", "macOS"},
	}
	for _, c := range cases {
		for _, want := range []string{
			RootFor(c.goos),
			VarPathFor(c.goos, "lib"),
			VarPathFor(c.goos, "cache"),
		} {
			if !strings.Contains(text, "`"+want+"`") {
				t.Errorf("%s: getting-started.md does not name %s", c.label, want)
			}
		}
	}

	// And the Windows row names the variable rather than a path that
	// would only ever be right on the machine the document was written
	// on.
	if !strings.Contains(text, "%PROGRAMDATA%") {
		t.Error("getting-started.md does not say where Windows keeps its files")
	}
	// The Windows row itself must not carry a unix path, which is what
	// the code used to produce there.
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "| Windows ") {
			continue
		}
		if strings.Contains(line, "/etc/") || strings.Contains(line, "/var/") {
			t.Errorf("the Windows row carries a unix path: %s", line)
		}
	}
}

// A path that is one of the defaults renders as its token, and one under
// a default renders as that token plus the rest.
//
// On Windows every default directory is a child of the configuration
// root, so a scan that took the first prefix match rendered the cache
// directory as `<config root>\cache` and docs/configuration.md generated
// there did not match the committed one. The separator in the remainder
// is normalised for the same reason: the token stands for a path on
// every platform, so what follows it must not name one.
func TestAPortablePathPrefersTheLongestMatchAndOneSeparator(t *testing.T) {
	cases := []struct{ in, want string }{
		{DefaultRoot, "<config root>"},
		{DefaultCacheDir, "<cache dir>"},
		{DefaultStateDir, "<state dir>"},
		{DefaultPKIDir, "<config root>/pki"},
		{DefaultPolicy, "<config root>/policy.yaml"},
		{filepath.Join(DefaultCacheDir, "files", "base"), "<cache dir>/files/base"},
		{filepath.Join("elsewhere", "entirely"), filepath.Join("elsewhere", "entirely")},
	}
	for _, c := range cases {
		if got := PortablePath(c.in); got != c.want {
			t.Errorf("PortablePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every platform's layout is computed from the target, so all four can
// be checked from one host. filepath.Join takes the separator from the
// machine it runs on, which made this test vacuous on Windows: it
// asserted \etc\halite and got it.
func TestALayoutIsInItsOwnPlatformsConvention(t *testing.T) {
	cases := []struct{ goos, root, lib, run string }{
		{"linux", "/etc/halite", "/var/lib/halite", "/run/halite"},
		{"darwin", "/etc/halite", "/var/lib/halite", "/run/halite"},
		{"freebsd", "/usr/local/etc/halite", "/var/db/halite", "/var/run/halite"},
	}
	for _, c := range cases {
		if got := RootFor(c.goos); got != c.root {
			t.Errorf("RootFor(%q) = %q, want %q", c.goos, got, c.root)
		}
		if got := VarPathFor(c.goos, "lib"); got != c.lib {
			t.Errorf("VarPathFor(%q, lib) = %q, want %q", c.goos, got, c.lib)
		}
		if got := RunPathFor(c.goos); got != c.run {
			t.Errorf("RunPathFor(%q) = %q, want %q", c.goos, got, c.run)
		}
	}

	t.Setenv("PROGRAMDATA", `C:\ProgramData`)
	for _, c := range []struct{ got, want string }{
		{RootFor("windows"), `C:\ProgramData\Halite`},
		{VarPathFor("windows", "lib"), `C:\ProgramData\Halite\lib`},
		{RunPathFor("windows"), `C:\ProgramData\Halite\run`},
	} {
		if c.got != c.want {
			t.Errorf("the Windows layout gave %q, want %q", c.got, c.want)
		}
	}
}
