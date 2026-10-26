package builtin

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Issue 321, section 5: archive extraction had a limit on one entry and
// none on how many entries, or how many bytes in all, an archive may hold.
// A bomb is many small things, and a gzip stream of 300,000 empty files is
// a few hundred kilobytes that makes a node create 300,000 files.

// The archive is the finding as written: it is read here as a dry run, so
// that the test does not create what it is about. On `main` it was
// accepted, and every one of its names was reported as planned.
func TestAnArchiveOfTooManyEntriesIsRefusedBeforeItIsExtracted(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "many.tar.gz")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for i := 0; i < 300_000; i++ {
		if err := tw.WriteHeader(&tar.Header{Name: "d/f" + strconv.Itoa(i), Typeflag: tar.TypeReg, Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []interface{ Close() error }{tw, gz, f} {
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}

	planned, err := extractArchive(src, filepath.Join(dir, "dest"), false, true)
	if err == nil {
		t.Fatalf("an archive of 300,000 entries was accepted and %d names planned", len(planned))
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Errorf("the refusal does not say what was too many: %v", err)
	}
}

func tinyLimits() archiveLimits { return archiveLimits{entries: 3, bytes: 100} }

func TestAnArchivePastItsEntryLimitStopsThere(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tar")
	makeOrderedTar(t, src, fileEntry("a", "1"), fileEntry("b", "2"), fileEntry("c", "3"), fileEntry("d", "4"))
	dest := filepath.Join(dir, "dest")

	written, err := extractArchiveWith(src, dest, false, false, tinyLimits())
	if err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatalf("err = %v, want a refusal naming the entry limit", err)
	}
	if len(written) != 3 {
		t.Errorf("wrote %v; the limit is three entries and the fourth should not have been", written)
	}
	if _, err := os.Stat(filepath.Join(dest, "d")); err == nil {
		t.Error("the entry past the limit was extracted")
	}
}

func TestAnArchivePastItsByteLimitStopsThere(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tar")
	makeOrderedTar(t, src, fileEntry("a", strings.Repeat("x", 60)), fileEntry("b", strings.Repeat("y", 60)))
	dest := filepath.Join(dir, "dest")

	_, err := extractArchiveWith(src, dest, false, false, tinyLimits())
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("err = %v, want a refusal naming the byte limit", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "b")); err == nil {
		t.Error("the entry that crossed the limit was extracted")
	}
}

// Bytes are counted for an entry that is skipped or planned, not only for
// one that is read: a dry run reports what a real one would refuse.
func TestTheByteLimitHoldsInADryRun(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tar")
	makeOrderedTar(t, src, fileEntry("a", strings.Repeat("x", 60)), fileEntry("b", strings.Repeat("y", 60)))
	if _, err := extractArchiveWith(src, filepath.Join(dir, "dest"), false, true, tinyLimits()); err == nil {
		t.Fatal("a dry run accepted what a real run refuses")
	}
}

func TestAZipPastItsLimitsIsRefusedToo(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.zip")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, n := range []string{"a", "b", "c", "d"} {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("1"))
	}
	zw.Close()
	f.Close()

	if _, err := extractArchiveWith(src, filepath.Join(dir, "dest"), false, false, tinyLimits()); err == nil ||
		!strings.Contains(err.Error(), "entries") {
		t.Fatalf("err = %v, want the entry limit", err)
	}
}

// And the ordinary case is untouched: an archive inside both limits
// extracts exactly as it did.
func TestAnArchiveInsideItsLimitsExtractsAsBefore(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tar")
	makeOrderedTar(t, src, dirEntry("d"), fileEntry("d/a", "hello"), fileEntry("d/b", "world"))
	dest := filepath.Join(dir, "dest")
	if _, err := extractArchiveWith(src, dest, false, false, tinyLimits()); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "d", "b"))
	if err != nil || string(got) != "world" {
		t.Errorf("d/b = %q, %v", got, err)
	}
}

// The defaults are not so low that they refuse a real tree, and not so
// high that the per-entry limit is the only one in force.
func TestTheDefaultArchiveLimitsAreSane(t *testing.T) {
	l := defaultArchiveLimits()
	if l.entries < 100_000 || l.bytes < maxArchiveEntry {
		t.Errorf("defaults %+v would refuse a large source tree or a single permitted entry", l)
	}
	if l.bytes > 64<<30 {
		t.Errorf("a byte limit of %d is no limit", l.bytes)
	}
}
