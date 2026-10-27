package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
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
	// A tree of example states, which ships with its directories.
	write(filepath.Join(root, "ex", "tree", "states", "top.sls"), "base: {}\n", 0o600)
	write(filepath.Join(root, "man", "halite-node.8"), ".Dd\n", 0o600)
	write(filepath.Join(root, "man", "halite.7"), ".Dd\n", 0o600)
	write(filepath.Join(root, "hb", "index.html"), "<html>\n", 0o600)
	write(filepath.Join(root, "rcd", "halite_hub"), "#!/bin/sh\n", 0o600)
	write(filepath.Join(root, "sd", "halite-hub.service"), "[Unit]\n", 0o600)
	write(filepath.Join(root, "LICENSE"), "BSD 2-Clause License\n", 0o600)
	write(filepath.Join(root, "evidence.md"), "# What has been demonstrated\n", 0o600)
	return options{
		dist: dist, version: "0.13.0", epoch: "1790360170",
		targets: "linux/amd64 windows/arm64", binaries: "halite-node halite-hub",
		examples: filepath.Join(root, "ex"), man: filepath.Join(root, "man"),
		handbook: filepath.Join(root, "hb"), rcd: filepath.Join(root, "rcd"), systemd: filepath.Join(root, "sd"),
		license:  filepath.Join(root, "LICENSE"),
		evidence: filepath.Join(root, "evidence.md"),
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
		top, top + "EVIDENCE.md", top + "LICENSE",
		top + "bin/", top + "bin/halite-hub", top + "bin/halite-node",
		top + "examples/", top + "examples/hub.yaml", top + "examples/node.yaml",
		top + "examples/tree/", top + "examples/tree/states/", top + "examples/tree/states/top.sls",
		top + "handbook/", top + "handbook/index.html",
		top + "man/", top + "man/man7/", top + "man/man7/halite.7", top + "man/man8/", top + "man/man8/halite-node.8",
		top + "service/", top + "service/rc.d/", top + "service/rc.d/halite_hub",
		top + "service/systemd/", top + "service/systemd/halite-hub.service",
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
	for _, n := range []string{"EVIDENCE.md", "LICENSE", "bin/halite-node.exe", "bin/halite-hub.exe", "examples/node.yaml", "man/man8/halite-node.8"} {
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
	// A tag's `v` in the version would name every archive differently
	// from what the release notes and the README fetch.
	o = fixture(t)
	o.version = "v0.1.0"
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "the tag's v") {
		t.Errorf("a version with a tag's v: %v", err)
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
	// The evidence report is required for the same reason the licence is:
	// an archive without it is not the one SPEC 27.2 describes, and it is
	// the only thing in the tarball that says what was demonstrated.
	o = fixture(t)
	o.evidence = filepath.Join(t.TempDir(), "evidence.md")
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "gendocs") {
		t.Errorf("a missing evidence report: %v", err)
	}
}

// The FIPS set (SPEC 27.4) is a separate archive whose bin/ holds the -fips
// binaries, and the ordinary archive holds none: a -fips binary beside its
// ordinary twin is one cp from the wrong one being installed, which is what
// publishing them separately is for. Each is also checked for the files
// every archive carries.
func TestFipsBinariesShipInTheirOwnArchive(t *testing.T) {
	o := fixture(t)
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(o.dist, name), []byte("FIPS ELF "+name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write("halite-node-fips-linux-amd64")
	write("halite-hub-fips-linux-amd64")
	o.fipsTargets = "linux/amd64"

	made, err := run(o)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(made, " ") != "halite-0.13.0-linux-amd64.tar.gz halite-0.13.0-windows-arm64.zip halite-0.13.0-linux-amd64-fips.tar.gz" {
		t.Fatalf("made %v", made)
	}
	names := func(archive string) string {
		f, err := os.Open(filepath.Join(o.dist, archive))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		var out []string
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, h.Name)
		}
		return strings.Join(out, "\n")
	}
	fips := names("halite-0.13.0-linux-amd64-fips.tar.gz")
	top := "halite-0.13.0-linux-amd64-fips/"
	for _, want := range []string{top + "bin/halite-node-fips", top + "bin/halite-hub-fips", top + "LICENSE", top + "EVIDENCE.md", top + "service/rc.d/halite_hub"} {
		if !strings.Contains(fips, want) {
			t.Errorf("the FIPS archive lacks %s:\n%s", want, fips)
		}
	}
	if strings.Contains(fips, "bin/halite-node\n") || strings.HasSuffix(fips, "bin/halite-node") {
		t.Errorf("the FIPS archive carries the ordinary binary:\n%s", fips)
	}
	if plain := names("halite-0.13.0-linux-amd64.tar.gz"); strings.Contains(plain, "-fips") && strings.Contains(plain, "bin/halite-node-fips") {
		t.Errorf("the ordinary archive carries a FIPS binary:\n%s", plain)
	}

	// A FIPS target whose binaries were never built is an error naming the
	// step, not an archive quietly without them.
	o.fipsTargets = "linux/arm64"
	if _, err := run(o); err == nil || !strings.Contains(err.Error(), "make fips-cross") {
		t.Errorf("a missing -fips binary: %v", err)
	}
}

// Chapter 30 of the handbook shows what `ls` prints inside an unpacked
// archive, and tells the reader to copy from service/. It said so for an
// archive that did not exist yet: the first published release held only
// bin/, examples/ and man/, and the chapter was written against what the
// Makefile was going to do. The listing is checked here against what this
// tool actually puts at the top of an archive.
func TestTheInstallChapterListsWhatTheArchiveHolds(t *testing.T) {
	src, err := os.ReadFile("../../docs/handbook/30-install-hub.md")
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	lines := strings.Split(string(src), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "EVIDENCE.md ") && i > 0 && lines[i-1] == "```" {
			for _, f := range strings.Fields(l) {
				listed = append(listed, strings.TrimSuffix(f, "/"))
			}
		}
	}
	if len(listed) == 0 {
		t.Fatal("chapter 30 no longer shows the archive's listing; update this test with it")
	}

	o := fixture(t)
	if _, err := run(o); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(o.dist, "halite-0.13.0-linux-amd64.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, _ := gzip.NewReader(f)
	tr := tar.NewReader(gz)
	top := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(strings.TrimSuffix(h.Name, "/"), "/")
		if len(parts) > 1 {
			top[parts[1]] = true
		}
	}
	var have []string
	for n := range top {
		have = append(have, n)
	}
	sort.Strings(have)
	sort.Strings(listed)
	if strings.Join(have, " ") != strings.Join(listed, " ") {
		t.Errorf("chapter 30 lists %v; an archive holds %v", listed, have)
	}
}

// A file under contrib that git does not track -- a scratch file from a
// local build -- was packaged with the rest, into an archive that is then
// signed. Where the directories are in a repository, only tracked files go.
func TestAnUntrackedFileIsNotPackaged(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	o := fixture(t)
	root := filepath.Dir(o.dist)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "ex", "man", "rcd", "sd")
	scratch := filepath.Join(root, "rcd", "halite_hub.orig")
	if err := os.WriteFile(scratch, []byte("scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := sharedEntries(o)
	if err != nil {
		t.Fatal(err)
	}
	var sawTracked bool
	for _, e := range entries {
		if strings.HasSuffix(e.path, "halite_hub.orig") {
			t.Errorf("the untracked file was packaged as %s", e.path)
		}
		if strings.HasSuffix(e.path, "service/rc.d/halite_hub") {
			sawTracked = true
		}
	}
	if !sawTracked {
		t.Error("the tracked rc script is missing")
	}
}
