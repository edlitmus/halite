package atomicfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/fileperm/permtest"
)

// The retry exists for one situation: somebody else has the file open
// right now and will let go in microseconds. A permission error is not
// that situation, and waiting two seconds for it to change cost every
// caller that met one two seconds before it was told (issue 614).
//
// "Fast" is measured against retryWindow rather than a stopwatch figure,
// because a loaded runner is slow in absolute terms and still nowhere
// near two seconds for one open.
const fast = retryWindow / 4

// A file this account may not read is not going to become readable by
// waiting for it.
func TestReadingAFileThisAccountMayNotReadFailsAtOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "denied")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	permtest.DenyRead(t, path)

	start := time.Now()
	_, err := Read(path)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a file this account is denied was read; the deny entry did not take")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Errorf("the error is %v; a caller testing for fs.ErrPermission does not recognise it", err)
	}
	if elapsed >= fast {
		t.Errorf("a permission error took %v to report; it is not transient and should not be retried", elapsed)
	}
}

// Replacing a read-only file is refused with ERROR_ACCESS_DENIED, the
// same code a reader holding the file causes. The two need telling apart.
func TestReplacingAReadOnlyFileFailsAtOnce(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "new")
	to := filepath.Join(dir, "old")
	if err := os.WriteFile(from, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	// os.Chmod without a write bit is the read-only attribute here.
	if err := os.Chmod(to, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(to, 0o600) })

	start := time.Now()
	err := Rename(from, to)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a read-only file was replaced; there is nothing here to measure")
	}
	t.Logf("replacing a read-only file: %v", err)
	if elapsed >= fast {
		t.Errorf("a refusal took %v to report; it is not transient and should not be retried", elapsed)
	}
}

// And the case the retry is for still waits: a reader that opened the
// destination the way os.Open does, without FILE_SHARE_DELETE, and lets
// go a little later. Without the wait the replace fails.
func TestReplacingAFileSomebodyIsReadingWaitsForThem(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "new")
	to := filepath.Join(dir, "old")
	if err := os.WriteFile(from, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(to)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(retryWindow / 8)
		reader.Close()
		close(released)
	}()
	defer func() { <-released }()

	if err := Rename(from, to); err != nil {
		t.Fatalf("a replace did not wait for a reader that let go after %v: %v", retryWindow/8, err)
	}
	body, err := os.ReadFile(to)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new" {
		t.Errorf("after the replace the file says %q", body)
	}
}
