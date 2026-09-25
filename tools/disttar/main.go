// Command disttar assembles the release tarballs of SPEC 27.2: per
// platform, the static binaries, the example configuration and the
// manual pages, "for air-gapped and container use".
//
// It is run by `make dist`, after `make cross` has put the binaries in
// dist/ and before SHA256SUMS is written, so every archive is a line in
// the manifest the release workflow's two builders hold equal and the
// attestation names.
//
// # Reproducible, which is the whole job
//
// SPEC 4.3 requires two builders to produce identical digests, and an
// archive is much easier to make unreproducible than a binary is: tar and
// zip each carry a modification time, an owner, a group and a mode per
// entry, in whatever order the entries were written, and gzip carries a
// file name and a time of its own. Every one of those is pinned here:
//
//   - every time is SOURCE_DATE_EPOCH, which the Makefile takes from the
//     commit; an unset or zero epoch is refused rather than defaulted,
//     because a default is exactly the clock this exists to keep out;
//   - entries are written in sorted order, directories before the files
//     in them;
//   - owner and group are 0 and have no names;
//   - modes are 0755 for directories and binaries and 0644 for the rest,
//     never the mode the file happens to have in the checkout, which
//     depends on the checkout's umask;
//   - the gzip header has no name and no time, and the compression level
//     is fixed.
//
// It uses the standard library only (plan.md §3.6: no packaging tool in
// go.mod, and none needed for an archive).
//
// Unix platforms get a .tar.gz and Windows a .zip, because a .zip is what
// Windows opens without a tool.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func main() {
	var o options
	flag.StringVar(&o.dist, "dist", "dist", "directory holding the cross-compiled binaries, and where the archives go")
	flag.StringVar(&o.version, "version", "", "the version, as stamped into the binaries")
	flag.StringVar(&o.epoch, "epoch", "", "SOURCE_DATE_EPOCH, in seconds; every entry is given this time")
	flag.StringVar(&o.targets, "targets", "", "space-separated GOOS/GOARCH pairs")
	flag.StringVar(&o.binaries, "binaries", "", "space-separated binary names")
	flag.StringVar(&o.examples, "examples", "contrib/examples", "example configuration to include")
	flag.StringVar(&o.man, "man", "contrib/man", "manual pages to include, as man8")
	flag.Parse()
	names, err := run(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "disttar:", err)
		os.Exit(1)
	}
	for _, n := range names {
		fmt.Println("assembled", filepath.Join(o.dist, n))
	}
}

type options struct {
	dist, version, epoch, targets, binaries, examples, man string
}

// entry is one file in an archive: where it goes, what it holds, and its
// mode. Directories are derived from the paths, not listed.
type entry struct {
	path string
	body []byte
	mode int64
}

func run(o options) ([]string, error) {
	if o.version == "" {
		return nil, errors.New("-version is empty")
	}
	epoch, err := strconv.ParseInt(strings.TrimSpace(o.epoch), 10, 64)
	if err != nil || epoch <= 0 {
		return nil, fmt.Errorf("-epoch %q is not a positive SOURCE_DATE_EPOCH; refusing to fall back to the clock", o.epoch)
	}
	mtime := time.Unix(epoch, 0).UTC()
	targets, binaries := strings.Fields(o.targets), strings.Fields(o.binaries)
	if len(targets) == 0 || len(binaries) == 0 {
		return nil, errors.New("-targets and -binaries are both required")
	}

	shared, err := sharedEntries(o.examples, o.man)
	if err != nil {
		return nil, err
	}

	var made []string
	for _, t := range targets {
		goos, goarch, ok := strings.Cut(t, "/")
		if !ok {
			return nil, fmt.Errorf("target %q is not GOOS/GOARCH", t)
		}
		top := fmt.Sprintf("halite-%s-%s-%s", o.version, goos, goarch)
		entries := append([]entry(nil), shared...)
		for _, b := range binaries {
			ext := ""
			if goos == "windows" {
				ext = ".exe"
			}
			src := filepath.Join(o.dist, fmt.Sprintf("%s-%s-%s%s", b, goos, goarch, ext))
			body, err := os.ReadFile(src)
			if err != nil {
				return nil, fmt.Errorf("%s: %w; run make cross first", src, err)
			}
			entries = append(entries, entry{path: "bin/" + b + ext, body: body, mode: 0o755})
		}
		for i := range entries {
			entries[i].path = top + "/" + entries[i].path
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })

		var buf bytes.Buffer
		name := top + ".tar.gz"
		if goos == "windows" {
			name = top + ".zip"
			err = writeZip(&buf, top, entries, mtime)
		} else {
			err = writeTarGz(&buf, top, entries, mtime)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(o.dist, name), buf.Bytes(), 0o644); err != nil {
			return nil, err
		}
		made = append(made, name)
	}
	return made, nil
}

// sharedEntries is what every platform's archive carries besides its
// binaries: the example configuration and the manual pages.
func sharedEntries(examples, man string) ([]entry, error) {
	var out []entry
	add := func(dir, into string) error {
		files, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		n := 0
		for _, f := range files {
			if !f.Type().IsRegular() {
				continue
			}
			body, err := os.ReadFile(filepath.Join(dir, f.Name()))
			if err != nil {
				return err
			}
			out = append(out, entry{path: into + "/" + f.Name(), body: body, mode: 0o644})
			n++
		}
		if n == 0 {
			return fmt.Errorf("%s holds no files; an archive without them is not the one SPEC 27.2 describes", dir)
		}
		return nil
	}
	if err := add(examples, "examples"); err != nil {
		return nil, err
	}
	if err := add(man, "man/man8"); err != nil {
		return nil, err
	}
	return out, nil
}

// dirsOf lists every directory the entries sit in, top first, each once,
// in the order a reader extracting top-down needs them.
func dirsOf(top string, entries []entry) []string {
	seen := map[string]bool{top: true}
	dirs := []string{top}
	for _, e := range entries {
		d := filepath.ToSlash(filepath.Dir(e.path))
		var chain []string
		for d != top && !seen[d] {
			seen[d] = true
			chain = append([]string{d}, chain...)
			d = filepath.ToSlash(filepath.Dir(d))
		}
		dirs = append(dirs, chain...)
	}
	sort.Strings(dirs)
	return dirs
}

func writeTarGz(w io.Writer, top string, entries []entry, mtime time.Time) error {
	gz, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	// No name and no time in the gzip header: Go's writer leaves both
	// empty unless told otherwise, and that is the point. Set here so a
	// reader does not have to know the default.
	gz.Header = gzip.Header{OS: 255}
	tw := tar.NewWriter(gz)

	hdr := func(name string, mode int64, size int64, typ byte) *tar.Header {
		return &tar.Header{
			Typeflag: typ, Name: name, Mode: mode, Size: size,
			ModTime: mtime, Uid: 0, Gid: 0, Format: tar.FormatUSTAR,
		}
	}
	all := dirsOf(top, entries)
	// Directories and files, merged in sorted order, so a directory is
	// written before anything in it.
	type item struct {
		path string
		file *entry
	}
	var items []item
	for _, d := range all {
		items = append(items, item{path: d + "/"})
	}
	for i := range entries {
		items = append(items, item{path: entries[i].path, file: &entries[i]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].path < items[j].path })
	for _, it := range items {
		if it.file == nil {
			if err := tw.WriteHeader(hdr(it.path, 0o755, 0, tar.TypeDir)); err != nil {
				return err
			}
			continue
		}
		if err := tw.WriteHeader(hdr(it.path, it.file.mode, int64(len(it.file.body)), tar.TypeReg)); err != nil {
			return err
		}
		if _, err := tw.Write(it.file.body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func writeZip(w io.Writer, top string, entries []entry, mtime time.Time) error {
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	type item struct {
		path string
		file *entry
	}
	var items []item
	for _, d := range dirsOf(top, entries) {
		items = append(items, item{path: d + "/"})
	}
	for i := range entries {
		items = append(items, item{path: entries[i].path, file: &entries[i]})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].path < items[j].path })
	for _, it := range items {
		h := &zip.FileHeader{Name: it.path, Modified: mtime}
		if it.file == nil {
			h.SetMode(os.ModeDir | 0o755)
			if _, err := zw.CreateHeader(h); err != nil {
				return err
			}
			continue
		}
		h.Method = zip.Deflate
		h.SetMode(os.FileMode(it.file.mode))
		f, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := f.Write(it.file.body); err != nil {
			return err
		}
	}
	return zw.Close()
}
