package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// **The govulncheck CI runs is the one `make vuln` runs** (issue 321,
// M-03). The Makefile ran @latest and no workflow ran it at all, so the
// scan that had cleared the advisories was never repeated, and when it was
// run it was a different tool each day. Both now name a version, and it
// is the same version; @latest is refused in either.
func TestGovulncheckIsPinnedAndTheSameEverywhere(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	ci, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	pinned := regexp.MustCompile(`(?m)^GOVULNCHECK_VERSION\s*=\s*(v[0-9][0-9.]*)\s*$`).FindSubmatch(makefile)
	if pinned == nil {
		t.Fatal("the Makefile pins no GOVULNCHECK_VERSION")
	}
	runs := regexp.MustCompile(`govulncheck@([^\s]+)`).FindAllSubmatch(ci, -1)
	if len(runs) == 0 {
		t.Fatal("no workflow in ci.yml runs govulncheck")
	}
	for _, r := range runs {
		if string(r[1]) != string(pinned[1]) {
			t.Errorf("ci.yml runs govulncheck@%s and the Makefile pins %s", r[1], pinned[1])
		}
	}
	if regexp.MustCompile(`govulncheck@latest`).Match(makefile) {
		t.Error("the Makefile still runs govulncheck@latest")
	}
}
