package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
)

// WriteIn is Write for a name inside an os.Root: every component of name
// is resolved by the root, so a symlink anywhere on the way -- one that
// was already on disk, or one an archive created a moment earlier -- is
// followed only while it stays inside the root, and refused when it
// leaves.
//
// It exists for archive extraction (DIVERGENCE 5.290). Write takes a path
// and lets the kernel resolve it, and a path under a directory that an
// archive has just populated is a path whose meaning the archive chose:
// `a/b -> ..` and then `a/b/L -> ../..` makes `L/x` two levels above the
// destination while every name in the archive is lexically inside it.
// Checking the name and then writing the path is the check-then-act that
// cannot be made safe, because what the path means is decided by the
// disk at the moment of the write, not by the check. The root decides it
// at that moment.
//
// It is otherwise the same idiom as Write -- temporary file in the
// destination directory, mode applied through internal/fileperm before
// the rename, synced, renamed, directory synced -- and it lives here so
// that it is not a seventh copy (TestNothingElseWritesThroughATempFileAndARename).
// The temporary file is made by hand because os.CreateTemp takes a
// directory path, which is the thing that must not be resolved outside
// the root.
//
// The rename replaces whatever is at name, including a symlink, rather
// than writing through it: rename(2) does not follow its last component.
func WriteIn(root *os.Root, name string, data []byte, mode os.FileMode) error {
	dir, base := filepath.Dir(name), filepath.Base(name)
	var (
		tmp     *os.File
		tmpName string
		err     error
	)
	for range 10000 {
		tmpName = filepath.Join(dir, "."+base+"."+strconv.FormatUint(uint64(rand.Uint32()), 10))
		tmp, err = root.OpenFile(tmpName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil || !errors.Is(err, fs.ErrExist) {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	defer root.Remove(tmpName)
	if err := chmodTemp(tmp, mode); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if err := renameIn(root, tmpName, name); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if d, err := root.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
