package atomicfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A reader must never see a half-written file, and must never stop a
// writer from replacing it. Both of those held on unix by construction
// and neither held on Windows, where the hub could not record a delivery
// while the API was serving the same job.
func TestAReplaceAndAReadDoNotBlockEachOther(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "job.json")

	old := bytes.Repeat([]byte("o"), 4096)
	newer := bytes.Repeat([]byte("n"), 4096)
	if err := Write(path, old, 0o600); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	writeErr := make(chan error, 200)
	readErr := make(chan error, 200)

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			body := old
			if i%2 == 0 {
				body = newer
			}
			if err := Write(path, body, 0o600); err != nil {
				writeErr <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			b, err := Read(path)
			if err != nil {
				readErr <- err
				return
			}
			// Whole or nothing: a partial write would be neither.
			if !bytes.Equal(b, old) && !bytes.Equal(b, newer) {
				readErr <- errors.New("a reader saw a partially written file")
				return
			}
		}
	}()
	wg.Wait()

	close(writeErr)
	close(readErr)
	for err := range writeErr {
		t.Errorf("a replace failed while the file was being read: %v", err)
	}
	for err := range readErr {
		t.Errorf("a read failed while the file was being replaced: %v", err)
	}
}

// A missing file reports itself as missing, in the form callers test for.
// Read is not os.ReadFile on every platform, so this is not free.
func TestReadingWhatIsNotThereIsNotExist(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("reading a missing file gave %v, want a not-exist error", err)
	}
}

// The temporary file never survives, whether the write succeeded or not.
// A directory filling with .job.json.* is how this idiom fails quietly.
func TestNoTemporaryFileIsLeftBehind(t *testing.T) {
	dir := t.TempDir()
	if err := Write(filepath.Join(dir, "x.json"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "x.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the directory holds %v, want only x.json", names)
	}
}

// Writing into a directory that does not exist fails naming the file the
// caller asked for, not the temporary one it never saw.
func TestAFailedWriteNamesTheCallersPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "x.json")
	err := Write(path, []byte("y"), 0o600)
	if err == nil {
		t.Fatal("writing into a missing directory succeeded")
	}
	if !bytes.Contains([]byte(err.Error()), []byte(path)) {
		t.Errorf("the error does not name %s: %v", path, err)
	}
}
