package builtin

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// registerArchive installs the archive module.
//
// Extraction is the dangerous half, and it is done with archive/tar and
// archive/zip rather than by shelling out to tar, so that the containment
// check is ours: an entry named ../../etc/passwd, or a symlink pointing
// outside the destination, is refused by name. That is the same property
// SPEC section 13.5 requires of the file server, applied to an archive.
func registerArchive(r *Registries) {
	r.Exec.Add(
		exec.Module{
			Sig: signature.Signature{
				Module: "archive", Function: "list",
				Doc:      "Return the paths inside an archive, without extracting it.",
				Params:   []signature.Param{req("path", signature.Path, "The archive.")},
				TestMode: signature.TestNotApplicable,
				Section:  "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				names, err := listArchive(states.Str(args, "path", ""))
				if err != nil {
					return nil, err
				}
				return toAnyList(names), nil
			},
		},
		exec.Module{
			Sig: signature.Signature{
				Module: "archive", Function: "extract",
				Doc: "Extract an archive into a directory.",
				Params: []signature.Param{
					req("path", signature.Path, "The archive."),
					req("dest", signature.Path, "Where to extract it."),
					opt("overwrite", signature.Bool, false, "Replace files that already exist."),
				},
				Mutates: true, TestMode: signature.TestReliable,
				Section: "15.2",
			},
			Fn: func(c *exec.Context, args *value.Map) (any, error) {
				written, err := extractArchive(
					states.Str(args, "path", ""), states.Str(args, "dest", ""),
					states.Bool(args, "overwrite", false), c.Test)
				if err != nil {
					return nil, err
				}
				return toAnyList(written), nil
			},
		},
	)

	r.States.Add(states.Module{
		Sig: signature.Signature{
			Module: "archive", Function: "extracted",
			Doc: "Ensure an archive has been extracted into a directory.",
			Params: []signature.Param{
				pathParam("The destination directory. Defaults to the state ID."),
				req("source", signature.Path, "The archive: a local path, or a halite:// or salt:// URI."),
				opt("source_hash", signature.String, "", "Expected digest of the archive, as `algorithm=digest`."),
				opt("if_missing", signature.Path, "", "Skip when this path already exists."),
				opt("overwrite", signature.Bool, false, "Replace files that already exist."),
				opt("makedirs", signature.Bool, true, "Create the destination directory."),
			},
			Mutates:  true,
			TestMode: signature.TestReliable,
			Section:  "15.5",
		},
		Fn: archiveExtracted,
	})
}

// listArchive enumerates an archive's entries.
func listArchive(path string) ([]string, error) {
	if isZip(path) {
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		out := make([]string, 0, len(zr.File))
		for _, f := range zr.File {
			out = append(out, f.Name)
		}
		return out, nil
	}

	f, tr, closers, err := openTar(path)
	if err != nil {
		return nil, err
	}
	defer closeAll(f, closers)

	var out []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		out = append(out, h.Name)
	}
	return out, nil
}

func isZip(path string) bool { return strings.HasSuffix(strings.ToLower(path), ".zip") }

// openTar opens a tar, transparently ungzipping it.
func openTar(path string) (*os.File, *tar.Reader, []io.Closer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	var r io.Reader = f
	var closers []io.Closer
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".tgz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, nil, nil, err
		}
		closers = append(closers, gz)
		r = gz
	}
	return f, tar.NewReader(r), closers, nil
}

func closeAll(f *os.File, closers []io.Closer) {
	for _, c := range closers {
		c.Close()
	}
	f.Close()
}

// maxArchiveEntry bounds one extracted file, so a decompression bomb
// cannot fill a disk before anything notices. SPEC section 6.5 sets the
// same kind of limit on the wire.
const maxArchiveEntry = 1 << 30

// extractArchive writes an archive's contents into dest, refusing any
// entry that would land outside it.
func extractArchive(src, dest string, overwrite, dryRun bool) ([]string, error) {
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return nil, err
	}
	if isZip(src) {
		return extractZip(src, absDest, overwrite, dryRun)
	}

	f, tr, closers, err := openTar(src)
	if err != nil {
		return nil, err
	}
	defer closeAll(f, closers)

	var written []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, err
		}
		target, err := containedEntry(absDest, h.Name)
		if err != nil {
			return written, err
		}

		switch h.Typeflag {
		case tar.TypeDir:
			if !dryRun {
				if err := os.MkdirAll(target, os.FileMode(h.Mode).Perm()|0o700); err != nil {
					return written, err
				}
			}
			written = append(written, h.Name)

		case tar.TypeSymlink:
			// A symlink is only written when its target stays inside the
			// destination. A link to /etc/shadow inside an archive is how
			// a later file write escapes.
			if _, err := containedEntry(absDest, resolveLinkTarget(h.Name, h.Linkname)); err != nil {
				return written, fmt.Errorf("archive entry %q links outside the destination: %w", h.Name, err)
			}
			if !dryRun {
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return written, err
				}
				os.Remove(target)
				if err := os.Symlink(h.Linkname, target); err != nil {
					return written, err
				}
			}
			written = append(written, h.Name)

		case tar.TypeReg:
			if h.Size > maxArchiveEntry {
				return written, fmt.Errorf("archive entry %q is %d bytes, past the %d byte limit", h.Name, h.Size, maxArchiveEntry)
			}
			if !overwrite {
				if _, err := os.Lstat(target); err == nil {
					continue
				}
			}
			if dryRun {
				written = append(written, h.Name)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return written, err
			}
			data, err := io.ReadAll(io.LimitReader(tr, maxArchiveEntry+1))
			if err != nil {
				return written, err
			}
			if int64(len(data)) > maxArchiveEntry {
				return written, fmt.Errorf("archive entry %q exceeds the %d byte limit", h.Name, maxArchiveEntry)
			}
			mode := os.FileMode(h.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			if err := writeAtomic(target, data, mode); err != nil {
				return written, err
			}
			written = append(written, h.Name)

		default:
			// Device nodes, fifos, and the rest are skipped rather than
			// created: a state tree has no business shipping one, and
			// creating one from an archive is a privilege question.
			continue
		}
	}
	return written, nil
}

func extractZip(src, absDest string, overwrite, dryRun bool) ([]string, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var written []string
	for _, zf := range zr.File {
		target, err := containedEntry(absDest, zf.Name)
		if err != nil {
			return written, err
		}
		if zf.FileInfo().IsDir() {
			if !dryRun {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return written, err
				}
			}
			written = append(written, zf.Name)
			continue
		}
		if !overwrite {
			if _, err := os.Lstat(target); err == nil {
				continue
			}
		}
		if dryRun {
			written = append(written, zf.Name)
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return written, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxArchiveEntry+1))
		rc.Close()
		if err != nil {
			return written, err
		}
		if int64(len(data)) > maxArchiveEntry {
			return written, fmt.Errorf("archive entry %q exceeds the %d byte limit", zf.Name, maxArchiveEntry)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return written, err
		}
		mode := zf.Mode().Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := writeAtomic(target, data, mode); err != nil {
			return written, err
		}
		written = append(written, zf.Name)
	}
	return written, nil
}

// containedEntry resolves an archive entry name against the destination
// and refuses anything that escapes it.
func containedEntry(absDest, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("archive entry %q is an absolute path", name)
	}
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	target := filepath.Join(absDest, clean)
	if target != absDest && !strings.HasPrefix(target, absDest+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the destination", name)
	}
	return target, nil
}

// resolveLinkTarget expresses a symlink's target relative to the archive
// root, so the containment check can be applied to it.
func resolveLinkTarget(entryName, linkTarget string) string {
	if strings.HasPrefix(linkTarget, "/") {
		return linkTarget
	}
	return filepath.Join(filepath.Dir(entryName), linkTarget)
}

func archiveExtracted(c *exec.Context, args *value.Map) (states.Result, error) {
	dest := states.Str(args, "name", "")
	source := states.Str(args, "source", "")
	if dest == "" || source == "" {
		return states.False("This state needs a destination and a source archive."), nil
	}

	if marker := states.Str(args, "if_missing", ""); marker != "" {
		if _, err := os.Lstat(marker); err == nil {
			return states.True(fmt.Sprintf("%s already exists, so the archive was not extracted.", marker)), nil
		}
	}

	local := source
	if c.Files != nil && strings.Contains(source, "://") {
		fetched, err := c.Files.Fetch(c.Env, source)
		if err != nil {
			return states.False(fmt.Sprintf("The archive %s could not be fetched: %v", source, err)), nil
		}
		local = fetched
	}

	if expected := states.Str(args, "source_hash", ""); expected != "" {
		data, err := os.ReadFile(local)
		if err != nil {
			return states.False(fmt.Sprintf("The archive %s could not be read: %v", local, err)), nil
		}
		if err := verifySourceHash(c, data, expected); err != nil {
			return states.False(fmt.Sprintf("The archive %s failed its hash check: %v", source, err)), nil
		}
	}

	// A dry run reports what is missing, which is also how the state
	// decides whether there is anything to do at all.
	planned, err := extractArchive(local, dest, states.Bool(args, "overwrite", false), true)
	if err != nil {
		return states.False(fmt.Sprintf("The archive %s could not be read: %v", source, err)), nil
	}
	missing := make([]string, 0, len(planned))
	for _, name := range planned {
		target, err := containedEntry(mustAbs(dest), name)
		if err != nil {
			return states.False(fmt.Sprintf("%v", err)), nil
		}
		if _, err := os.Lstat(target); err != nil {
			missing = append(missing, name)
		}
	}

	if len(missing) == 0 && !states.Bool(args, "overwrite", false) {
		return states.True(fmt.Sprintf("The archive %s is already extracted into %s.", source, dest)), nil
	}

	changes := value.MapOf("extracted", toAnyList(truncateList(missing, 20)))
	if c.Test {
		return states.WouldChange(
			fmt.Sprintf("%d entries from %s would be extracted into %s.", len(missing), source, dest), changes), nil
	}
	if states.Bool(args, "makedirs", true) {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return states.False(fmt.Sprintf("%s could not be created: %v", dest, err)), nil
		}
	}
	written, err := extractArchive(local, dest, states.Bool(args, "overwrite", false), false)
	if err != nil {
		return states.False(fmt.Sprintf("The archive %s could not be extracted: %v", source, err)), nil
	}
	return states.Changed(
		fmt.Sprintf("%d entries from %s were extracted into %s.", len(written), source, dest), changes), nil
}

func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// truncateList keeps a change set readable when an archive holds
// thousands of entries.
func truncateList(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	out := append([]string{}, in[:n]...)
	return append(out, fmt.Sprintf("... and %d more", len(in)-n))
}
