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
//   - modes are 0755 for directories, binaries and the files git records
//     as executable (the rc.d scripts), and 0644 for the rest, never the
//     mode the file happens to have in the checkout, which depends on the
//     checkout's umask;
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
	"os/exec"
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
	flag.StringVar(&o.fipsTargets, "fips-targets", "",
		"space-separated GOOS/GOARCH pairs that also get a -fips archive (SPEC 27.4); empty means none")
	flag.StringVar(&o.examples, "examples", "contrib/examples", "example configuration to include")
	flag.StringVar(&o.man, "man", "contrib/man", "manual pages to include, each filed under man/man<N> by its suffix")
	flag.StringVar(&o.handbook, "handbook", "build/handbook",
		"the built HTML handbook (tools/handbook build), carried whole under handbook/")
	flag.StringVar(&o.rcd, "rcd", "contrib/rc.d", "rc.d scripts, carried under service/rc.d")
	flag.StringVar(&o.systemd, "systemd", "contrib/systemd", "systemd units, carried under service/systemd")
	flag.StringVar(&o.license, "license", "LICENSE", "the licence, copied to the top of every archive")
	flag.StringVar(&o.evidence, "evidence", "docs/evidence.md",
		"the generated evidence report, carried in every archive")
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
	dist, version, epoch, targets, binaries, examples, man, license, evidence, fipsTargets string
	handbook, rcd, systemd                                                                 string
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
	// The version names every archive, and it is the version stamped
	// into the binaries inside them, so it is the bare number: a tag
	// is `v0.1.0`, the version is `0.1.0`. A `v` reaching here means
	// the Makefile did not drop it, and the archives would be named
	// `halite-v0.1.0-...` beside release notes and a README that fetch
	// `halite-0.1.0-...`, which is every download link of the release
	// broken. Refused rather than trimmed here: trimming would make the
	// archive disagree with the stamp in the binaries it holds.
	// DIVERGENCE 5.199.
	if len(o.version) > 1 && o.version[0] == 'v' && o.version[1] >= '0' && o.version[1] <= '9' {
		return nil, fmt.Errorf("-version %q carries the tag's v; the version is %q, and the Makefile drops it", o.version, o.version[1:])
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

	shared, err := sharedEntries(o)
	if err != nil {
		return nil, err
	}

	// An archive is a platform and a variant. The ordinary one carries
	// bin/halite-hub; the FIPS one (SPEC 27.4, Linux only) is a separate
	// archive whose bin/ holds halite-hub-fips, the name `make install-fips`
	// and the service drop-ins expect. It is not folded into the ordinary
	// archive: the whole point of the -fips set is that nobody gets one by
	// accident, and a binary beside its non-FIPS twin is one `cp` from that.
	type variant struct {
		target, suffix string
	}
	var variants []variant
	for _, t := range targets {
		variants = append(variants, variant{t, ""})
	}
	for _, t := range strings.Fields(o.fipsTargets) {
		variants = append(variants, variant{t, "-fips"})
	}

	var made []string
	for _, v := range variants {
		t := v.target
		goos, goarch, ok := strings.Cut(t, "/")
		if !ok {
			return nil, fmt.Errorf("target %q is not GOOS/GOARCH", t)
		}
		top := fmt.Sprintf("halite-%s-%s-%s%s", o.version, goos, goarch, v.suffix)
		entries := append([]entry(nil), shared...)
		for _, b := range binaries {
			ext := ""
			if goos == "windows" {
				ext = ".exe"
			}
			src := filepath.Join(o.dist, fmt.Sprintf("%s%s-%s-%s%s", b, v.suffix, goos, goarch, ext))
			body, err := os.ReadFile(src)
			if err != nil {
				return nil, fmt.Errorf("%s: %w; run make %s first", src, err, map[string]string{"": "cross", "-fips": "fips-cross"}[v.suffix])
			}
			entries = append(entries, entry{path: "bin/" + b + v.suffix + ext, body: body, mode: 0o755})
		}
		for i := range entries {
			entries[i].path = top + "/" + entries[i].path
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })

		var buf bytes.Buffer
		var err error
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
// binaries: the licence, the example configuration and the manual pages.
//
// The licence is required, not optional: an archive is a copy of the
// software handed to somebody, and the BSD licence's first condition is
// that a redistribution in binary form carries its text.
func sharedEntries(o options) ([]entry, error) {
	examples, man, license, evidence := o.examples, o.man, o.license, o.evidence
	var out []entry
	// files collects the regular files under dir, descending into its
	// directories when deep is set, in the order os.ReadDir gives, which
	// is sorted, so the archive's order does not depend on the disk's.
	//
	// The examples descend: contrib/examples/tree is a state and pillar
	// tree, and it shipped as nothing at all while this read one level
	// (DIVERGENCE 5.262). The manual pages do not, and a directory there
	// is not one of them.
	//
	// Where the directory is under version control, only the files git
	// tracks are packaged (see trackedUnder). The handbook is built, not
	// committed, so it is added without a list.
	var files func(dir, into string, deep bool, tracked map[string]os.FileMode) (int, error)
	files = func(dir, into string, deep bool, tracked map[string]os.FileMode) (int, error) {
		listing, err := os.ReadDir(dir)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, f := range listing {
			path := filepath.Join(dir, f.Name())
			if f.IsDir() && deep {
				got, err := files(path, into+"/"+f.Name(), deep, tracked)
				if err != nil {
					return 0, err
				}
				n += got
				continue
			}
			if !f.Type().IsRegular() {
				continue
			}
			mode := os.FileMode(0o644)
			if tracked != nil {
				m, ok := tracked[path]
				if !ok {
					fmt.Fprintf(os.Stderr, "disttar: not packaging %s: git does not track it\n", path)
					continue
				}
				mode = m
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return 0, err
			}
			out = append(out, entry{path: into + "/" + f.Name(), body: body, mode: int64(mode)})
			n++
		}
		return n, nil
	}
	add := func(dir, into string, deep bool) error {
		var tracked map[string]os.FileMode
		if dir != o.handbook {
			tracked = trackedUnder(dir)
		}
		n, err := files(dir, into, deep, tracked)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%s holds no files; an archive without them is not the one SPEC 27.2 describes", dir)
		}
		return nil
	}
	body, err := os.ReadFile(license)
	if err != nil {
		return nil, fmt.Errorf("%w; every archive carries the licence", err)
	}
	out = append(out, entry{path: "LICENSE", body: body, mode: 0o644})

	// EVIDENCE.md, generated from internal/builtin's table, in every
	// archive.
	//
	// Every release before this one carried a hand-written "not verified on
	// real hardware" section in its notes, and a hand-written list of what
	// has not been run goes stale in the one direction that matters: work
	// gets done and the list still says it was not. It happened twice in a
	// fortnight (DIVERGENCE 5.83 and 5.142). Somebody holding only the
	// tarball -- which is the point of the tarball, for air-gapped use --
	// should be able to read what was demonstrated without a network.
	evidenceBody, err := os.ReadFile(evidence)
	if err != nil {
		return nil, fmt.Errorf("%w; every archive carries the evidence report, "+
			"generated by `go run ./tools/gendocs docs`", err)
	}
	out = append(out, entry{path: "EVIDENCE.md", body: evidenceBody, mode: 0o644})
	if err := add(examples, "examples", true); err != nil {
		return nil, err
	}
	// The manual pages are filed by the section their suffix names, which
	// is where man(1) looks: halite-hub.8 under man8, a file format under
	// man5, an overview under man7. A suffix that is none of those is
	// refused, because a page in the wrong directory is one nobody finds.
	pages, err := os.ReadDir(man)
	if err != nil {
		return nil, err
	}
	nman := 0
	trackedPages := trackedUnder(man)
	for _, f := range pages {
		if !f.Type().IsRegular() {
			continue
		}
		if _, ok := trackedPages[filepath.Join(man, f.Name())]; trackedPages != nil && !ok {
			fmt.Fprintf(os.Stderr, "disttar: not packaging %s: git does not track it\n", filepath.Join(man, f.Name()))
			continue
		}
		sec := strings.TrimPrefix(filepath.Ext(f.Name()), ".")
		if sec != "5" && sec != "7" && sec != "8" {
			return nil, fmt.Errorf("%s: a manual page in %s ends in .5, .7 or .8, and %q ends in %q", f.Name(), man, f.Name(), sec)
		}
		body, err := os.ReadFile(filepath.Join(man, f.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, entry{path: "man/man" + sec + "/" + f.Name(), body: body, mode: 0o644})
		nman++
	}
	if nman == 0 {
		return nil, fmt.Errorf("%s holds no manual pages; an archive without them is not the one SPEC 27.2 describes", man)
	}

	// The handbook is the documentation of this release, built from this
	// commit's sources; a release whose archive lacks it, or carries the
	// previous one, is documentation that has drifted from the code, which is
	// what building it in the release is there to prevent. An unbuilt
	// handbook is therefore an error, not an omission.
	if err := add(o.handbook, "handbook", true); err != nil {
		return nil, fmt.Errorf("%w; run `make handbook` first", err)
	}
	// The service files are in the archive so that an installation from a
	// tarball is not an installation without them: the chapter on
	// installing says to copy from service/.
	if err := add(o.rcd, "service/rc.d", true); err != nil {
		return nil, err
	}
	if err := add(o.systemd, "service/systemd", true); err != nil {
		return nil, err
	}
	return out, nil
}

// trackedUnder is the set of files git tracks under dir, as paths joined to
// dir the way the walk joins them, each with the mode the archive gives it,
// or nil when git cannot say (no git, or dir is not in a repository), in
// which case the caller packages what is there at 0644, as it always did.
//
// The mode is git's, not the checkout's: 0755 for a file git records as
// 100755 and 0644 for the rest. The rc.d scripts are 100755 in the
// repository and went into every archive at 0644, so a script unpacked and
// copied with cp, or run in place, was not executable; the install chapters
// use `install -m 0555`, which hid it. git's bit is part of the commit, so
// the archive is still the same from any checkout of it.
//
// It asks because the packager used to take whatever lay under
// contrib/examples, rc.d and systemd: a scratch file left in one of them
// by a local build ended up in an archive that is then signed and
// attested. CI builds from a clean checkout and never met it; a release
// built on a working machine could (issue 321, build section).
func trackedUnder(dir string) map[string]os.FileMode {
	out, err := exec.Command("git", "-C", dir, "ls-files", "-s", "-z").Output()
	if err != nil {
		return nil
	}
	set := map[string]os.FileMode{}
	// Each record is "<mode> <object> <stage>\t<path>".
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, rel, ok := strings.Cut(rec, "\t")
		if !ok || rel == "" {
			continue
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(meta, "100755 ") {
			mode = 0o755
		}
		set[filepath.Join(dir, filepath.FromSlash(rel))] = mode
	}
	return set
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
