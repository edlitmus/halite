package modules

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTarGz writes a gzipped tar of the given path/content pairs.
func makeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		header := &tar.Header{
			Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: int64(len(content)),
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestArchiveExtractedUnpacksAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.tar.gz")
	makeTarGz(t, src, map[string]string{"app/bin/run": "#!/bin/sh\n", "app/README": "hi\n"})
	dest := filepath.Join(dir, "opt")

	args := map[string]any{"name": dest, "source": src}
	res := archiveExtracted(&Ctx{}, "extract", args)
	if !res.Ok || !res.Changed {
		t.Fatalf("first extraction: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(dest, "app", "README"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi\n" {
		t.Errorf("content = %q", got)
	}

	again := archiveExtracted(&Ctx{}, "extract", map[string]any{"name": dest, "source": src})
	if !again.Ok || again.Changed {
		t.Errorf("second run changed something: %+v", again)
	}
}

func TestArchiveExtractedTestModeChangesNothing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.tar.gz")
	makeTarGz(t, src, map[string]string{"app/README": "hi\n"})
	dest := filepath.Join(dir, "opt")

	res := archiveExtracted(&Ctx{Test: true}, "extract", map[string]any{"name": dest, "source": src})
	if !res.Ok || !res.Changed {
		t.Fatalf("test mode should report a pending change: %+v", res)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("test mode created the destination")
	}
}

func TestArchiveExtractedHonorsIfMissing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.tar.gz")
	makeTarGz(t, src, map[string]string{"app/README": "hi\n"})
	sentinel := filepath.Join(dir, "already-there")
	if err := os.WriteFile(sentinel, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	res := archiveExtracted(&Ctx{}, "extract", map[string]any{
		"name": filepath.Join(dir, "opt"), "source": src, "if_missing": sentinel,
	})
	if !res.Ok || res.Changed {
		t.Errorf("if_missing did not skip extraction: %+v", res)
	}
}

func TestArchiveExtractedVerifiesSourceHash(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.tar.gz")
	makeTarGz(t, src, map[string]string{"app/README": "hi\n"})
	dest := filepath.Join(dir, "opt")

	good := archiveExtracted(&Ctx{}, "extract", map[string]any{
		"name": dest, "source": src, "source_hash": "sha256=" + sha256Of(t, src),
	})
	if !good.Ok {
		t.Fatalf("matching hash rejected: %+v", good)
	}

	bad := archiveExtracted(&Ctx{}, "extract", map[string]any{
		"name": filepath.Join(dir, "opt2"), "source": src,
		"source_hash": "sha256=" + strings.Repeat("0", 64),
	})
	if bad.Ok {
		t.Error("a mismatched source_hash must fail the state")
	}
	if !strings.Contains(bad.Comment, "mismatch") {
		t.Errorf("unhelpful comment: %q", bad.Comment)
	}
}

func TestArchiveExtractedRefusesRemoteSourceWithoutHash(t *testing.T) {
	res := archiveExtracted(&Ctx{}, "extract", map[string]any{
		"name": t.TempDir(), "source": "https://example.com/app.tar.gz",
	})
	if res.Ok {
		t.Fatal("a remote source without source_hash must be refused")
	}
	if !strings.Contains(res.Comment, "source_hash") {
		t.Errorf("unhelpful comment: %q", res.Comment)
	}
}

func TestArchiveExtractedNeedsAKnownFormat(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "app.bundle")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := archiveExtracted(&Ctx{}, "extract", map[string]any{"name": dir, "source": src})
	if res.Ok {
		t.Fatal("an unrecognized extension must fail rather than guess")
	}
	if !strings.Contains(res.Comment, "archive_format") {
		t.Errorf("comment should point at archive_format: %q", res.Comment)
	}
}

func TestArchiveExtractedResolvesRelativeSourcesAgainstTheSLS(t *testing.T) {
	base := t.TempDir()
	makeTarGz(t, filepath.Join(base, "app.tar.gz"), map[string]string{"app/README": "hi\n"})
	dest := filepath.Join(t.TempDir(), "opt")

	res := archiveExtracted(&Ctx{BaseDir: base}, "extract", map[string]any{
		"name": dest, "source": "app.tar.gz",
	})
	if !res.Ok || !res.Changed {
		t.Fatalf("relative source not resolved against BaseDir: %+v", res)
	}
}
