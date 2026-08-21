package builtin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// The cases below are the ones dpkg's and rpm's own test suites use.
// SPEC section 31 asks for a differential test against
// `dpkg --compare-versions` and `rpmdev-vercmp`, which needs a Debian or
// RHEL host to run; until there is one, their published vectors are the
// nearest honest thing, and they are the cases the algorithms are known
// to get wrong.

func checkCmp(t *testing.T, name string, cmp func(string, string) int, a, want, b string) {
	t.Helper()
	got := cmp(a, b)
	expect := map[string]int{"<": -1, "=": 0, ">": 1}[want]
	if got != expect {
		t.Errorf("%s: %q %s %q, got %d", name, a, want, b, got)
	}
	// The comparison must be antisymmetric, or a sort using it is
	// unstable in a way that depends on input order.
	if back := cmp(b, a); back != -expect {
		t.Errorf("%s: %q vs %q is %d but %q vs %q is %d; the comparison is not antisymmetric",
			name, a, b, got, b, a, back)
	}
}

// From dpkg's t/Dpkg_Version.t and the Debian policy manual.
func TestCompareDebian(t *testing.T) {
	cases := [][3]string{
		{"1.0", "=", "1.0"},
		{"1.0", "<", "1.1"},
		{"1.9", "<", "1.10"},
		{"1.0", "<", "1.0.1"},
		{"1.0-1", "<", "1.0-2"},
		{"1.0-1", "=", "1.0-1"},

		// The epoch beats everything after it.
		{"1:1.0", ">", "2.0"},
		{"1:1.0", "<", "2:0.1"},
		{"0:1.0", "=", "1.0"},

		// The tilde sorts before everything, including the empty string.
		// Getting this backwards installs a release candidate over a
		// release, which is the reason it is called out in SPEC 15.2.
		{"1.0~rc1", "<", "1.0"},
		{"1.0~rc1", "<", "1.0~rc2"},
		{"1.0~~", "<", "1.0~"},
		{"1.0~", "<", "1.0"},
		{"1.0~beta1~svn1245", "<", "1.0~beta1"},
		{"1.0~beta1", "<", "1.0"},

		// Letters sort before every other non-letter, so an alpha
		// release precedes the plain version's separator.
		{"1.0a", "<", "1.0b"},
		{"1.0a", ">", "1.0"},
		{"1.0+b1", ">", "1.0"},

		// Leading zeros in a digit run do not count.
		{"1.007", "=", "1.7"},
		{"1.0-007", "=", "1.0-7"},

		// A hyphen in the upstream version belongs to the upstream
		// version; only the last one starts the revision.
		{"1.0-beta-1", "<", "1.0-beta-2"},

		// Real ones, from Debian archives.
		{"2.4.7-1", "<", "2.4.7-2"},
		{"1:2.4.7-1", ">", "2.4.8-1"},
		{"5.4.0-1023.24", "<", "5.4.0-1023.25"},
		{"1.2.3-1~bpo11+1", "<", "1.2.3-1"},
	}
	for _, c := range cases {
		checkCmp(t, "debian", CompareDebian, c[0], c[1], c[2])
	}
}

// From rpm's tests/rpmvercmp.at.
func TestCompareRPM(t *testing.T) {
	cases := [][3]string{
		{"1.0", "=", "1.0"},
		{"1.0", "<", "1.1"},
		{"1.0", "<", "1.0.1"},
		{"2.0", ">", "1.0"},
		{"1.9", "<", "1.10"},

		// Separators are skipped, so the punctuation between segments
		// does not matter.
		{"1.0", "=", "1_0"},
		{"1.0", "=", "1..0"},
		{"2.0.1", "=", "2.0.1"},

		// Digits beat letters where the two meet at the same position,
		// because 10 is a later version than "beta".
		{"1.a", "<", "1.1"},
		{"1.1", ">", "1.a"},
		{"alpha", "<", "beta"},
		// That is not the same as one version simply having more left:
		// whichever still has characters wins, so a trailing letter is an
		// addition rather than a demotion. Debian agrees here.
		{"1.0a", ">", "1.0"},

		// Leading zeros do not count.
		{"1.0010", ">", "1.9"},
		{"1.05", "=", "1.5"},

		// The tilde, as in Debian.
		{"1.0~rc1", "<", "1.0"},
		{"1.0~rc1", "<", "1.0~rc2"},
		{"1.0~rc1", "=", "1.0~rc1"},

		// The caret sorts after nothing and before anything else, which
		// is how a post-release snapshot sits between two releases.
		{"1.0^", ">", "1.0"},
		{"1.0^", "<", "1.0.1"},
		{"1.0^git1", ">", "1.0"},
		{"1.0^git1", "<", "1.1"},
		{"1.0^git1", "=", "1.0^git1"},
		{"1.0~rc1^git1", ">", "1.0~rc1"},
		{"1.0^git1~pre", "<", "1.0^git1"},

		// Epoch and release.
		{"1:1.0", ">", "2.0"},
		{"1:1.0-1", "<", "1:1.0-2"},
		{"1.0-1", "=", "1.0-1"},

		// Real ones.
		{"4.18.0-425.3.1.el8", "<", "4.18.0-425.10.1.el8"},
		{"2.34-60.el9", "<", "2.34-60.el9_2.7"},
	}
	for _, c := range cases {
		checkCmp(t, "rpm", CompareRPM, c[0], c[1], c[2])
	}
}

// An absent release compares equal to any release in RPM, which is how
// `1.0` and `1.0-1` are the same package to rpm and how a query without a
// release still matches.
func TestRPMAbsentReleaseMatchesAny(t *testing.T) {
	if got := CompareRPM("1.0", "1.0-5"); got != 0 {
		t.Errorf("1.0 vs 1.0-5 = %d, want 0", got)
	}
	// It is not a wildcard on the version, though.
	if got := CompareRPM("1.0", "1.1-5"); got != -1 {
		t.Errorf("1.0 vs 1.1-5 = %d, want -1", got)
	}
}

// Comparison must be a total order, or a sort built on it produces a
// different answer depending on the order it started in.
func TestComparisonIsATotalOrder(t *testing.T) {
	versions := []string{
		"1.0~~", "1.0~", "1.0~rc1", "1.0", "1.0a", "1.0-1", "1.0.1", "1.1", "2.0", "1:0.1",
	}
	for _, cmp := range []struct {
		name string
		fn   func(string, string) int
	}{{"debian", CompareDebian}, {"rpm", CompareRPM}} {
		for _, a := range versions {
			if c := cmp.fn(a, a); c != 0 {
				t.Errorf("%s: %q compared with itself is %d", cmp.name, a, c)
			}
			for _, b := range versions {
				ab, ba := cmp.fn(a, b), cmp.fn(b, a)
				if ab != -ba {
					t.Errorf("%s: %q vs %q is %d but the reverse is %d", cmp.name, a, b, ab, ba)
				}
				// Transitivity, over every third version.
				for _, c := range versions {
					bc := cmp.fn(b, c)
					if ab < 0 && bc < 0 && cmp.fn(a, c) >= 0 {
						t.Errorf("%s: %q < %q < %q but %q is not less than %q", cmp.name, a, b, c, a, c)
					}
				}
			}
		}
	}
}

func TestVersionScheme(t *testing.T) {
	cases := map[[2]string]string{
		{"", "Debian"}:    "debian",
		{"", "RedHat"}:    "rpm",
		{"", "FreeBSD"}:   "freebsd",
		{"", "Something"}: "rpm",
		{"deb", ""}:       "debian",
		{"zypper", ""}:    "rpm",
		{"pkgng", ""}:     "freebsd",
	}
	for in, want := range cases {
		got, err := versionScheme(in[0], in[1])
		if err != nil || got != want {
			t.Errorf("versionScheme(%q, %q) = %q, %v; want %q", in[0], in[1], got, err, want)
		}
	}
	if _, err := versionScheme("nonsense", ""); err == nil {
		t.Error("an unknown scheme should be refused rather than guessed at")
	}
}

// A malformed epoch is not an epoch. `1.0:2` has no numeric prefix, so
// the whole string is the version.
func TestMalformedEpochIsNotAnEpoch(t *testing.T) {
	e, v, _ := splitDebian("1.0:2")
	if e != 0 || v != "1.0:2" {
		t.Errorf("splitDebian(%q) = %d, %q", "1.0:2", e, v)
	}
	if fmt.Sprint(CompareDebian("1.0:2", "1.0:2")) != "0" {
		t.Error("a version with a colon should compare equal to itself")
	}
}

// A differential test, of the kind SPEC section 31 asks for, against the
// one package tool this host has.
//
// The Debian and RPM halves of that requirement need a Debian or a RHEL
// host and are still outstanding. This half is real: every pair below is
// put to `pkg version -t` and to halite, and the two must agree. It runs
// only where pkg(8) exists, and says so when it does not, rather than
// passing quietly on a host that could not have tested anything.
func TestFreeBSDVersionsAgreeWithPkg(t *testing.T) {
	c := newCtx(false)
	// A real runner: the whole point is to reach the tool, and newCtx
	// hands out a recorder so that module tests never do.
	c.Runner = &exec.OSRunner{}
	if c.Which("pkg") == "" {
		t.Skip("pkg(8) is not on this host; the FreeBSD differential needs it")
	}

	pairs := [][2]string{
		{"1.0", "1.1"}, {"1.0", "1.0"}, {"1.1", "1.0"},
		{"1.0_1", "1.0"}, {"1.0", "1.0_1"},
		{"1.0.r1", "1.0"}, {"1.0,1", "1.0"},
		{"2.34", "2.4"}, {"1.0a", "1.0"}, {"1.0", "1.0a"},
		{"0.9.8_1", "0.9.8"}, {"20240101", "20231231"},
		{"1.2.3", "1.2.3_2"}, {"3.11.9", "3.12.0"},
	}
	for _, p := range pairs {
		got, err := compareFreeBSD(c, p[0], p[1])
		if err != nil {
			t.Fatalf("%v: %v", p, err)
		}
		// The tool's own answer, read independently of the code path
		// under test.
		res, err := c.Run(execCommandFor("pkg", "version", "-t", p[0], p[1]))
		if err != nil {
			t.Fatalf("%v: %v", p, err)
		}
		want := map[string]int64{"<": -1, "=": 0, ">": 1}[trimSpaceOf(res.Stdout)]
		if got != want {
			t.Errorf("pkg version -t %q %q said %q, halite said %v", p[0], p[1], trimSpaceOf(res.Stdout), got)
		}
	}
}

func execCommandFor(argv ...string) exec.Command {
	return exec.Command{Argv: argv, IgnoreExitCode: true}
}

func trimSpaceOf(s string) string { return strings.TrimSpace(s) }
