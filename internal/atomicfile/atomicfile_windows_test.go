package atomicfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"

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

// holdShared opens path the way Read does, sharing everything, and keeps
// it open until the test ends.
func holdShared(t *testing.T, path string) *os.File {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(h), path)
	t.Cleanup(func() { f.Close() })
	return f
}

// Issue 799. A reader that shares delete, as Read does, is not in a
// replace's way at all: the replace lands at once, the reader goes on
// reading the file it opened, and the next open sees the new one. Under
// MoveFileEx the replace was refused for as long as the reader held the
// file, and this test's reader holds it throughout, so the old code failed
// it after the whole retry window rather than racing.
func TestAReplaceDoesNotWaitForAReaderThatSharesDelete(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "new")
	to := filepath.Join(dir, "old")
	if err := os.WriteFile(from, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader := holdShared(t, to)

	start := time.Now()
	err := Rename(from, to)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("a replace was refused while a reader that shares delete held the file, after %v: %v", elapsed, err)
	}
	if elapsed >= fast {
		t.Errorf("the replace took %v; it waited for a reader that was never in its way", elapsed)
	}
	held, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(held) != "old" {
		t.Errorf("the reader's open file says %q after the replace, want the one it opened", held)
	}
	body, err := Read(to)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new" {
		t.Errorf("after the replace the path says %q", body)
	}
}
