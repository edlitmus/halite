package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The release workflow's provenance is only worth what its preconditions
// are (SPEC 4.3, decided 2026-09-25). The attestation is meant to say
// "two builders produced these bytes, and the gate let them out", and
// each clause of that is a line of YAML somebody could move:
//
//   - `needs` both the gate and the compare job, and an `if` that
//     requires both to have *succeeded* -- `always()` or a manual-run
//     escape here would attest a build nobody reproduced or a release the
//     gate refused, which is the build job's latitude and not this one's;
//   - the subject is the agreed manifest (`subject-checksums`), so what is
//     attested is what was compared;
//   - `id-token: write` and `attestations: write` exist in this job and no
//     other, so the one job that can sign is the one whose inputs are
//     checked.
//
// Held as text, because the property is about the file a reviewer reads.
func TestTheReleaseAttestsOnlyWhatTwoBuildersAgreed(t *testing.T) {
	root := repoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)

	jobs := splitJobs(t, wf)
	attest, ok := jobs["attest"]
	if !ok {
		t.Fatal("release.yml has no attest job")
	}
	if !regexp.MustCompile(`needs:\s*\[\s*gate\s*,\s*compare\s*\]`).MatchString(attest) {
		t.Error("the attest job does not need both gate and compare")
	}
	for _, want := range []string{"needs.gate.result == 'success'", "needs.compare.result == 'success'"} {
		if !strings.Contains(attest, want) {
			t.Errorf("the attest job's condition does not require %s", want)
		}
	}
	if strings.Contains(attest, "always()") || strings.Contains(attest, "workflow_dispatch") {
		t.Error("the attest job has an escape from its preconditions")
	}
	if !strings.Contains(attest, "actions/attest-build-provenance@") || !strings.Contains(attest, "subject-checksums:") {
		t.Error("the attest job does not attest the agreed checksum manifest")
	}
	for name, body := range jobs {
		for _, perm := range []string{"id-token: write", "attestations: write"} {
			has := strings.Contains(body, perm)
			if name == "attest" && !has {
				t.Errorf("the attest job lacks %s", perm)
			}
			if name != "attest" && has {
				t.Errorf("job %s holds %s; only attest may sign", name, perm)
			}
		}
	}
	// Publishing is the one job that writes to the repository, it comes
	// after the attestation, it checks what it publishes against what was
	// compared, and it only creates a release on a tag.
	publish, ok := jobs["publish"]
	if !ok {
		t.Fatal("release.yml has no publish job")
	}
	if !regexp.MustCompile(`needs:\s*attest\b`).MatchString(publish) ||
		!strings.Contains(publish, "needs.attest.result == 'success'") {
		t.Error("the publish job does not wait for a successful attest")
	}
	for _, want := range []string{"cmp dist/SHA256SUMS digests-ubuntu-24.04.txt", "sha256sum -c", "startsWith(github.ref, 'refs/tags/v')", "gh release create"} {
		if !strings.Contains(publish, want) {
			t.Errorf("the publish job has no %q", want)
		}
	}
	for name, body := range jobs {
		has := strings.Contains(body, "contents: write")
		if name == "publish" && !has {
			t.Error("the publish job lacks contents: write")
		}
		if name != "publish" && has {
			t.Errorf("job %s holds contents: write; only publish may", name)
		}
	}

	top := wf[:strings.Index(wf, "\njobs:")]
	if strings.Contains(top, "id-token: write") || strings.Contains(top, "attestations: write") || strings.Contains(top, "contents: write") {
		t.Error("release.yml grants signing permissions to every job at the top level")
	}
}

// splitJobs cuts a workflow into its jobs by their two-space-indented keys.
func splitJobs(t *testing.T, wf string) map[string]string {
	t.Helper()
	i := strings.Index(wf, "\njobs:\n")
	if i < 0 {
		t.Fatal("no jobs: block")
	}
	body := wf[i+len("\njobs:\n"):]
	key := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9_-]*):\s*$`)
	locs := key.FindAllStringSubmatchIndex(body, -1)
	out := map[string]string{}
	for n, loc := range locs {
		end := len(body)
		if n+1 < len(locs) {
			end = locs[n+1][0]
		}
		out[body[loc[2]:loc[3]]] = body[loc[0]:end]
	}
	return out
}
