package builtin

import (
	"errors"
	"os"
	osexec "os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/grains"
	"github.com/edlitmus/halite/internal/value"
)

// The Debian half of SPEC 31's version-comparison differential.
//
// SPEC 31 asks for `pkg.version_cmp` to be checked against
// `dpkg --compare-versions` and `rpmdev-vercmp`. The RPM half has run on
// Rocky and AlmaLinux since 5.172 (`TestLiveRpmVersionCmpAgreesWithRpm`);
// this half had no host and its absence was recorded, in 5.2 and in
// `vercmp_test.go`, as "needs a Debian or RHEL host". That was true when
// it was written and had stopped being true: `make fleetcheck` runs a
// real Debian with a real dpkg, and this project has had Debian-family
// machines to hand since. See 5.631.
//
// Like the rpm reads, this needs neither root nor `HALITE_SYSTEM_LIVE`:
// `dpkg --compare-versions` parses two strings and exits, touching no
// package database and changing nothing. It is named `TestLive*` because
// it drives a real tool and must appear in the live log beside the tests
// that do need a machine.

// requireDpkg skips, with the reason, anywhere dpkg cannot be asked.
func requireDpkg(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("dpkg is a Debian-family tool; this is %s", runtime.GOOS)
	}
	if _, err := osexec.LookPath("dpkg"); err != nil {
		t.Skip("this host has no dpkg; the Debian ordering needs a Debian-family system")
	}
}

// dpkgRelation puts one relation to dpkg and returns whether it holds.
//
// `dpkg --compare-versions` answers by exit status: 0 if the relation
// holds, 1 if it does not, and 2 for a usage error. The third is a
// failure of this test rather than a false comparison, so it is kept
// distinct -- a version string this project accepts and dpkg refuses
// outright is a finding, not a disagreement about ordering.
//
// dpkg warns on stderr about a version outside Debian policy (one that
// does not begin with a digit, for instance) and still compares it. The
// warning is returned so a vector that relies on such a version is
// visible in the log rather than silently trusted.
func dpkgRelation(t *testing.T, a, op, b string) (holds bool, warning string) {
	t.Helper()
	cmd := osexec.Command("dpkg", "--compare-versions", a, op, b)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	warning = strings.TrimSpace(stderr.String())
	if err == nil {
		return true, warning
	}
	var exit *osexec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, warning
	}
	t.Fatalf("dpkg --compare-versions %q %s %q: %v (%s)", a, op, b, err, warning)
	return false, warning
}

// dpkgCmp asks dpkg for a three-way answer by putting all three relations
// to it and requiring exactly one to hold.
//
// Asking `lt` alone would take dpkg's "false" for "greater or equal" and
// never notice a tool -- or a test -- that disagreed with itself. Asking
// all three means dpkg's own answer has to be a total order before it is
// used as an oracle for one.
func dpkgCmp(t *testing.T, a, b string) (int, string) {
	t.Helper()
	lt, warning := dpkgRelation(t, a, "lt", b)
	eq, _ := dpkgRelation(t, a, "eq", b)
	gt, _ := dpkgRelation(t, a, "gt", b)

	switch {
	case lt && !eq && !gt:
		return -1, warning
	case eq && !lt && !gt:
		return 0, warning
	case gt && !lt && !eq:
		return 1, warning
	}
	t.Fatalf("dpkg says %q lt %q is %v, eq is %v, gt is %v, which is not one relation of three",
		a, b, lt, eq, gt)
	return 0, warning
}

// TestLiveDpkgVersionCmpAgreesWithDpkg is the differential: every vector
// the offline test holds, put to a real dpkg through the module rather
// than to `CompareDebian` directly, so the registration and the scheme
// dispatch are covered with the algorithm.
func TestLiveDpkgVersionCmpAgreesWithDpkg(t *testing.T) {
	requireDpkg(t)

	version := strings.TrimSpace(dpkgToolVersion(t))
	t.Logf("dpkg %s on %s", version, runtime.GOARCH)

	r := New()
	c := &exec.Context{}
	warned := 0
	for _, vector := range debianVectors {
		a, want, b := vector[0], vector[1], vector[2]
		expect := map[string]int{"<": -1, "=": 0, ">": 1}[want]

		real, warning := dpkgCmp(t, a, b)
		if warning != "" {
			warned++
			t.Logf("dpkg warns about this pair and compares it anyway: %q vs %q: %s", a, b, warning)
		}

		// The table's own expectation first. A vector that dpkg answers
		// differently is a defect in the table, and the table is what the
		// offline test has been trusting on every machine with no dpkg.
		if real != expect {
			t.Errorf("the table says %q %s %q; dpkg %s says %d", a, want, b, version, real)
		}

		got, err := r.Exec.Call(c, "pkg.version_cmp",
			value.MapOf("pkg1", a, "pkg2", b, "scheme", "debian"))
		if err != nil {
			t.Fatalf("pkg.version_cmp(%q, %q): %v", a, b, err)
		}
		if got != int64(real) {
			t.Errorf("pkg.version_cmp(%q, %q) = %v; dpkg %s says %d", a, b, got, version, real)
		}
	}
	t.Logf("%d vectors against dpkg %s, %d of them outside Debian policy",
		len(debianVectors), version, warned)

	// The algorithm is only reached if the dispatch picks it. `auto` reads
	// `os_family` from the node's grains and falls back to the RPM
	// ordering for a family it does not know, silently -- so on a machine
	// with a real dpkg, a grain that failed to say "Debian" would compare
	// Debian versions with rpm's rules and report nothing. The two
	// orderings disagree about `1.0` against `1_0`, among others.
	t.Run("auto picks debian on a machine with dpkg", func(t *testing.T) {
		g, warnings := grains.Collect(grains.Options{})
		for _, w := range warnings {
			t.Logf("grains warning: %v", w)
		}
		family, _ := g.Get("os_family")
		scheme, err := versionScheme("auto", value.KeyString(family))
		if err != nil {
			t.Fatalf("versionScheme(auto, %q): %v", value.KeyString(family), err)
		}
		t.Logf("os_family %q resolves to the %q ordering", value.KeyString(family), scheme)
		if scheme != "debian" {
			t.Errorf("this host has dpkg and os_family %q resolves to the %q ordering; "+
				"a Debian node would compare its versions by rpm's rules",
				value.KeyString(family), scheme)
		}
	})
}

// dpkgVersion is dpkg's own version, for the log: an ordering is a claim
// about a particular dpkg, and 1.22 is not 1.18.
func dpkgToolVersion(t *testing.T) string {
	t.Helper()
	out, err := osexec.Command("dpkg", "--version").Output()
	if err != nil {
		t.Fatalf("dpkg --version: %v", err)
	}
	line := strings.SplitN(string(out), "\n", 2)[0]
	// "Debian 'dpkg' package management program version 1.22.22 (amd64)."
	if i := strings.Index(line, "version "); i >= 0 {
		return strings.TrimSuffix(strings.Fields(line[i+len("version "):])[0], ".")
	}
	return line
}
