package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// dpkgFixture is a real dpkg's answers, captured rather than reasoned
// about. See testdata/dpkg-1.22.22-trixie.tsv for where it came from.
const dpkgFixture = "dpkg-1.22.22-trixie.tsv"

// TestDebianOrderingMatchesACapturedDpkg is the half of SPEC 31's
// version-comparison differential that runs on every machine.
//
// `TestLiveDpkgVersionCmpAgreesWithDpkg` is the differential proper and
// it needs a Debian host, so until 5.633 it had never run: SPEC 31's row
// said as much, and `TestCompareDebian` was left holding a table written
// from dpkg's test suite and the policy manual -- a table that agrees
// with the documentation by construction, and with dpkg only if the
// documentation is right.
//
// This closes that on any machine, by holding the implementation to what
// a real dpkg actually answered. A captured oracle is weaker than the
// live one in exactly one way, and the comment in the fixture says which:
// it is one dpkg, on one day. It is stronger in the way that matters most
// here, because it runs in the ordinary suite on every platform instead
// of only where dpkg exists.
func TestDebianOrderingMatchesACapturedDpkg(t *testing.T) {
	captured := readDpkgFixture(t)

	// Both directions. A vector in the table with no captured answer has
	// never been put to dpkg, and that is the gap this entry closed; a
	// captured answer for a vector no longer in the table is a line
	// nothing reads, which rots.
	byPair := map[string]string{}
	for _, c := range captured {
		byPair[c[0]+"\x00"+c[2]] = c[1]
	}
	inTable := map[string]bool{}
	for _, v := range debianVectors {
		inTable[v[0]+"\x00"+v[2]] = true
		if _, ok := byPair[v[0]+"\x00"+v[2]]; !ok {
			t.Errorf("%q against %q is in debianVectors and no real dpkg has answered it; "+
				"capture it the way %s was captured, or the offline table is testing the "+
				"policy manual again", v[0], v[2], dpkgFixture)
		}
	}
	for _, c := range captured {
		if !inTable[c[0]+"\x00"+c[2]] {
			t.Errorf("%s holds dpkg's answer for %q against %q and debianVectors no longer "+
				"has that pair", dpkgFixture, c[0], c[2])
		}
	}

	r := New()
	c := &exec.Context{}
	for _, row := range captured {
		a, want, b := row[0], row[1], row[2]
		expect := map[string]int{"<": -1, "=": 0, ">": 1}[want]

		if got := CompareDebian(a, b); got != expect {
			t.Errorf("CompareDebian(%q, %q) = %d; dpkg 1.22.22 answered %q", a, b, got, want)
		}
		// Through the module as well, because an ordering nothing can
		// reach is not an ordering: `pkg.version_cmp` with an explicit
		// scheme is what a state calls.
		got, err := r.Exec.Call(c, "pkg.version_cmp",
			value.MapOf("pkg1", a, "pkg2", b, "scheme", "debian"))
		if err != nil {
			t.Fatalf("pkg.version_cmp(%q, %q): %v", a, b, err)
		}
		if got != int64(expect) {
			t.Errorf("pkg.version_cmp(%q, %q) = %v; dpkg 1.22.22 answered %q", a, b, got, want)
		}
	}
	t.Logf("%d pairs against dpkg 1.22.22's captured answers", len(captured))
}

// readDpkgFixture reads the captured answers, and fails rather than
// returning nothing: an empty fixture would make every check above pass
// by having nothing to check.
func readDpkgFixture(t *testing.T) [][3]string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", dpkgFixture))
	if err != nil {
		t.Fatalf("reading the captured dpkg answers: %v", err)
	}
	var rows [][3]string
	for n, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			t.Fatalf("%s:%d has %d tab-separated fields, want 3: %q",
				dpkgFixture, n+1, len(fields), line)
		}
		switch fields[1] {
		case "<", "=", ">":
		default:
			t.Fatalf("%s:%d: %q is not one of <, = or >", dpkgFixture, n+1, fields[1])
		}
		rows = append(rows, [3]string{fields[0], fields[1], fields[2]})
	}
	if len(rows) == 0 {
		t.Fatalf("%s holds no answers; this check is reading nothing", dpkgFixture)
	}
	return rows
}
