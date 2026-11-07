package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The signature check before a release is made public and the one after
// are the same script.
//
// `make release-publish` checks a draft's signatures and then publishes
// it. GitHub's Publish button does the second without the first, and
// nothing GitHub offers stops an account with write access from pressing
// it. The owner's decision was to detect that rather than try to prevent
// it: verify-release.yml runs on `release: published` and checks the
// signatures again (issue 311, DIVERGENCE 5.278). That is only a check of
// the gate if it asks what the gate asks. Two copies of "download it and
// run relsign" would be two things that could drift, and the one that
// runs after the fact is the one nobody watches, so both callers go
// through tools/verify-signatures.sh and this holds them to it.
func TestTheSignatureCheckBeforeAndAfterPublishIsOneScript(t *testing.T) {
	root := repoRoot(t)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	script := read("tools/verify-signatures.sh")
	if !regexp.MustCompile(`(?m)^[^#]*relsign verify -published `).MatchString(script) {
		t.Error("tools/verify-signatures.sh does not run `relsign verify -published`; " +
			"it is the check both make release-publish and verify-release.yml rely on")
	}

	// The release-publish recipe: its target line to the next blank line.
	mk := read("Makefile")
	i := strings.Index(mk, "\nrelease-publish:")
	if i < 0 {
		t.Fatal("the Makefile has no release-publish target")
	}
	recipe := mk[i+1:]
	if j := strings.Index(recipe, "\n\n"); j >= 0 {
		recipe = recipe[:j]
	}
	if !strings.Contains(recipe, "tools/verify-signatures.sh") {
		t.Errorf("make release-publish does not run tools/verify-signatures.sh, so "+
			"verify-release.yml no longer checks what it checks:\n%s", recipe)
	}
	if strings.Contains(recipe, "relsign verify") {
		t.Errorf("make release-publish runs relsign itself; a second copy of the check "+
			"is one that can drift from the script verify-release.yml runs:\n%s", recipe)
	}
	// The draft check and the publish must stay on either side of it.
	verify := strings.Index(recipe, "tools/verify-signatures.sh")
	edit := strings.Index(recipe, "--draft=false")
	if edit < 0 || verify < 0 || edit < verify {
		t.Errorf("make release-publish must verify before `gh release edit --draft=false`:\n%s", recipe)
	}

	wf := read(".github/workflows/verify-release.yml")
	if !regexp.MustCompile(`(?m)^\s*release:\s*\n\s*types:\s*\[\s*published\s*\]`).MatchString(wf) {
		t.Error("verify-release.yml no longer runs on `release: published`, which is the " +
			"only thing that checks a release published from GitHub's release page")
	}
	if !regexp.MustCompile(`(?m)^\s*run:\s*sh tools/verify-signatures\.sh `).MatchString(wf) {
		t.Error("verify-release.yml does not run tools/verify-signatures.sh, so a release " +
			"published without make release-publish is not checked for signatures")
	}
}
