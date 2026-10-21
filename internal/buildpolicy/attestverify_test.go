package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every instruction to verify a release's provenance names the signer
// workflow and the source ref.
//
// `gh attestation verify <file> --repo edlitmus/halite` accepts an
// attestation made by any workflow, from any ref, in the repository, and
// release.yml can be dispatched from any branch and attests what it
// builds. So the bare command, which the release notes, the README and
// two handbook chapters all gave, accepted provenance for a build nobody
// reviewed. DIVERGENCE 5.425.
//
// The changelog and the ledger quote history and are not instructions,
// so they are not read.
func TestEveryProvenanceCheckNamesTheWorkflowAndRef(t *testing.T) {
	root := repoRoot(t)
	var files []string
	for _, pat := range []string{"README.md", "docs/*.md", "docs/handbook/*.md", ".github/workflows/*.yml"} {
		m, err := filepath.Glob(filepath.Join(root, pat))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	command := regexp.MustCompile(`^\s*(?:#\s*)?gh attestation verify `)
	found := 0
	for _, path := range files {
		rel, _ := filepath.Rel(root, path)
		if rel == filepath.Join("docs", "DIVERGENCE.md") {
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if !command.MatchString(line) {
				continue
			}
			found++
			// The command, with its continuation lines.
			cmd := line
			for j := i; j+1 < len(lines) && strings.HasSuffix(strings.TrimSpace(lines[j]), `\`); j++ {
				cmd += "\n" + lines[j+1]
			}
			for _, flag := range []string{"--signer-workflow ", "--source-ref "} {
				if !strings.Contains(cmd, flag) {
					t.Errorf("%s:%d tells somebody to verify provenance without %s, which "+
						"accepts an attestation from any workflow or branch of the repository:\n%s",
						rel, i+1, strings.TrimSpace(flag), cmd)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("no `gh attestation verify` command was found; this guard has stopped checking anything")
	}
	t.Logf("%d provenance checks name the signer workflow and the source ref", found)
}
