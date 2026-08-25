package gitfs

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// MaxArchiveBytes bounds what one ref may unpack to.
//
// A repository is somebody else's, even when it is the estate's own: a
// branch with a very large file in it should be a refusal rather than a
// full disk on the hub.
const MaxArchiveBytes = 2 << 30

// MaxArchiveFiles bounds how many.
const MaxArchiveFiles = 200000

// extractTar unpacks `git archive` output into a directory.
//
// Every path is checked before anything is written. `git archive` does
// not produce a traversing path, but this is unpacking an archive built
// from a repository the hub did not write, and an unpacker that trusts
// its input is the shape of bug that has been found in every archive
// tool ever shipped.
func extractTar(archive []byte, dest string) error {
	reader := tar.NewReader(bytes.NewReader(archive))
	var written int64
	files := 0

	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		// `git archive` writes a `pax_global_header` first, carrying
		// the commit id. It is metadata rather than a file, and
		// skipping it is not a relaxation of the check below: an
		// extended header describes the next entry, which is still
		// checked.
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}
		files++
		if files > MaxArchiveFiles {
			return fmt.Errorf("more than %d files", MaxArchiveFiles)
		}

		target, err := safeJoin(dest, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			written += header.Size
			if written > MaxArchiveBytes {
				return fmt.Errorf("more than %d bytes", MaxArchiveBytes)
			}
			if err := writeFile(target, reader, header.Size, modeFor(header)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// A symlink is refused rather than created. The file server
			// has its own `follow_symlinks` decision, and a link
			// arriving from a repository is one the operator did not
			// make: a `.ssh/id_rsa` symlink in a served tree turns a
			// file server into a key server.
			return fmt.Errorf("%s is a symlink, which a served tree may not carry", header.Name)
		default:
			// A device node, a fifo, a hard link. None of them is a
			// state tree, and each is a way to make the hub create
			// something surprising.
			return fmt.Errorf("%s is not a regular file or a directory", header.Name)
		}
	}
}

// safeJoin resolves a path inside the destination, or refuses.
func safeJoin(dest, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("an entry with no name")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, string(filepath.Separator)) {
		return "", fmt.Errorf("%q is absolute", name)
	}
	for _, segment := range strings.Split(filepath.ToSlash(clean), "/") {
		if segment == ".." {
			return "", fmt.Errorf("%q leaves the tree", name)
		}
	}
	target := filepath.Join(dest, clean)
	// Belt and braces: the segment check above is the reasoning, and
	// this is the check that holds if the reasoning is wrong.
	if !strings.HasPrefix(target, filepath.Clean(dest)+string(filepath.Separator)) {
		return "", fmt.Errorf("%q leaves the tree", name)
	}
	return target, nil
}

// modeFor is the mode to write a file with.
//
// The executable bit is carried over, because a repository serving a
// script means it; nothing else is. A setuid bit arriving from a git
// tree is not something an estate asked for.
func modeFor(header *tar.Header) os.FileMode {
	if header.FileInfo().Mode()&0o100 != 0 {
		return 0o700
	}
	return 0o600
}

func writeFile(path string, r io.Reader, size int64, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	// Bounded by the header's size, so a truncated or lying archive
	// cannot write more than it declared.
	if _, err := io.CopyN(f, r, size); err != nil && err != io.EOF {
		return err
	}
	return nil
}
