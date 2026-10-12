package buildpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `make dist` sums everything in dist/ with `sha256sum *` and sorts the
// result into the manifest. A pipeline's stages start together, so when
// the last stage is `> SHA256SUMS` the shell can create that file before
// the first stage's `*` is expanded, and the manifest then lists itself
// with the hash of an empty file. Measured on this host at about 1 run in
// 100 (2 of 200 in a loop of the bare pipeline, 0 of 1000 after the
// change), which is the kind of failure a release workflow that compares
// two builders' manifests reports as "the builds differ" one time in a
// hundred and never reproduces for the person who looks. DIVERGENCE 5.281.
//
// The recipe writes a dot file, which `*` does not match, and renames it.
// This holds the Makefile to that shape. It reads text rather than running
// the recipe because the race is the point: a run that passes proves
// nothing about a failure that happens once in a hundred.
func TestTheManifestIsNotWrittenWhereItsOwnGlobCanSeeIt(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	mk := string(src)
	for _, line := range strings.Split(mk, "\n") {
		if strings.Contains(line, "sort >") && strings.Contains(line, "SHA256SUMS") &&
			!strings.Contains(line, "> .SHA256SUMS.tmp") {
			t.Errorf("the manifest pipeline redirects into %q, a name its own glob can match:\n%s", "SHA256SUMS", line)
		}
		if strings.Contains(line, "sort > SHA256SUMS") {
			t.Errorf("the manifest is written straight to SHA256SUMS:\n%s", line)
		}
	}
	if !strings.Contains(mk, "mv .SHA256SUMS.tmp SHA256SUMS") {
		t.Error("the Makefile never renames .SHA256SUMS.tmp to SHA256SUMS")
	}
}
