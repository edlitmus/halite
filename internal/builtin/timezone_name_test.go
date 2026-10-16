package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A zone name is a path into the tz database, and linkZone joined it to
// zoneinfoDir unchecked. `linkZone("../secret", "freebsd")` copied a 0600
// file's contents into /etc/localtime at 0644, and wrote `../secret` into
// /var/db/zoneinfo, which `tzsetup -r` reads back; on a Linux without
// timedatectl it pointed /etc/localtime at the file. Only the state ever
// checked the name, against listZones; timezone.set_zone checked only
// that it was not empty. DIVERGENCE 5.314.
//
// Each refusal asserts that nothing was written: the localtime copy is
// untouched, no link appeared, and the name record was not created.
func TestLinkZoneRefusesANameOutsideTheTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix zone files do not exist on Windows")
	}
	for _, goos := range []string{"freebsd", "linux"} {
		for _, name := range []string{
			"../secret",
			"Etc/../../secret",
			"Etc/../UTC", // inside the tree, and still not a name
			"./UTC",
			"/SECRET",            // absolute: replaced below with the real path
			"Etc//UTC",           // an empty component
			"Etc/UTC/",           // a trailing separator
			"-UTC",               // would be an option to any tool handed it
			"Europe/Berlin\nUTC", // two lines in the name record
			"Etc/UTC\x00",        // a NUL
			"Escape",             // a link in the tree pointing out of it
			"EscapeDir/secret",   // a directory link pointing out of it
			strings.Repeat("A", 300),
		} {
			t.Run(goos+"/"+name, func(t *testing.T) {
				dir := t.TempDir()
				restore := redirectZoneFiles(t, dir)
				defer restore()
				secret := filepath.Join(dir, "secret")
				if err := os.WriteFile(secret, []byte("TZif-secret"), 0o600); err != nil {
					t.Fatal(err)
				}
				if name == "/SECRET" {
					name = secret
				}
				writeFile(t, filepath.Join(zoneinfoDir, "UTC"), "TZif-utc")
				writeFile(t, filepath.Join(zoneinfoDir, "Etc/UTC"), "TZif-etc-utc")
				if err := os.Symlink(secret, filepath.Join(zoneinfoDir, "Escape")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir, filepath.Join(zoneinfoDir, "EscapeDir")); err != nil {
					t.Fatal(err)
				}
				// FreeBSD holds a copy; Linux a link. Both are what the
				// node already had, and neither may change.
				if goos == "freebsd" {
					writeFile(t, localtimePath, "TZif-before")
				} else if err := os.Symlink(filepath.Join(zoneinfoDir, "UTC"), localtimePath); err != nil {
					t.Fatal(err)
				}

				if err := linkZone(name, goos); err == nil {
					t.Fatalf("linkZone(%q) was accepted", name)
				}

				if goos == "freebsd" {
					if got := readFile(t, localtimePath); got != "TZif-before" {
						t.Errorf("%s now holds %q", localtimePath, got)
					}
				} else if got, err := os.Readlink(localtimePath); err != nil || got != filepath.Join(zoneinfoDir, "UTC") {
					t.Errorf("%s now points at %q (%v)", localtimePath, got, err)
				}
				if _, err := os.Lstat(zoneNamePath); err == nil {
					t.Errorf("%s was written: %q", zoneNamePath, readFile(t, zoneNamePath))
				}
			})
		}
	}
}

// Real names still pass, including the shapes a too-tight grammar would
// refuse: three components, a `+` and a `-` in a component, a name with no
// slash at all, a hyphenated city, an alternate tree's prefix, and a
// `backward` alias that is a link inside the tree -- which is how Alpine
// and recent Debian ship them.
func TestLinkZoneAcceptsRealNames(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix zone files do not exist on Windows")
	}
	for _, goos := range []string{"freebsd", "linux"} {
		for _, name := range []string{
			"America/Argentina/Buenos_Aires",
			"Etc/GMT+5",
			"Etc/GMT-14",
			"UTC",
			"EST5EDT",
			"America/Port-au-Prince",
			"posix/Europe/Berlin",
			"US/Pacific",
		} {
			t.Run(goos+"/"+name, func(t *testing.T) {
				dir := t.TempDir()
				restore := redirectZoneFiles(t, dir)
				defer restore()
				body := "TZif-" + name
				if name == "US/Pacific" {
					writeFile(t, filepath.Join(zoneinfoDir, "America/Los_Angeles"), body)
					if err := os.MkdirAll(filepath.Join(zoneinfoDir, "US"), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("../America/Los_Angeles", filepath.Join(zoneinfoDir, "US/Pacific")); err != nil {
						t.Fatal(err)
					}
				} else {
					writeFile(t, filepath.Join(zoneinfoDir, filepath.FromSlash(name)), body)
				}
				if goos == "freebsd" {
					writeFile(t, localtimePath, "TZif-before")
				}

				if err := linkZone(name, goos); err != nil {
					t.Fatalf("linkZone(%q): %v", name, err)
				}
				if got := readFile(t, localtimePath); got != body {
					t.Errorf("%s holds %q, want %q", localtimePath, got, body)
				}
				if goos == "freebsd" {
					if got := strings.TrimSpace(readFile(t, zoneNamePath)); got != name {
						t.Errorf("%s holds %q, want %q", zoneNamePath, got, name)
					}
				}
			})
		}
	}
}

// timezone.set_zone refuses a name that is not one before anything else
// happens, in test mode as well: test mode said it would set `../secret`,
// which is a promise the real run should not have been able to keep.
func TestSetZoneRefusesANameThatIsAPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows zone names are not paths; tzutil checks them")
	}
	r := New()
	for _, test := range []bool{true, false} {
		c, _, _ := zoneFixture(t)
		c.Test = test
		secret := filepath.Join(filepath.Dir(zoneinfoDir), "secret")
		if err := os.WriteFile(secret, []byte("TZif-secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Readlink(localtimePath)
		for _, name := range []string{"../secret", secret} {
			if _, err := r.Exec.Call(c, "timezone.set_zone", value.MapOf("timezone", name)); err == nil {
				t.Errorf("set_zone(%q) with test=%v was accepted", name, test)
			} else if !strings.Contains(err.Error(), "not a time zone name") {
				t.Errorf("set_zone(%q) refused for another reason: %v", name, err)
			}
			if after, _ := os.Readlink(localtimePath); after != before {
				t.Errorf("set_zone(%q) moved %s to %q", name, localtimePath, after)
			}
		}

		// And the state, which used to rely on listZones alone and so
		// let the name through on a node whose tree could not be read.
		res, err := r.States.Call(c, "timezone.system", value.MapOf("name", "../secret"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Result == nil || *res.Result {
			t.Errorf("timezone.system accepted ../secret with test=%v: %s", test, res.Comment)
		}
	}
}

func TestZoneNameGrammar(t *testing.T) {
	for name, ok := range map[string]bool{
		"UTC": true, "Etc/GMT+5": true, "Etc/GMT-14": true, "GMT0": true,
		"America/Argentina/Buenos_Aires": true, "posix/Europe/Berlin": true,
		"America/Port-au-Prince": true, "right/UTC": true,

		"": false, "/": false, "/UTC": false, "UTC/": false, "a//b": false,
		".": false, "..": false, "../x": false, "a/./b": false, "a/../b": false,
		"zone.tab": false, "-x": false, "Etc/-x": false, "a b": false,
		"a\\b": false, "a\nb": false, strings.Repeat("a", 129): false,
	} {
		if err := checkZoneName(name); (err == nil) != ok {
			t.Errorf("checkZoneName(%q) = %v, want ok=%v", name, err, ok)
		}
	}
}
