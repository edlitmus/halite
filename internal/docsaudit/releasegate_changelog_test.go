//go:build releasegate

package docsaudit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleaseGateChangelogNamesTheTag is the changelog half of the
// release gate (`make release-gate`): when HEAD is a release tag, the
// changelog at that commit must say what the tag is. See changelogFitsTag.
//
// The tag is read from the workflow's ref when there is one, and from git
// otherwise. With neither, the run is not a release (a manual dispatch,
// or a developer's tree), and the test skips and says so rather than
// passing: a gate that cannot tell what it is gating has not run.
func TestReleaseGateChangelogNamesTheTag(t *testing.T) {
	tag := os.Getenv("GITHUB_REF_NAME")
	if !strings.HasPrefix(tag, "v") {
		out, err := exec.Command("git", "describe", "--exact-match", "--tags", "--match", "v*", "HEAD").Output()
		if err != nil {
			t.Skip("HEAD is not a v* tag and GITHUB_REF_NAME names none: not a release, so no changelog to hold to one")
		}
		tag = strings.TrimSpace(string(out))
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := changelogFitsTag(string(raw), strings.TrimPrefix(tag, "v")); err != nil {
		t.Fatalf("%s: %v", tag, err)
	}
}
