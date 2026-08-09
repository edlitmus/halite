package archive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Format is an archive container halite can read.
type Format string

const (
	FormatTarGz Format = "tar.gz"
	FormatTar   Format = "tar"
	FormatZip   Format = "zip"
)

// DetectFormat infers the container from a file name.
func DetectFormat(name string) (Format, error) {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return FormatTarGz, nil
	case strings.HasSuffix(lower, ".tar"):
		return FormatTar, nil
	case strings.HasSuffix(lower, ".zip"):
		return FormatZip, nil
	}
	return "", fmt.Errorf("cannot tell the archive format of %q: set archive_format", name)
}

// Extract unpacks an archive file into dest.
func (e *Extractor) Extract(path, dest string, format Format) error {
	if format == FormatZip {
		return e.zip(path, dest)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if format == FormatTarGz {
		return e.TarGz(f, dest)
	}
	return e.Tar(f, dest)
}

// zip extracts a zip file under the same rules as tar: entries that escape
// the destination are refused, as is anything that is not a regular file or
// directory.
func (e *Extractor) zip(path, dest string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer reader.Close()

	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if len(reader.File) > e.MaxEntries {
		return fmt.Errorf("archive has more than %d entries", e.MaxEntries)
	}

	var written int64
	for _, entry := range reader.File {
		target, err := safeJoin(root, entry.Name)
		if err != nil {
			return err
		}
		info := entry.FileInfo()
		if info.IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("archive entry %q is not a regular file", entry.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := entry.Open()
		if err != nil {
			return err
		}
		n, err := writeEntry(target, rc, entryPerm(info.Mode(), 0o644), e.MaxTotalBytes-written)
		rc.Close()
		if err != nil {
			return fmt.Errorf("archive entry %q: %w", entry.Name, err)
		}
		written += n
	}
	return nil
}

// TopLevelNames lists the first path component of every archive entry. It
// is what archive.extracted checks to decide whether the archive has
// already been unpacked.
func TopLevelNames(path string, format Format) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	add := func(entry string) {
		top := topComponent(entry)
		if top == "" || seen[top] {
			return
		}
		seen[top] = true
		names = append(names, top)
	}

	if format == FormatZip {
		reader, err := zip.OpenReader(path)
		if err != nil {
			return nil, fmt.Errorf("open zip: %w", err)
		}
		defer reader.Close()
		for _, entry := range reader.File {
			add(entry.Name)
		}
		return names, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var source io.Reader = f
	if format == FormatTarGz {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer gz.Close()
		source = gz
	}
	tr := tar.NewReader(source)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		add(header.Name)
	}
}

func topComponent(name string) string {
	clean := strings.TrimPrefix(filepath.ToSlash(name), "./")
	clean = strings.Trim(clean, "/")
	if clean == "" || strings.HasPrefix(clean, "..") {
		return ""
	}
	if i := strings.Index(clean, "/"); i >= 0 {
		return clean[:i]
	}
	return clean
}

func entryPerm(mode os.FileMode, def os.FileMode) os.FileMode {
	if perm := mode.Perm(); perm != 0 {
		return perm
	}
	return def
}
