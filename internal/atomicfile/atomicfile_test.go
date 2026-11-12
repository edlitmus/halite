package atomicfile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/fileperm"
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

// countRemovals swaps both removal hooks for counters that still remove,
// so that a test sees how many removals a write asked for.
func countRemovals(t *testing.T) *int {
	t.Helper()
	n := 0
	oldRemove, oldRemoveIn := removeTemp, removeTempIn
	removeTemp = func(name string) error { n++; return oldRemove(name) }
	removeTempIn = func(root *os.Root, name string) error { n++; return oldRemoveIn(root, name) }
	t.Cleanup(func() { removeTemp, removeTempIn = oldRemove, oldRemoveIn })
	return &n
}

// A write that succeeded removes nothing: the rename already took the
// temporary name. Removing it anyway is not a correctness bug, since the
// name is gone and the call fails, but on unix os.Remove then tries rmdir
// as well, and both are lookups of the destination's directory. A FreeBSD
// hub filing 20,000 returns into one directory had threads queued on that
// directory's vnode lock in exactly those calls (issue 718).
func TestASuccessfulWriteRemovesNothing(t *testing.T) {
	removals := countRemovals(t)
	dir := t.TempDir()
	if err := Write(filepath.Join(dir, "x.json"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := WriteIn(root, "z.json", []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if *removals != 0 {
		t.Errorf("two successful writes asked for %d removals, want 0", *removals)
	}
}

// The other half: a write that fails after the temporary file exists
// still removes it, by both paths, so that dropping the removal after a
// rename did not drop it before one.
func TestAWriteThatFailsBeforeTheRenameRemovesItsTemporaryFile(t *testing.T) {
	removals := countRemovals(t)
	dir := t.TempDir()
	refused := errors.New("refused")
	err := WritePrepared(filepath.Join(dir, "x.json"), []byte("y"), 0o600,
		func(string) error { return refused })
	if !errors.Is(err, refused) {
		t.Fatalf("the write gave %v, want the prepare step's error", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	broken := iotestErrReader{refused}
	if err := WriteInFrom(root, "z.json", broken, 0o600); !errors.Is(err, refused) {
		t.Fatalf("the write gave %v, want the reader's error", err)
	}
	if *removals != 2 {
		t.Errorf("two failed writes asked for %d removals, want 2", *removals)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("a failed write left %s behind", e.Name())
	}
}

// iotestErrReader fails every read with err.
type iotestErrReader struct{ err error }

func (r iotestErrReader) Read([]byte) (int, error) { return 0, r.err }

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

// A mode that denies group and other means the file is private, and on
// Windows a mode alone does not make it so. Checked here rather than in
// internal/winsec because this is where every caller states the intent:
// the enrollment key, the shared secret, the job cache and the return
// log all reach the platform through this one function.
func TestAPrivateModeIsHonouredOnThisPlatform(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "key")
	if err := Write(private, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if others, err := fileperm.Others(private); err != nil {
		t.Fatal(err)
	} else if len(others) != 0 {
		t.Errorf("a file written 0600 can be read by %v", others)
	}

	// And a mode that does not ask for privacy is left alone, so a
	// state that means a file to be world-readable gets one.
	shared := filepath.Join(dir, "motd")
	if err := Write(shared, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(shared); err != nil {
		t.Fatal(err)
	}
}
