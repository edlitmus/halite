package modules

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/archive"
)

func init() {
	register("archive.extracted", archiveExtracted)
}

// archiveExtracted unpacks a tar, tar.gz, or zip archive into a directory.
//
//	/opt/app:
//	  archive.extracted:
//	    - source: https://example.com/app-1.2.tar.gz
//	    - source_hash: sha256=4f3c...
//	    - if_missing: /opt/app/bin/app
//
// A local source resolves relative to the SLS file, like file.managed's.
// A remote source must carry a source_hash: downloading and unpacking
// whatever the network hands back is not something a state should do
// quietly.
func archiveExtracted(c *Ctx, id string, args map[string]any) Result {
	dest := Str(args, "name", id)
	source := Str(args, "source", "")
	sourceHash := Str(args, "source_hash", "")
	ifMissing := Str(args, "if_missing", "")

	if source == "" {
		return resFail("archive.extracted requires a source")
	}
	if ifMissing != "" {
		if _, err := os.Stat(ifMissing); err == nil {
			return resOK(fmt.Sprintf("%s exists, archive not extracted", ifMissing))
		}
	}

	remote := isURL(source)
	if remote && sourceHash == "" {
		return resFail("a remote source requires source_hash (sha256=...)")
	}

	localPath := source
	if !remote {
		if !filepath.IsAbs(localPath) && c.BaseDir != "" {
			localPath = filepath.Join(c.BaseDir, localPath)
		}
		if _, err := os.Stat(localPath); err != nil {
			return resFail("cannot read source %s: %v", localPath, err)
		}
	}

	format, err := archiveFormat(args, source)
	if err != nil {
		return resFail("%v", err)
	}

	// A remote archive has to be on disk before it can be inspected, and in
	// test mode that download is the one side effect worth avoiding.
	if remote && c.Test {
		if extracted, _ := destinationHasEntries(dest, nil); extracted {
			return resOK(fmt.Sprintf("%s already exists", dest))
		}
		return resWould(fmt.Sprintf("would fetch %s and extract it into %s", source, dest))
	}
	if remote {
		tmp, err := download(source, sourceHash)
		if err != nil {
			return resFail("%v", err)
		}
		defer os.Remove(tmp)
		localPath = tmp
	} else if sourceHash != "" {
		if err := verifyHash(localPath, sourceHash); err != nil {
			return resFail("%v", err)
		}
	}

	names, err := archive.TopLevelNames(localPath, format)
	if err != nil {
		return resFail("read %s: %v", localPath, err)
	}
	if extracted, missing := destinationHasEntries(dest, names); extracted {
		return resOK(fmt.Sprintf("%s is already extracted in %s", filepath.Base(source), dest))
	} else if c.Test {
		return resWould(fmt.Sprintf("would extract %s into %s (%d entr%s missing)",
			filepath.Base(source), dest, missing, plural(missing, "y", "ies")))
	}

	if err := archive.NewExtractor().Extract(localPath, dest, format); err != nil {
		return resFail("extract %s: %v", filepath.Base(source), err)
	}
	return resChanged(
		fmt.Sprintf("extracted %s into %s", filepath.Base(source), dest),
		map[string]string{"extracted": strings.Join(names, ", ")})
}

// destinationHasEntries reports whether every top-level archive entry is
// already present in dest, and how many are not. An archive with no entries
// counts as extracted only if the destination itself exists.
func destinationHasEntries(dest string, names []string) (bool, int) {
	info, err := os.Stat(dest)
	if err != nil || !info.IsDir() {
		return false, len(names)
	}
	missing := 0
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			missing++
		}
	}
	return missing == 0, missing
}

func archiveFormat(args map[string]any, source string) (archive.Format, error) {
	if explicit := Str(args, "archive_format", ""); explicit != "" {
		switch archive.Format(explicit) {
		case archive.FormatTar, archive.FormatTarGz, archive.FormatZip:
			return archive.Format(explicit), nil
		default:
			return "", fmt.Errorf("unknown archive_format %q (tar, tar.gz, zip)", explicit)
		}
	}
	return archive.DetectFormat(source)
}

func isURL(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")
}

// download fetches source to a temporary file and verifies it against the
// expected hash before returning. A file that fails the check is removed,
// so a bad download can never be extracted.
func download(source, sourceHash string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(source)
	if err != nil {
		return "", fmt.Errorf("fetch %s: %w", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch %s: %s", source, resp.Status)
	}

	f, err := os.CreateTemp("", "halite-archive-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("download %s: %w", source, firstErr(copyErr, closeErr))
	}
	if err := verifyHash(tmp, sourceHash); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// verifyHash checks a file against "sha256=<hex>" or a bare hex digest.
func verifyHash(path, expected string) error {
	want := strings.TrimSpace(expected)
	if algorithm, digest, found := strings.Cut(want, "="); found {
		if !strings.EqualFold(algorithm, "sha256") {
			return fmt.Errorf("unsupported source_hash algorithm %q (sha256 only)", algorithm)
		}
		want = digest
	}
	want = strings.ToLower(strings.TrimSpace(want))

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return err
	}
	got := hex.EncodeToString(sum.Sum(nil))
	if got != want {
		return fmt.Errorf("source_hash mismatch: got sha256=%s, want sha256=%s", got, want)
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
