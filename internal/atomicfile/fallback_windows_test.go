package atomicfile

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// Where the filesystem refuses POSIX semantics (FAT, many network shares)
// the replace falls back to MoveFileEx and still lands. The refusal is a
// stand-in, because a runner's NTFS never refuses: what is exercised is
// the fallback path itself, and that a refusal is not reported as the
// rename failing. A real FAT volume was measured on the issue 799 draft
// (DIVERGENCE 5.715).
func TestAFallbackReplaceLandsWhereThereIsNoPOSIXRename(t *testing.T) {
	refused := 0
	saved := setRenameInfo
	setRenameInfo = func(windows.Handle, uint32, *byte, uint32) error {
		refused++
		return windows.ERROR_INVALID_PARAMETER
	}
	t.Cleanup(func() { setRenameInfo = saved })

	dir := t.TempDir()
	path := filepath.Join(dir, "job.json")
	if err := Write(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("a replace with POSIX semantics refused did not fall back: %v", err)
	}
	if refused == 0 {
		t.Fatal("the POSIX rename was never attempted, so the fallback was not what replaced the file")
	}
	body, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new" {
		t.Errorf("after the fallback replace the file says %q", body)
	}
}
