// Package archive packs and unpacks tar.gz trees with stdlib archive/tar
// and compress/gzip. The control plane uses it to ship a state tree to an
// agent, and archive.extracted uses it for local archives.
//
// Extraction is the dangerous half: an archive is untrusted input, and an
// entry named "../../etc/passwd" or a symlink pointing outside the
// destination is the classic way to turn one into a write primitive. Every
// entry is resolved and checked against the destination prefix, and only
// regular files and directories are ever created.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Default limits on extraction, so a malformed or hostile archive cannot
// exhaust the disk before anything notices.
const (
	MaxTotalBytes int64 = 512 << 20
	MaxEntries          = 100000
)

// Extractor unpacks archives under explicit limits.
type Extractor struct {
	MaxTotalBytes int64
	MaxEntries    int
}

// NewExtractor returns an extractor with the default limits.
func NewExtractor() *Extractor {
	return &Extractor{MaxTotalBytes: MaxTotalBytes, MaxEntries: MaxEntries}
}

// PackDir writes root and everything under it as a gzipped tar, with paths
// relative to root. Symlinks, devices, and sockets are skipped: a state
// tree is configuration, and shipping links across hosts is a trap.
func PackDir(root string, w io.Writer) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("read %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}

	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		// Ownership is meaningless across hosts and modification times make
		// archives non-reproducible; only the mode carries over.
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "", ""
		header.ModTime = header.ModTime.UTC().Truncate(1)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

// UnpackTarGz extracts a gzipped tar into dest under the default limits.
func UnpackTarGz(r io.Reader, dest string) error {
	return NewExtractor().TarGz(r, dest)
}

// UnpackTar extracts an uncompressed tar into dest under the default limits.
func UnpackTar(r io.Reader, dest string) error {
	return NewExtractor().Tar(r, dest)
}

// TarGz extracts a gzipped tar into dest, creating it if needed.
func (e *Extractor) TarGz(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	return e.unpack(tar.NewReader(gz), dest)
}

// Tar extracts an uncompressed tar into dest.
func (e *Extractor) Tar(r io.Reader, dest string) error {
	return e.unpack(tar.NewReader(r), dest)
}

func (e *Extractor) unpack(tr *tar.Reader, dest string) error {
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}

	var written int64
	for entries := 0; ; entries++ {
		header, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		if entries >= e.MaxEntries {
			return fmt.Errorf("archive has more than %d entries", e.MaxEntries)
		}
		target, err := safeJoin(root, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, entryMode(header, 0o755)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			n, err := writeEntry(target, tr, entryMode(header, 0o644), e.MaxTotalBytes-written)
			if err != nil {
				return fmt.Errorf("archive entry %q: %w", header.Name, err)
			}
			written += n
		default:
			// Symlinks, hard links, devices, and fifos are refused rather
			// than skipped: an archive that contains them is not the kind of
			// archive halite ships, and quietly dropping entries hides that.
			return fmt.Errorf("archive entry %q has unsupported type %q", header.Name, string(header.Typeflag))
		}
	}
}

// safeJoin resolves name against root and refuses anything that would land
// outside it, whether by absolute path, "..", or a separator trick.
func safeJoin(root, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("archive entry has an empty name")
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	target := filepath.Join(root, clean)
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	return target, nil
}

func entryMode(header *tar.Header, def os.FileMode) os.FileMode {
	mode := header.FileInfo().Mode().Perm()
	if mode == 0 {
		return def
	}
	return mode
}

// writeEntry copies one file, refusing to exceed the remaining budget.
func writeEntry(target string, r io.Reader, mode os.FileMode, remaining int64) (int64, error) {
	if remaining <= 0 {
		return 0, fmt.Errorf("archive exceeds the size budget")
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, remaining))
	if err != nil {
		return n, err
	}
	if n == remaining {
		return n, fmt.Errorf("archive exceeds the size budget")
	}
	return n, f.Chmod(mode)
}
