package buildpolicy

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every container image this repository builds from is pinned by digest.
//
// They were pinned by tag (issue 321, build section): `golang:1.27.3-bookworm`
// names whatever the registry says it is on the day, so the fleet and
// saltdiff images CI drives as root could change under an unchanged
// commit. The tag is kept beside the digest because it says what the
// digest is, and the toolchain checks read it. Each digest is the
// multi-arch index, so the same line serves amd64 and arm64.
//
// Moving one is deliberate: fetch the index digest for the new tag and
// replace both together.
func TestContainerBasesArePinnedByDigest(t *testing.T) {
	root := repoRoot(t)
	digest := regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

	var dockerfiles []string
	err := filepath.WalkDir(filepath.Join(root, "contrib"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), "Dockerfile") {
			dockerfiles = append(dockerfiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dockerfiles) < 6 {
		t.Fatalf("found %d Dockerfiles under contrib, expected at least 6; this guard is looking in the wrong place", len(dockerfiles))
	}

	refs := 0
	for _, path := range dockerfiles {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		stages := map[string]bool{}
		for _, line := range strings.Split(string(body), "\n") {
			fields := strings.Fields(line)
			switch {
			case len(fields) >= 2 && fields[0] == "FROM":
				image := fields[1]
				if len(fields) >= 4 && strings.EqualFold(fields[2], "AS") {
					stages[fields[3]] = true
				}
				// A build argument is checked where its value is
				// given; an earlier stage is not an image.
				if strings.HasPrefix(image, "$") || stages[image] {
					continue
				}
				refs++
				if !digest.MatchString(image) {
					t.Errorf("%s: FROM %s is not pinned by digest", rel, image)
				}
			case len(fields) == 2 && fields[0] == "ARG" && strings.HasPrefix(fields[1], "BASE="):
				refs++
				if !digest.MatchString(fields[1]) {
					t.Errorf("%s: %s is not pinned by digest", rel, fields[1])
				}
			}
		}
	}

	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`--build-arg BASE=(\S+)`).FindAllStringSubmatch(string(mk), -1) {
		refs++
		if !digest.MatchString(m[1]) {
			t.Errorf("Makefile: --build-arg BASE=%s is not pinned by digest", m[1])
		}
	}
	if refs < 10 {
		t.Errorf("checked %d image references, expected at least 10; this guard has stopped seeing them", refs)
	}
}
