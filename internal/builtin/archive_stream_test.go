package builtin

import (
	"archive/tar"
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// An entry used to be read whole into memory before it was written, so a
// one-gigabyte entry cost a gigabyte. The measure is the bytes the
// process allocated while extracting one 96 MiB entry: it was at least
// the entry's size, and is now a copy buffer.
const streamedEntrySize = 96 << 20

func allocatedWhile(f func()) uint64 {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func TestATarEntryIsStreamedNotHeldInMemory(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.tar")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	if err := tw.WriteHeader(&tar.Header{Name: "big", Typeflag: tar.TypeReg, Mode: 0o644, Size: streamedEntrySize}); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for i := 0; i < streamedEntrySize>>20; i++ {
		if _, err := tw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	f.Close()

	dest := filepath.Join(dir, "dest")
	var extractErr error
	alloc := allocatedWhile(func() { _, extractErr = extractArchive(src, dest, false, false) })
	if extractErr != nil {
		t.Fatal(extractErr)
	}
	if info, err := os.Stat(filepath.Join(dest, "big")); err != nil || info.Size() != streamedEntrySize {
		t.Fatalf("the entry was not written whole: %v, %v", info, err)
	}
	if alloc > streamedEntrySize/4 {
		t.Errorf("extracting a %d MiB entry allocated %d MiB", streamedEntrySize>>20, alloc>>20)
	}
}

func TestAZipEntryIsStreamedAndCountedAsItArrives(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.zip")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("big")
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for i := 0; i < streamedEntrySize>>20; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	f.Close()

	dest := filepath.Join(dir, "dest")
	var extractErr error
	alloc := allocatedWhile(func() { _, extractErr = extractArchive(src, dest, false, false) })
	if extractErr != nil {
		t.Fatal(extractErr)
	}
	if alloc > streamedEntrySize/4 {
		t.Errorf("extracting a %d MiB zip entry allocated %d MiB", streamedEntrySize>>20, alloc>>20)
	}

	// The budget follows the bytes as they arrive: with a limit below the
	// entry, the extraction stops there and leaves no half-written file.
	dest2 := filepath.Join(dir, "dest2")
	_, err = extractArchiveWith(src, dest2, false, false, archiveLimits{entries: 10, bytes: 8 << 20})
	if err == nil {
		t.Fatal("a zip entry past the total byte limit was accepted")
	}
	if _, statErr := os.Stat(filepath.Join(dest2, "big")); statErr == nil {
		t.Error("a half-written entry was left behind")
	}
}
