package archive

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// tarWith builds an uncompressed tar containing the given headers, for
// exercising entries PackDir would never produce.
func tarWith(t *testing.T, headers ...*tar.Header) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range headers {
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestRoundTripPreservesContentAndMode(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "top.sls", "base:\n  '*':\n    - web\n", 0o644)
	writeFile(t, src, "web/init.sls", "nginx:\n  pkg.installed: []\n", 0o600)
	writeFile(t, src, "web/files/nginx.conf", "listen 80;\n", 0o644)

	var buf bytes.Buffer
	if err := PackDir(src, &buf); err != nil {
		t.Fatalf("pack: %v", err)
	}
	dest := t.TempDir()
	if err := UnpackTarGz(&buf, dest); err != nil {
		t.Fatalf("unpack: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "web", "files", "nginx.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "listen 80;\n" {
		t.Errorf("content = %q", got)
	}
	info, err := os.Stat(filepath.Join(dest, "web", "init.sls"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %04o, want 0600", mode)
	}
}

func TestEntryEscapingTheDestinationIsRefused(t *testing.T) {
	dest := t.TempDir()
	outside := filepath.Join(t.TempDir(), "victim")

	for _, name := range []string{
		"../victim",
		"../../victim",
		"web/../../victim",
		outside,
	} {
		buf := tarWith(t, &tar.Header{
			Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: 4,
		})
		err := UnpackTar(buf, dest)
		if err == nil {
			t.Errorf("entry %q was extracted, want refusal", name)
			continue
		}
		if !strings.Contains(err.Error(), "escapes the destination") {
			t.Errorf("entry %q: unexpected error %v", name, err)
		}
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("a file was written outside the destination")
	}
}

func TestSymlinkEntriesAreRefused(t *testing.T) {
	buf := tarWith(t, &tar.Header{
		Typeflag: tar.TypeSymlink, Name: "passwd", Linkname: "/etc/passwd", Mode: 0o777,
	})
	err := UnpackTar(buf, t.TempDir())
	if err == nil {
		t.Fatal("a symlink entry must be refused")
	}
	if !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPackDirSkipsSymlinks(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "real.sls", "x: y\n", 0o644)
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var buf bytes.Buffer
	if err := PackDir(src, &buf); err != nil {
		t.Fatalf("pack: %v", err)
	}
	dest := t.TempDir()
	if err := UnpackTarGz(&buf, dest); err != nil {
		t.Fatalf("a packed tree must never contain entries unpack refuses: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "link")); err == nil {
		t.Error("symlink was packed")
	}
}

func TestPackDirRejectsAFile(t *testing.T) {
	src := t.TempDir()
	writeFile(t, src, "solo.sls", "x: y\n", 0o644)
	if err := PackDir(filepath.Join(src, "solo.sls"), &bytes.Buffer{}); err == nil {
		t.Fatal("packing a plain file must fail")
	}
}

func TestExtractionStopsAtTheSizeBudget(t *testing.T) {
	buf := tarWith(t, &tar.Header{Typeflag: tar.TypeReg, Name: "big", Mode: 0o644, Size: 4096})
	extractor := &Extractor{MaxTotalBytes: 1024, MaxEntries: MaxEntries}

	err := extractor.Tar(buf, t.TempDir())
	if err == nil {
		t.Fatal("an entry over the size budget must be refused")
	}
	if !strings.Contains(err.Error(), "size budget") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestExtractionStopsAtTheEntryBudget(t *testing.T) {
	buf := tarWith(t,
		&tar.Header{Typeflag: tar.TypeReg, Name: "a", Mode: 0o644, Size: 1},
		&tar.Header{Typeflag: tar.TypeReg, Name: "b", Mode: 0o644, Size: 1},
		&tar.Header{Typeflag: tar.TypeReg, Name: "c", Mode: 0o644, Size: 1},
	)
	extractor := &Extractor{MaxTotalBytes: MaxTotalBytes, MaxEntries: 2}

	err := extractor.Tar(buf, t.TempDir())
	if err == nil {
		t.Fatal("an archive over the entry budget must be refused")
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Errorf("unexpected error: %v", err)
	}
}
