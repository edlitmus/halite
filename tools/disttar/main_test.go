package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// fixture builds a dist/ with stand-in binaries for two platforms, and
// the examples and man directories the archives take their other files
// from, with modes that are deliberately wrong so that the archive's are
// seen to be its own.
func fixture(t *testing.T) options {
	t.Helper()
	root := t.TempDir()
	write := func(p, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	dist := filepath.Join(root, "dist")
	for _, b := range []string{"halite-node", "halite-hub"} {
		write(filepath.Join(dist, b+"-linux-amd64"), "ELF "+b, 0o700)
		write(filepath.Join(dist, b+"-windows-arm64.exe"), "PE "+b, 0o600)
	}
	write(filepath.Join(root, "ex", "node.yaml"), "node: {}\n", 0o600)
	write(filepath.Join(root, "ex", "hub.yaml"), "hub: {}\n", 0o666)
	write(filepath.Join(root, "man", "halite-node.8"), ".Dd\n", 0o600)
	write(filepath.Join(root, "LICENSE"), "BSD 2-Clause License\n", 0o600)
	return options{
		dist: dist, version: "0.13.0", epoch: "1790360170",
		targets: "linux/amd64 windows/arm64", binaries: "halite-node halite-hub",
		examples: filepath.Join(root, "ex"), man: filepath.Join(root, "man"),
		license: filepath.Join(root, "LICENSE"),
	}
}

// Two runs over the same inputs produce the same bytes, which is SPEC
// 4.3's property for the archive and the reason this tool exists. The
// second run is a moment later, so anything that read the clock would
// show here.
func TestArchivesAreByteIdenticalAcrossRuns(t *testing.T) {
	o := fixture(t)
	if _, err := run(o); err != nil {
		t.Fatal(err)
	}
	first := map[string][]byte{}
	for _, n := range []string{"halite-0.13.0-linux-amd64.tar.gz", "halite-0.13.0-windows-arm64.zip"} {
		b, err := os.ReadFile(filepath.Join(o.dist, n))
		if err != nil {
			t.Fatal(err)
		}
		first[n] = b
	}
	time.Sleep(1100 * time.Millisecond)
	// Touch the inputs, so a tool that copied a file's own time or mode
	// would produce something different.
	now := time.Now()
	_ = os.Chtimes(filepath.Join(o.examples, "node.yaml"), now, now)
	if _, err := run(o); err != nil {
		t.Fatal(err)
	}
	for n, want := range first {
		got, err := os.ReadFile(filepath.Join(o.dist, n))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs between two runs over the same inputs", n)
		}
	}
}

// The tarball holds what SPEC 27.2 says, in sorted order, directories
// first, every entry at the epoch, owned by 0:0 with no names, and with
// the archive's modes rather than the checkout's.
func TestTheTarballIsPinned(t *testing.T) {
	o := fixture(t)
	if _, err := run(o); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(o.dist, "halite-0.13.0-linux-amd64.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	if gz.Name != "" || !gz.ModTime.IsZero() {
		t.Errorf("the gzip header carries name %q and time %v", gz.Name, gz.ModTime)
	}
	tr := tar.NewReader(gz)
	want := time.Unix(1790360170, 0).UTC()
	var names []string
	modes := map[string]int64{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		modes[h.Name] = h.Mode
		if !h.ModTime.Equal(want) {
			t.Errorf("%s has time %v, want the epoch %v", h.Name, h.ModTime, want)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s is owned by %d:%d (%q:%q)", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
	}
	top := "halite-0.13.0-linux-amd64/"
	wantNames := []string{
		top, top + "LICENSE", top + "bin/", top + "bin/halite-hub", top + "bin/halite-node",
		top + "examples/", top + "examples/hub.yaml", top + "examples/node.yaml",
		top + "man/", top + "man/man8/", top + "man/man8/halite-node.8",
	}
	if strings.Join(names, "\n") != strings.Join(wantNames, "\n") {
		t.Errorf("entries:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(wantNames, "\n"))
	}
	if !sort.StringsAreSorted(names) {
		t.Error("entries are not in sorted order")
	}
	for n, m := range map[string]int64{
		top + "bin/halite-node": 0o755, top + "examples/hub.yaml": 0o644, top + "LICENSE": 0o644,
		top + "man/man8/halite-node.8": 0o644, top + "bin/": 0o755,
	} {
		if modes[n] != m {
			t.Errorf("%s has mode %o, want %o", n, modes[n], m)
		}
	}
}

// Windows gets a zip, with .exe binaries and the same pinning.
func TestTheWindowsArchiveIsAPinnedZip(t *testing.T) {
	o := fixture(t)
	if _, err := run(o); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(filepath.Join(o.dist, "halite-0.13.0-windows-arm64.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	want := time.Unix(1790360170, 0).UTC()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
		if !f.Modified.Equal(want) {
			t.Errorf("%s has time %v, want %v", f.Name, f.Modified, want)
		}
	}
	joined := strings.Join(names, " ")
	for _, n := range []string{"LICENSE", "bin/halite-node.exe", "bin/halite-hub.exe", "examples/node.yaml", "man/man8/halite-node.8"} {
		if !strings.Contains(joined, "halite-0.13.0-windows-arm64/"+n) {
			t.Errorf("the zip has no %s: %v", n, names)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Error("zip entries are not in sorted order")
	}
}

// A missing epoch is refused, not replaced with the clock, and a missing
// binary names the step that was skipped.
func TestItRefusesWhatWouldMakeAnArchiveWrong(t *testing.T) {
	o := fixture(t)
	for _, e := range []string{"", "0", "yesterday"} {
		o.epoch = e
		if _, err := run(o); err == nil || !strings.Contains(err.Error(), "SOURCE_DATE_EPOCH") {
			t.Errorf("epoch %q: %v", e, err)
		}
	}
	o = fixture(t)
	o.license = filepath.Join(t.TempDir(), "LICENSE")
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "licence") {
		t.Errorf("a missing licence: %v", err)
	}
	o = fixture(t)
	o.binaries += " halite-api"
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "make cross") {
		t.Errorf("a missing binary: %v", err)
	}
}
