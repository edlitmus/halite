package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// buildDist builds one real binary into a dist/ laid out as `make cross`
// and disttar leave it, with a stand-in archive beside it. For Windows,
// cross-compiled from any host, because that is a build that links a
// module besides halite's own (golang.org/x/sys/windows), so the
// comparison below has a dependency to compare.
func buildDist(t *testing.T) (dir, target string) {
	t.Helper()
	dir = t.TempDir()
	goos, goarch := "windows", "amd64"
	ext := ".exe"
	bin := filepath.Join(dir, "halite-node-"+goos+"-"+goarch+ext)
	build := exec.Command("go", "build", "-trimpath", "-o", bin, "../../cmd/halite-node")
	build.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building halite-node: %v", err)
	}
	archive := "halite-0.0.1-" + goos + "-" + goarch + ".tar.gz"
	if goos == "windows" {
		archive = "halite-0.0.1-" + goos + "-" + goarch + ".zip"
	}
	if err := os.WriteFile(filepath.Join(dir, archive), []byte("an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, goos + "/" + goarch
}

// The SBOM lists what `go version -m` says the binary links -- SPEC 4.3's
// "generated from `go version -m` output on the shipped binary" -- and
// nothing it does not, and it is the same bytes on a second run, which
// two release builders depend on.
func TestTheSBOMIsWhatGoVersionMSaysLinked(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	dir, target := buildDist(t)
	o := options{dist: dir, version: "0.0.1", epoch: "1760000000", targets: target, binaries: "halite-node"}
	made, err := run(o)
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 1 {
		t.Fatalf("made %v", made)
	}
	first, _ := os.ReadFile(filepath.Join(dir, made[0]))
	if _, err := run(o); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, made[0]))
	if !bytes.Equal(first, second) {
		t.Error("two runs over the same binaries wrote different documents")
	}

	var doc bom
	if err := json.Unmarshal(first, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.BOMFormat != "CycloneDX" || doc.SpecVersion != "1.5" || doc.Metadata.Timestamp != "2025-10-09T08:53:20Z" {
		t.Errorf("header = %s %s %s", doc.BOMFormat, doc.SpecVersion, doc.Metadata.Timestamp)
	}
	var fromSBOM []string
	for _, c := range doc.Components {
		if c.Type == "library" {
			fromSBOM = append(fromSBOM, c.Name+"@"+c.Version)
		}
	}
	sort.Strings(fromSBOM)

	out, err := exec.Command("go", "version", "-m",
		filepath.Join(dir, "halite-node-"+strings.ReplaceAll(target, "/", "-")+".exe")).Output()
	if err != nil {
		t.Fatal(err)
	}
	var fromGo []string
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) >= 2 && f[0] == "mod":
			// The main module, which the SBOM records at the release's
			// version whatever Go derived for it.
			fromGo = append(fromGo, f[1]+"@v0.0.1")
		case len(f) >= 3 && f[0] == "dep":
			fromGo = append(fromGo, f[1]+"@"+f[2])
		case len(f) >= 3 && f[0] == "=>":
			// A replacement stands for the dep line above it.
			fromGo[len(fromGo)-1] = f[1] + "@" + f[2]
		}
	}
	sort.Strings(fromGo)
	if strings.Join(fromSBOM, " ") != strings.Join(fromGo, " ") {
		t.Errorf("the SBOM lists %v; go version -m says %v", fromSBOM, fromGo)
	}
	if len(fromGo) < 2 {
		t.Errorf("go version -m listed %v; the test is comparing no dependency", fromGo)
	}
}

// No epoch, no document: a timestamp from the clock would differ between
// the two builders and stop the release.
func TestTheSBOMRefusesTheClock(t *testing.T) {
	if _, err := run(options{version: "0.0.1", targets: "linux/amd64", binaries: "halite-node"}); err == nil ||
		!strings.Contains(err.Error(), "SOURCE_DATE_EPOCH") {
		t.Errorf("no epoch gave %v", err)
	}
}
