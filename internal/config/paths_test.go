package config

import (
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
		entries, err := os.ReadDir(c.dir)
		if err != nil {
			t.Fatalf("%s: %v", c.dir, err)
		}
		found := false
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			body, err := os.ReadFile(filepath.Join(c.dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			// The wrong prefix appears inside the right one, so the
			// check is for the wrong one standing alone.
			for _, line := range strings.Split(text, "\n") {
				if strings.Contains(line, c.wrong) && !strings.Contains(line, c.want) {
					t.Errorf("%s/%s references %s, but a %s host reads %s",
						c.dir, e.Name(), c.wrong, c.platform, c.want)
				}
			}
			if strings.Contains(text, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("no file in %s names %s", c.dir, c.want)
		}
	}
}
