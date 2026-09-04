// Package atomicfile replaces a file's contents in one step, and reads a
// file that something else may be replacing at that moment.
//
// Six packages had their own copy of the same write-a-temp-file-and-
// rename helper. All six were correct on unix, where rename(2) is atomic
// and cannot fail because somebody has the destination open, and all six
// were wrong on Windows, where it can and does. The hub met it first:
// a delivery being recorded while the API served the same job produced
//
//	could not record a delivery  error=rename ...\.job.json.4094326449 ...\job.json: Access is denied.
//	reading the returns for ...  open ...\returns\web1.example.json: The process cannot access the file because it is being used by another process.
//
// which are the two sides of one race. MoveFileEx has to open the
// destination for delete with no sharing; a reader holding it open makes
// that fail, and while it holds it a reader's open fails instead. Neither
// error mentions the other party, and neither is recoverable by the
// caller, so both are handled here rather than at eighteen call sites.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path with data, atomically as far as any reader is
// concerned: a reader sees either the old contents or the new, never a
// partial write and never an empty file.
//
// The temporary file is created in the destination's own directory,
// because a rename across filesystems is not atomic and on some is not
// permitted at all.
func Write(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := chmodTemp(tmp, mode); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// Sync before the rename, so that a crash between the two leaves the
	// old file rather than a new one full of zeroes.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := Rename(name, path); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	// The directory entry is synced too, so that the rename survives a
	// power loss rather than leaving a file with no name. Best effort:
	// a directory cannot be opened for reading on Windows, and a
	// filesystem that refuses the sync is not a reason to report a write
	// that succeeded as failed.
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
