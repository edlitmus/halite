package builtin

import (
	"archive/tar"
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// DIVERGENCE 5.300. The containment check on an archive entry was
// lexical: it looked at the names, and an archive is a sequence of
// operations on a disk. These archives are built entry by entry, in
// order, because the order is the attack -- makeTar takes a map and
// cannot express one.

type tarEntry struct {
	name, link, body string
	typ              byte
}

func dirEntry(name string) tarEntry        { return tarEntry{name: name, typ: tar.TypeDir} }
func linkEntry(name, to string) tarEntry   { return tarEntry{name: name, link: to, typ: tar.TypeSymlink} }
func fileEntry(name, body string) tarEntry { return tarEntry{name: name, body: body, typ: tar.TypeReg} }

func makeOrderedTar(t *testing.T, path string, entries ...tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Typeflag: e.typ, Linkname: e.link, Mode: 0o644}
		if e.typ == tar.TypeDir {
			h.Mode = 0o755
		}
		if e.typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
}

// nestedDest puts the destination two levels down inside a temporary
// directory, so that "two levels above the destination" is still a
// directory this test owns and can look in.
func nestedDest(t *testing.T) (base, dest string) {
	base = t.TempDir()
	dest = filepath.Join(base, "x", "y", "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	return base, dest
}

// The exact archive from the audit (H-03): every name is lexically inside
// the destination, and `L/escaped.txt` landed two levels above it.
func TestArchiveChainedSymlinksCannotWriteOutside(t *testing.T) {
	base, dest := nestedDest(t)
	archive := filepath.Join(base, "chain.tar")
	makeOrderedTar(t, archive,
		dirEntry("a/"),
		linkEntry("a/b", ".."),
		linkEntry("a/b/L", "../.."),
		fileEntry("L/escaped.txt", "escaped\n"),
	)

	_, err := extractArchive(archive, dest, false, false)
	for _, p := range []string{
		filepath.Join(base, "x", "escaped.txt"),
		filepath.Join(base, "x", "y", "escaped.txt"),
		filepath.Join(base, "escaped.txt"),
	} {
		if _, statErr := os.Lstat(p); statErr == nil {
			t.Fatalf("%s was written outside the destination (err=%v)", p, err)
		}
	}
	if err == nil {
		t.Fatal("the chained-symlink archive extracted without complaint")
	}
	// And the link that pointed out is not left behind for whatever runs
	// from the destination next.
	if _, statErr := os.Lstat(filepath.Join(dest, "L")); statErr == nil {
		t.Errorf("the escaping link %s was left in place", filepath.Join(dest, "L"))
	}
}

// The second half of the audit: with overwrite unset, a symlink entry
// removed a file outside the destination to put itself there.
func TestArchiveSymlinkCannotRemoveAFileOutside(t *testing.T) {
	base, dest := nestedDest(t)
	victim := filepath.Join(base, "x", "y", "victim")
	if err := os.WriteFile(victim, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "remove.tar")
	makeOrderedTar(t, archive,
		dirEntry("a/"),
		linkEntry("a/b", ".."),
		linkEntry("a/b/c", ".."),
		linkEntry("a/b/c/victim", "z"),
	)

	_, err := extractArchive(archive, dest, false, false)
	info, statErr := os.Lstat(victim)
	if statErr != nil {
		t.Fatalf("the file outside the destination is gone: %v (extract err=%v)", statErr, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("the file outside the destination was replaced by a symlink (extract err=%v)", err)
	}
	if got, _ := os.ReadFile(victim); string(got) != "keep me\n" {
		t.Fatalf("the file outside the destination now holds %q", got)
	}
	if err == nil {
		t.Fatal("the archive extracted without complaint")
	}
}

// A link already on disk -- planted by the account the destination was
// handed to, or left by anything else -- steers neither a write, a
// symlink, nor a removal, whatever overwrite says.
func TestArchivePlantedSymlinkIsNotFollowedOut(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		t.Run("overwrite="+strconv.FormatBool(overwrite), func(t *testing.T) {
			base, dest := nestedDest(t)
			outside := filepath.Join(base, "outside")
			if err := os.MkdirAll(outside, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "l"), []byte("keep\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dest, "evil")); err != nil {
				t.Fatal(err)
			}
			for _, entry := range []tarEntry{
				fileEntry("evil/x.txt", "x\n"),
				linkEntry("evil/l", "y"),
				dirEntry("evil/sub/"),
			} {
				archive := filepath.Join(base, "planted.tar")
				makeOrderedTar(t, archive, entry)
				if _, err := extractArchive(archive, dest, overwrite, false); err == nil {
					t.Errorf("%s was extracted through a link out of the destination", entry.name)
				}
			}
			names, _ := os.ReadDir(outside)
			if len(names) != 1 || names[0].Name() != "l" {
				t.Errorf("the directory outside now holds %v", names)
			}
			if info, err := os.Lstat(filepath.Join(outside, "l")); err != nil || !info.Mode().IsRegular() {
				t.Errorf("the file outside was replaced: %v %v", info, err)
			}
		})
	}
}

// The same for a zip, which shares nothing with the tar path but the
// destination.
func TestArchiveZipIsNotWrittenThroughAPlantedSymlink(t *testing.T) {
	base, dest := nestedDest(t)
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "evil")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "planted.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("evil/x.txt")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("x\n"))
	zw.Close()
	f.Close()

	if _, err := extractArchive(archive, dest, true, false); err == nil {
		t.Error("a zip entry was extracted through a link out of the destination")
	}
	if _, err := os.Lstat(filepath.Join(outside, "x.txt")); err == nil {
		t.Error("the zip wrote outside the destination")
	}
}

// A link that dangles when it is made can point out once a later entry
// supplies its path: `x -> p/..` is lexically the destination, and is its
// parent once `p -> .` exists. Only the pass over the finished tree sees
// it.
//
// Unix only, because the escape is. Windows cleans a path lexically before
// it resolves anything -- os/root_windows.go: "on Windows the path
// `a\..\b` is exactly equivalent to `b` alone, even if `a` does not
// exist" -- so there `p\..` is the destination and this link is inside.
// CI's windows-2022 leg kept the link, which is the right answer there.
func TestArchiveDanglingLinkIsCheckedOnceItsTargetExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows resolves `p\\..` lexically, so `x -> p/..` does not leave the destination there")
	}
	base, dest := nestedDest(t)
	archive := filepath.Join(base, "late.tar")
	makeOrderedTar(t, archive,
		linkEntry("x", "p/.."),
		linkEntry("p", "."),
	)
	if _, err := extractArchive(archive, dest, false, false); err == nil {
		t.Fatal("a link that resolves outside once extraction finished was kept")
	}
	if _, err := os.Lstat(filepath.Join(dest, "x")); err == nil {
		t.Error("the escaping link was left in place")
	}
}

func TestArchiveAbsoluteSymlinkIsRefused(t *testing.T) {
	base, dest := nestedDest(t)
	archive := filepath.Join(base, "abs.tar")
	makeOrderedTar(t, archive, linkEntry("etc", "/etc"))
	_, err := extractArchive(archive, dest, false, false)
	if err == nil || !strings.Contains(err.Error(), "links outside the destination") {
		t.Fatalf("an absolute link target: err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "etc")); err == nil {
		t.Error("the absolute link was created")
	}
}

// The property the fix must not cost: an archive whose links stay inside
// it -- the shared-library shape, and a directory link written through --
// extracts, and extracts again.
func TestArchiveInternalSymlinksStillExtract(t *testing.T) {
	base, dest := nestedDest(t)
	archive := filepath.Join(base, "lib.tar")
	makeOrderedTar(t, archive,
		dirEntry("./"),
		dirEntry("lib/"),
		fileEntry("lib/libfoo.so.1", "elf\n"),
		linkEntry("lib/libfoo.so", "libfoo.so.1"),
		dirEntry("share/"),
		linkEntry("share/lib", "../lib"),
		linkEntry("current", "lib"),
		fileEntry("current/note.txt", "via an internal link\n"),
	)

	for _, overwrite := range []bool{false, false, true} {
		if _, err := extractArchive(archive, dest, overwrite, false); err != nil {
			t.Fatalf("overwrite=%v: %v", overwrite, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(dest, "lib", "libfoo.so")); err != nil || string(got) != "elf\n" {
		t.Errorf("lib/libfoo.so reads %q, %v", got, err)
	}
	if to, err := os.Readlink(filepath.Join(dest, "share", "lib")); err != nil || filepath.ToSlash(to) != "../lib" {
		t.Errorf("share/lib -> %q, %v", to, err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "lib", "note.txt")); err != nil || string(got) != "via an internal link\n" {
		t.Errorf("a file written through an internal directory link: %q, %v", got, err)
	}
}

// overwrite unset leaves an existing symlink alone, as it leaves a file.
func TestArchiveSymlinkHonoursOverwrite(t *testing.T) {
	base, dest := nestedDest(t)
	if err := os.Symlink("old", filepath.Join(dest, "link")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "relink.tar")
	makeOrderedTar(t, archive, linkEntry("link", "new"))

	if _, err := extractArchive(archive, dest, false, false); err != nil {
		t.Fatal(err)
	}
	if to, _ := os.Readlink(filepath.Join(dest, "link")); to != "old" {
		t.Errorf("overwrite=false replaced the link with -> %q", to)
	}
	if _, err := extractArchive(archive, dest, true, false); err != nil {
		t.Fatal(err)
	}
	if to, _ := os.Readlink(filepath.Join(dest, "link")); to != "new" {
		t.Errorf("overwrite=true left the link -> %q", to)
	}
}

// The state, not only the extractor: the chained archive fails the state
// rather than reporting it extracted, and nothing lands outside.
func TestArchiveExtractedStateRefusesTheChainedArchive(t *testing.T) {
	base, dest := nestedDest(t)
	archive := filepath.Join(base, "chain.tar")
	makeOrderedTar(t, archive,
		dirEntry("a/"),
		linkEntry("a/b", ".."),
		linkEntry("a/b/L", "../.."),
		fileEntry("L/escaped.txt", "escaped\n"),
	)
	res := run(t, New(), "archive.extracted", value.MapOf("name", dest, "source", archive), false)
	if res.Succeeded() {
		t.Errorf("the state succeeded: %q", res.Comment)
	}
	if _, err := os.Lstat(filepath.Join(base, "x", "escaped.txt")); err == nil {
		t.Fatal("escaped.txt was written outside the destination")
	}
}
