package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

// The listing is dpkg-query's package, architecture, version and status
// for every package in a debian:bookworm container on amd64 with i386
// added and hello:i386 and libc6:i386 installed, captured 2026-10-28.
// The keys wanted are what Salt 3006.28 and 3008.3 pkg.list_pkgs returned
// on that same container: the native and `all` packages bare, the i386
// ones qualified, and no bare `hello`, which has no native install.
func TestAForeignArchitecturePackageIsKeyedAsSaltKeysIt(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "apt", "bookworm-amd64-i386.dpkg-query"))
	if err != nil {
		t.Fatal(err)
	}
	pkgs := aptListPkgs(string(b))
	for name, want := range map[string]string{
		"gcc-12-base":      "12.2.0-14+deb12u1",
		"gcc-12-base:i386": "12.2.0-14+deb12u1",
		"hello:i386":       "2.10-3",
		"libc6":            "2.36-9+deb12u14",
		"libc6:i386":       "2.36-9+deb12u14",
		"libgcc-s1":        "12.2.0-14+deb12u1",
		"libgcc-s1:i386":   "12.2.0-14+deb12u1",
		"tzdata":           "2026e-0+deb12u1",
	} {
		got, ok := pkgs.Get(name)
		if !ok || got != want {
			t.Errorf("%s = %v (present %v), want %s", name, got, ok, want)
		}
	}
	for _, name := range []string{"hello", "tzdata:all", "libc6:amd64"} {
		if _, ok := pkgs.Get(name); ok {
			t.Errorf("%s is listed; Salt does not list it", name)
		}
	}
}

// Without a dpkg line the native architecture is unknown, and every name
// is bare, as the listing always was.
func TestWithoutDpkgsLineNamesStayBare(t *testing.T) {
	pkgs := aptListPkgs("curl\tamd64\t7.81.0\tinstall ok installed\nhello\ti386\t2.10-3\tinstall ok installed\n")
	for _, name := range []string{"curl", "hello"} {
		if _, ok := pkgs.Get(name); !ok {
			t.Errorf("%s is missing", name)
		}
	}
}
