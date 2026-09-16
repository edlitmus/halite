package builtin

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/safehttp"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

const (
	// maxRemoteArchive bounds a streamed download.
	//
	// Far larger than maxRemoteSource, and it can be: this never holds
	// the archive in memory. What it bounds is the disk, so that a
	// server answering forever fills a cache directory rather than the
	// partition the node runs on.
	maxRemoteArchive int64 = 2 << 30

	// remoteArchiveTimeout bounds the whole fetch.
	//
	// safehttp's 30-second default is right for a checksum file and
	// wrong for a JDK: the client timeout covers reading the body, so a
	// large archive on an ordinary link fails a deadline meant for a
	// request that should have been instant.
	remoteArchiveTimeout = 30 * time.Minute

	// extrnDirName is where fetched sources are kept under cache_dir,
	// mirroring the directory Salt calls `extrn_files`.
	extrnDirName = "extrn_files"
)

// remoteArchivePath is where a fetched archive is cached.
//
// Laid out by host and path the way Salt's `_extrn_path` is, so an
// operator who knows where Salt put things can find these.
//
// No credential can reach the path, and not because it is stripped
// here: `url.Parse` puts userinfo in `u.User` and leaves `u.Host` with
// the host alone. Salt has to strip it by hand -- `netloc.split("@")[-1]`
// -- because it works on the raw netloc. The property still has a test,
// because it is the property that matters and not the mechanism: a
// credential must never become a directory name that every later `ls`
// of the cache prints.
//
// An empty return means this node has no cache_dir. The caller falls
// back to a temporary file, which works and keeps nothing.
func remoteArchivePath(c *exec.Context, source string) string {
	root := cacheDirOf(c)
	if root == "" {
		return ""
	}
	u, err := url.Parse(source)
	if err != nil {
		return ""
	}
	host := u.Host
	// A port's colon cannot be a directory name on Windows, and Salt
	// removes it for the same reason.
	host = strings.ReplaceAll(host, ":", "")
	name := u.Path
	if u.RawQuery != "" {
		name += "-" + u.RawQuery
	}
	clean := filepath.Clean(filepath.FromSlash("/" + strings.TrimPrefix(name, "/")))
	env := c.Env
	if env == "" {
		env = "base"
	}
	return filepath.Join(root, extrnDirName, env, host, clean)
}

// fetchRemoteArchive streams an http(s) archive to a local file and
// returns its path, along with whether that path is this state's own
// copy rather than something the tree pointed at.
//
// Streamed rather than read: `file.managed` diffs its source against
// what is on disk and so must hold it, and an archive is extracted from
// a path and never needs to be in memory at all. The estate's own case
// is a JDK tarball, which is why this matters rather than being tidy.
func fetchRemoteArchive(c *exec.Context, args *value.Map, source string) (out fetchedArchive, err error) {
	// A directory made for an archive that never arrived is a directory
	// nothing will ever come back for.
	defer func() {
		if err != nil && out.Temp != "" {
			_ = os.RemoveAll(out.Temp)
			out.Temp = ""
		}
	}()

	if err := safehttp.CheckURL(source); err != nil {
		return out, err
	}
	expected := states.Str(args, "source_hash", "")

	// Salt's rule, applied to this state through `file.cached`: a
	// remote source that cannot be verified is refused rather than
	// fetched. It matters more here than for a single managed file --
	// what arrives is unpacked, and an archive decides its own paths.
	if expected == "" && !states.Bool(args, "skip_verify", false) {
		return out, fmt.Errorf(
			"%s is a remote archive and nothing here can verify it: "+
				"set source_hash, or set skip_verify to true to accept whatever it returns",
			redactedURL(source))
	}

	dest := remoteArchivePath(c, source)
	cached := dest != ""
	if !cached {
		// No cache_dir: a directory of this state's own, removed with
		// the archive. It is a directory rather than a bare temporary
		// file because the *name* has to survive -- see below.
		dir, mkErr := os.MkdirTemp("", "halite-archive-*")
		if mkErr != nil {
			return out, mkErr
		}
		out.Temp = dir
		dest = filepath.Join(dir, remoteArchiveName(source))
	}

	// A cached copy that already matches is the download not made. This
	// is what Salt's cache buys, and on a highstate every twenty minutes
	// against an unchanged JDK it is the difference between a request
	// and a few hundred megabytes.
	if cached && expected != "" {
		if err := verifySourceHashFile(c, dest, expected); err == nil {
			out.Path, out.Verified = dest, true
			return out, nil
		}
	}

	if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
		return out, mkErr
	}

	u, parseErr := url.Parse(source)
	if parseErr != nil {
		return out, fmt.Errorf("source %q is not a URL: %w", redactedURL(source), parseErr)
	}
	user := u.User
	u.User = nil
	safe := u.String()

	request, reqErr := http.NewRequestWithContext(c.Ctx, http.MethodGet, safe, nil)
	if reqErr != nil {
		return out, fmt.Errorf("building the request for %s: %w", safe, reqErr)
	}
	if user != nil {
		password, _ := user.Password()
		request.SetBasicAuth(user.Username(), password)
	}

	response, doErr := safehttp.Client(safehttp.Options{Timeout: remoteArchiveTimeout}).Do(request)
	if doErr != nil {
		return out, fmt.Errorf("fetching %s: %w", safe, doErr)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return out, fmt.Errorf("%s answered %s", safe, response.Status)
	}

	// Written beside the destination and renamed, so that an interrupted
	// download is never mistaken for a cached archive on the next run.
	part := dest + ".part"
	f, openErr := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if openErr != nil {
		return out, openErr
	}
	written, copyErr := io.Copy(f, io.LimitReader(response.Body, maxRemoteArchive+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		_ = os.Remove(part)
		return out, fmt.Errorf("reading %s: %w", safe, copyErr)
	case closeErr != nil:
		_ = os.Remove(part)
		return out, closeErr
	case written > maxRemoteArchive:
		_ = os.Remove(part)
		return out, fmt.Errorf("%s is larger than the %d byte limit for a fetched archive",
			safe, maxRemoteArchive)
	}

	// Verified before it is put in place, so a cache never holds a file
	// that failed its digest and a later run cannot pick one up.
	if expected != "" {
		if err := verifySourceHashFile(c, part, expected); err != nil {
			_ = os.Remove(part)
			return out, fmt.Errorf("%s failed its hash check: %w", safe, err)
		}
	}
	if renameErr := os.Rename(part, dest); renameErr != nil {
		_ = os.Remove(part)
		return out, renameErr
	}
	out.Path, out.Verified = dest, expected != ""
	return out, nil
}

// fetchedArchive is where a fetched archive landed and what is known
// about it.
type fetchedArchive struct {
	// Path is the local file, empty when nothing was fetched.
	Path string
	// Verified records that the digest was checked on the way in, so
	// the caller does not read the whole archive again to repeat it.
	Verified bool
	// Temp is a directory to remove along with the archive, for a node
	// with no cache_dir. Empty when the archive is in the cache.
	Temp string
}

// remoteArchiveName is the file name a fetched archive is given.
//
// The name is load-bearing and not cosmetic: the extractor chooses tar,
// gzip or zip by suffix, exactly as Salt guesses the format from the
// source's basename. A temporary file with a random name is read as an
// uncompressed tar and fails with "unexpected EOF" on the first gzip
// byte, which is a confusing way to say "the name was thrown away".
func remoteArchiveName(source string) string {
	name := "archive"
	if u, err := url.Parse(source); err == nil {
		if base := path.Base(u.Path); base != "" && base != "." && base != "/" {
			name = base
		}
	}
	// Whatever the URL says, this becomes a path component.
	name = strings.NewReplacer("/", "_", `\`, "_", ":", "_").Replace(name)
	if name == "" || name == "." || name == ".." {
		name = "archive"
	}
	return name
}

// verifySourceHashFile checks a file's digest without reading it whole.
//
// MD5 and SHA-1 go the long way round, through the byte path that warns
// about them: they are not in `newHash` on purpose, and a source_hash
// weak enough to need one is not the case worth streaming for.
func verifySourceHashFile(c *exec.Context, path, expected string) error {
	algorithm, digest, err := parseSourceHash(expected)
	if err != nil {
		return err
	}
	if algorithm == "md5" || algorithm == "sha1" {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return verifySourceHash(c, data, expected)
	}

	h, err := newHash(algorithm)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != digest {
		return fmt.Errorf("%s digest is %s, expected %s", algorithm, got, digest)
	}
	return nil
}
