package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/extension"
)

var (
	toolOnce sync.Once
	toolPath string
	toolErr  error
)

// extbundleBinary builds this command once, so the tests drive it the
// way an author does -- flags in, manifest.json out -- rather than
// calling into it. The defect these tests were written for lived in
// main() itself, which a test of a helper would have walked past.
func extbundleBinary(t *testing.T) string {
	t.Helper()
	toolOnce.Do(func() {
		dir, err := os.MkdirTemp("", "halite-extbundle-*")
		if err != nil {
			toolErr = err
			return
		}
		toolPath = filepath.Join(dir, "extbundle")
		if runtime.GOOS == "windows" {
			toolPath += ".exe"
		}
		build := exec.Command("go", "build", "-o", toolPath, ".")
		build.Stderr = os.Stderr
		toolErr = build.Run()
	})
	if toolErr != nil {
		t.Fatalf("building extbundle: %v", toolErr)
	}
	return toolPath
}

// crossBuild compiles the bridge's test extension for goos/goarch into
// dir/name. A real binary from the real toolchain, not a hand-made
// header: the thing under test is whether extbundle reads what `go
// build` actually writes, and a header written from the ELF
// specification would only test the specification.
func crossBuild(t *testing.T, dir, name, goos, goarch string) {
	t.Helper()
	build := exec.Command("go", "build", "-o", filepath.Join(dir, name),
		"../../internal/bridge/testdata/echoext")
	build.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cross-building for %s/%s: %v\n%s", goos, goarch, err, out)
	}
}

// runExtbundle runs the command over dir and returns its combined
// output and whether it succeeded.
func runExtbundle(t *testing.T, dir string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{
		"-dir", dir, "-name", "probe", "-exe", "ext",
		"-key", filepath.Join(t.TempDir(), "signing.key"),
	}, extra...)
	out, err := exec.Command(extbundleBinary(t), args...).CombinedOutput()
	return string(out), err
}

func readManifest(t *testing.T, dir string) *extension.Manifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, extension.ManifestName))
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}
	m, err := extension.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parsing the manifest: %v", err)
	}
	return m
}

// TestBundlesUnderTheBinarysPlatform is the defect: the manifest used to
// record the executable under runtime.GOOS/GOARCH -- the machine running
// extbundle -- so a linux/amd64 binary bundled on a Mac was labelled
// darwin/arm64 and every Linux host refused it. Several targets, so that
// whichever machine runs this, most are not its own platform; FreeBSD
// twice, because that is most of the estate and its ELF is told apart
// from Linux's by a different route; and one each of the Mach-O and PE
// readers.
func TestBundlesUnderTheBinarysPlatform(t *testing.T) {
	for _, target := range [][2]string{
		{"linux", "amd64"}, {"freebsd", "arm64"}, {"freebsd", "amd64"},
		{"linux", "arm64"}, {"darwin", "amd64"}, {"windows", "amd64"},
	} {
		goos, goarch := target[0], target[1]
		t.Run(goos+"_"+goarch, func(t *testing.T) {
			// The manifest refuses a windows executable Windows could
			// not start by name -- a check that, before this, only ran
			// for whoever bundled on Windows.
			exe := "ext"
			if goos == "windows" {
				exe = "ext.exe"
			}
			dir := t.TempDir()
			crossBuild(t, dir, exe, goos, goarch)
			if out, err := runExtbundle(t, dir, "-exe", exe); err != nil {
				t.Fatalf("extbundle: %v\n%s", err, out)
			}
			want := extension.Platform(goos, goarch)
			got := readManifest(t, dir).Executables
			if len(got) != 1 || got[want] != exe {
				t.Fatalf("executables = %v, want {%s: ext}", got, want)
			}
		})
	}
}

// refused runs extbundle expecting it to fail with want in its output,
// and checks that it left no manifest behind -- a refusal that still
// wrote one would be a refusal in the log and a bundle on disk.
func refused(t *testing.T, dir, want string, extra ...string) {
	t.Helper()
	os.Remove(filepath.Join(dir, extension.ManifestName))
	out, err := runExtbundle(t, dir, extra...)
	if err == nil {
		t.Fatalf("extbundle accepted it:\n%s", out)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("output does not say %q:\n%s", want, out)
	}
	if _, err := os.Stat(filepath.Join(dir, extension.ManifestName)); err == nil {
		t.Fatalf("a refused bundle still wrote %s", extension.ManifestName)
	}
}

// TestPlatformFlagMustAgree: -platform names what the author believes,
// and it is checked against the file rather than believed. The Linux
// case matters most, because its ELF header says nothing about the
// operating system (OSABI 0) and only Go's build information catches
// the author who says freebsd about a Linux binary.
func TestPlatformFlagMustAgree(t *testing.T) {
	dir := t.TempDir()
	crossBuild(t, dir, "ext", "linux", "amd64")

	refused(t, dir, "which is built for linux/amd64", "-platform", "freebsd/amd64")
	refused(t, dir, "header is for amd64", "-platform", "linux/arm64")
	refused(t, dir, "is not goos/goarch", "-platform", "linux")

	if out, err := runExtbundle(t, dir, "-platform", "linux/amd64"); err != nil {
		t.Fatalf("an agreeing -platform was refused: %v\n%s", err, out)
	}
	if got := readManifest(t, dir).Executables; got["linux/amd64"] != "ext" || len(got) != 1 {
		t.Fatalf("executables = %v", got)
	}

	fdir := t.TempDir()
	crossBuild(t, fdir, "ext", "freebsd", "amd64")
	refused(t, fdir, "which is built for freebsd/amd64", "-platform", "linux/amd64")
}

// withoutBuildInfo stands in for an ELF executable not built by Go: a
// real Go binary with the magic that opens its build information
// overwritten, so debug/buildinfo cannot find it and the header is all
// that is left. The header itself is untouched, which is the point --
// it is what `go build` wrote, not something composed from the ELF
// specification. What it does not stand in for is a C binary's notes
// and sections, which extbundle does not read.
func withoutBuildInfo(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	magic := "\xff Go buildinf:"
	if !strings.Contains(string(raw), magic) {
		t.Fatalf("%s has no build information to remove", path)
	}
	raw = []byte(strings.ReplaceAll(string(raw), magic, "\x00 no buildinf:"))
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestNonGoELF: without Go's build information, a FreeBSD ELF still
// names its operating system in OSABI and is bundled as freebsd; a
// Linux one does not, and is refused until the author names it, rather
// than being called linux because that is the likeliest answer.
func TestNonGoELF(t *testing.T) {
	ldir := t.TempDir()
	crossBuild(t, ldir, "ext", "linux", "amd64")
	withoutBuildInfo(t, filepath.Join(ldir, "ext"))
	refused(t, ldir, "cannot tell which operating system")
	refused(t, ldir, "header is for amd64", "-platform", "linux/arm64")
	if out, err := runExtbundle(t, ldir, "-platform", "linux/amd64"); err != nil {
		t.Fatalf("-platform linux/amd64 was refused: %v\n%s", err, out)
	}
	if got := readManifest(t, ldir).Executables; got["linux/amd64"] != "ext" {
		t.Fatalf("executables = %v", got)
	}

	fdir := t.TempDir()
	crossBuild(t, fdir, "ext", "freebsd", "arm64")
	withoutBuildInfo(t, filepath.Join(fdir, "ext"))
	refused(t, fdir, "which is built for freebsd/arm64", "-platform", "linux/arm64")
	if out, err := runExtbundle(t, fdir); err != nil {
		t.Fatalf("extbundle: %v\n%s", err, out)
	}
	if got := readManifest(t, fdir).Executables; got["freebsd/arm64"] != "ext" {
		t.Fatalf("executables = %v", got)
	}
}

// TestScriptsAndUnknownFiles: a script has no header, so the author
// names its platform and that is all there is to check; a file that is
// neither a script nor an executable this reads is refused even with
// -platform, because there is nothing to check the flag against and the
// likeliest explanation is the wrong -exe.
func TestScriptsAndUnknownFiles(t *testing.T) {
	sdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sdir, "ext"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	refused(t, sdir, "is a script")
	if out, err := runExtbundle(t, sdir, "-platform", "freebsd/amd64"); err != nil {
		t.Fatalf("a script with -platform was refused: %v\n%s", err, out)
	}
	if got := readManifest(t, sdir).Executables; got["freebsd/amd64"] != "ext" {
		t.Fatalf("executables = %v", got)
	}

	gdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gdir, "ext"), []byte("not an executable at all\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	refused(t, gdir, "not an ELF, Mach-O or PE executable", "-platform", "linux/amd64")
}
