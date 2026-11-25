package atomicfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteNew creates path holding data, and refuses if path already exists:
// the error then satisfies errors.Is(err, fs.ErrExist). Like Write, a reader
// never sees a partial file. Unlike Write, the name either does not exist or
// holds all of data. There is no instant at which it exists and is empty.
//
// # Why this exists
//
// It is for private keys, which internal/pki will not overwrite, and that
// refusal is the reason a plain write was not good enough. WriteKey used to
// open the final name with O_CREATE|O_EXCL and then write the key into it. A
// process killed between those two steps left an empty file under the key's
// name. The next start could not read a key from it, so it made a new one, and
// then refused to write the new one because the name was taken. The hub had
// done that to itself during its first start, and nothing but an operator
// moving the file aside would get it out. A stress loop that killed a first
// `serve` at random moments did it once in 150 runs, and CI did it to
// `TestRunnerStateOrchestrateWaitsForARunLongerThanTheClientsTimeout` on
// macos-15. DIVERGENCE 5.739.
//
// # How
//
// The data goes into a temporary file in the same directory, which is
// restricted, written and synced before anything else happens. Then it is
// given the final name by link(2) rather than by rename(2). link fails if the
// name exists and rename replaces it, and the refusal to replace is the
// point. The temporary name is then removed. A crash at any step leaves
// either no file at the final name or the whole one, plus at worst a stray
// `.name.*` beside it, which is harmless and holds a key nobody uses.
//
// os.Link is CreateHardLink on Windows, which NTFS supports. **A filesystem
// without hard links (FAT, exFAT, some FUSE mounts) cannot hold a key
// written this way**, and the error says so rather than falling back to the
// write that could strand the name. Nothing in this estate keeps its key
// directory on one. That is a judgement and has not been run against such a
// filesystem.
func WriteNew(path string, data []byte, mode os.FileMode) error {
	return Bounded(func() error { return writeNew(path, data, mode) })
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	// Checked first so that the common refusal costs no temporary file. The
	// link below is the check that counts, because the name can appear
	// between the two.
	if _, err := os.Lstat(path); err == nil {
		return &fs.PathError{Op: "create", Path: path, Err: fs.ErrExist}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	name := tmp.Name()
	// Removed on every path, success included. After a successful link the
	// temporary name is a second name for the file, not the file itself.
	defer func() { _ = removeTemp(name) }()
	if err := chmodTemp(tmp, mode); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := linkNew(name, path); err != nil {
		var le *os.LinkError
		if errors.As(err, &le) && errors.Is(le.Err, fs.ErrExist) {
			return &fs.PathError{Op: "create", Path: path, Err: fs.ErrExist}
		}
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// linkNew gives the finished temporary file its final name. A variable so
// that a test can stop the write at the moment a kill would have, and so
// that one can put the old create-then-write behaviour back and watch the
// test that guards it fail.
var linkNew = os.Link
