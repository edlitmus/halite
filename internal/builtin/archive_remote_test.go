package builtin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/states"
	"github.com/edlitmus/halite/internal/value"
)

// archiveServer serves one tarball, counts how many times it was asked
// for it, and requires basic auth when one is wanted -- which is the
// shape of the estate's own artifact server.
func archiveServer(t *testing.T, body []byte, wantUser, wantPass string) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wantUser != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != wantUser || pass != wantPass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		switch r.URL.Path {
		case "/jdk.tar.gz":
			hits++
			_, _ = w.Write(body)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "404, and this must never be unpacked")
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(s.Close)
	return s, &hits
}

// tarballFixture builds a small tar.gz and returns its bytes and digest.
func tarballFixture(t *testing.T) ([]byte, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jdk.tar.gz")
	makeTarGz(t, path, map[string]string{"jdk17/bin/java": "#!/bin/sh\n", "jdk17/README": "jdk\n"})
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return body, hex.EncodeToString(sum[:])
}

// runCached calls a state with a cache_dir configured, which is what
// decides whether a fetched archive is kept where a later run finds it.
func runCached(t *testing.T, r *Registries, cacheDir string, args *value.Map) states.Result {
	t.Helper()
	c := &exec.Context{
		Ctx:    context.Background(),
		Grains: value.MapOf("os", "Ubuntu", "os_family", "Debian"),
		Pillar: value.NewMap(0),
		Config: value.MapOf("cache_dir", cacheDir),
		NodeID: "test.node",
		Env:    "base",
		Runner: &exec.RecordingRunner{},
	}
	res, err := r.States.Call(c, "archive.extracted", args)
	if err != nil {
		t.Fatalf("archive.extracted: %v", err)
	}
	return res
}

// The estate's `base/openjdk/init.sls` is an archive.extracted with an
// https source. It failed before this: anything holding "://" went to
// the file server, which answered that it is not served from the base
// environment.
func TestArchiveExtractedFetchesAnHTTPSource(t *testing.T) {
	body, sum := tarballFixture(t)
	server, hits := archiveServer(t, body, "", "")
	r := New()
	dest := filepath.Join(t.TempDir(), "usr-lib")

	res := run(t, r, "archive.extracted", value.MapOf(
		"name", dest,
		"source", server.URL+"/jdk.tar.gz",
		"source_hash", sum,
	), false)

	if !res.Succeeded() {
		t.Fatalf("result = %v, comment %q", res.Result, res.Comment)
	}
	if *hits != 1 {
		t.Errorf("the server was asked %d times", *hits)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "jdk17", "README")); err != nil || string(b) != "jdk\n" {
		t.Errorf("the archive was not extracted: %v %q", err, b)
	}
}

// A cached copy that already matches is the download not made. On a
// highstate every twenty minutes against an unchanged JDK that is the
// difference between one request and a few hundred megabytes.
func TestACachedArchiveIsNotDownloadedAgain(t *testing.T) {
	body, sum := tarballFixture(t)
	server, hits := archiveServer(t, body, "", "")
	r := New()
	cache := t.TempDir()
	source := server.URL + "/jdk.tar.gz"

	first := runCached(t, r, cache, value.MapOf(
		"name", filepath.Join(t.TempDir(), "a"), "source", source, "source_hash", sum))
	if !first.Succeeded() {
		t.Fatalf("first run: %v %q", first.Result, first.Comment)
	}
	if *hits != 1 {
		t.Fatalf("the first run made %d requests", *hits)
	}

	second := runCached(t, r, cache, value.MapOf(
		"name", filepath.Join(t.TempDir(), "b"), "source", source, "source_hash", sum))
	if !second.Succeeded() {
		t.Fatalf("second run: %v %q", second.Result, second.Comment)
	}
	if *hits != 1 {
		t.Errorf("the cached archive was downloaded again: %d requests", *hits)
	}
}

// keep_source: false means the fetched copy does not stay. With a cache
// that is now a real question rather than a no-op.
func TestKeepSourceFalseRemovesTheFetchedArchive(t *testing.T) {
	body, sum := tarballFixture(t)
	server, hits := archiveServer(t, body, "", "")
	r := New()
	cache := t.TempDir()
	source := server.URL + "/jdk.tar.gz"

	res := runCached(t, r, cache, value.MapOf(
		"name", filepath.Join(t.TempDir(), "a"),
		"source", source, "source_hash", sum, "keep_source", false))
	if !res.Succeeded() {
		t.Fatalf("result = %v %q", res.Result, res.Comment)
	}

	var found []string
	_ = filepath.Walk(cache, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("the fetched archive was kept: %v", found)
	}

	// And the next run has to fetch it again, which is the cost the
	// operator chose.
	second := runCached(t, r, cache, value.MapOf(
		"name", filepath.Join(t.TempDir(), "b"),
		"source", source, "source_hash", sum, "keep_source", false))
	if !second.Succeeded() {
		t.Fatalf("second run: %v %q", second.Result, second.Comment)
	}
	if *hits != 2 {
		t.Errorf("requests = %d, want 2", *hits)
	}
}

// An unverifiable archive is refused rather than fetched. It matters
// more here than for a managed file: what arrives is unpacked, and an
// archive decides its own paths.
func TestAnUnverifiableRemoteArchiveIsRefused(t *testing.T) {
	body, _ := tarballFixture(t)
	server, hits := archiveServer(t, body, "", "")
	r := New()
	dest := filepath.Join(t.TempDir(), "out")

	res := run(t, r, "archive.extracted", value.MapOf(
		"name", dest, "source", server.URL+"/jdk.tar.gz"), false)

	if res.Succeeded() {
		t.Fatalf("accepted: %+v", res)
	}
	if !strings.Contains(res.Comment, "source_hash") || !strings.Contains(res.Comment, "skip_verify") {
		t.Errorf("the comment does not say what to do: %q", res.Comment)
	}
	if *hits != 0 {
		t.Errorf("it was fetched anyway: %d requests", *hits)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("something was extracted")
	}
}

// A mismatched digest must not be extracted, and must not be left in
// the cache for a later run to pick up as though it had matched.
func TestAMismatchedArchiveIsNeitherExtractedNorCached(t *testing.T) {
	body, _ := tarballFixture(t)
	server, _ := archiveServer(t, body, "", "")
	r := New()
	cache := t.TempDir()
	dest := filepath.Join(t.TempDir(), "out")

	res := runCached(t, r, cache, value.MapOf(
		"name", dest,
		"source", server.URL+"/jdk.tar.gz",
		"source_hash", strings.Repeat("0", 64)))

	if res.Succeeded() {
		t.Fatalf("a mismatched archive was accepted: %+v", res)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("it was extracted")
	}
	var found []string
	_ = filepath.Walk(cache, func(p string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("a file that failed its digest was left in the cache: %v", found)
	}
}

// A 404 body is a perfectly valid body. Unpacking one is worse than
// writing it to a file: an archive decides where its entries land.
func TestANonSuccessStatusIsNotUnpacked(t *testing.T) {
	body, _ := tarballFixture(t)
	server, _ := archiveServer(t, body, "", "")
	r := New()
	dest := filepath.Join(t.TempDir(), "out")

	res := run(t, r, "archive.extracted", value.MapOf(
		"name", dest, "source", server.URL+"/missing", "skip_verify", true), false)

	if res.Succeeded() {
		t.Fatalf("a 404 was accepted: %+v", res)
	}
	if strings.Contains(res.Comment, "must never be unpacked") {
		t.Errorf("the error page reached the comment: %q", res.Comment)
	}
	// The status is what went wrong, and saying so is the difference
	// between a diagnosable failure and "could not be read". Without
	// the status check the 404 body is written and then fails to
	// extract, which fails for the wrong reason and says the wrong
	// thing.
	if !strings.Contains(res.Comment, "404") {
		t.Errorf("the comment does not name the status: %q", res.Comment)
	}
}

// The estate's openjdk source carries its credentials in the URL, and
// they must reach the server as an Authorization header.
func TestArchiveCredentialsInTheURLBecomeBasicAuth(t *testing.T) {
	body, sum := tarballFixture(t)
	server, hits := archiveServer(t, body, "deploy", "hunter2sekrit")
	r := New()
	dest := filepath.Join(t.TempDir(), "out")

	withCreds := strings.Replace(server.URL, "http://", "http://deploy:hunter2sekrit@", 1)
	res := run(t, r, "archive.extracted", value.MapOf(
		"name", dest, "source", withCreds+"/jdk.tar.gz", "source_hash", sum), false)

	if !res.Succeeded() {
		t.Fatalf("result = %v, comment %q", res.Result, res.Comment)
	}
	if *hits != 1 {
		t.Errorf("requests = %d", *hits)
	}
}

// And no comment names the credential, on any of the failing paths.
func TestAFailedArchiveFetchDoesNotNameTheCredential(t *testing.T) {
	body, _ := tarballFixture(t)
	server, _ := archiveServer(t, body, "deploy", "hunter2sekrit")
	r := New()
	withCreds := strings.Replace(server.URL, "http://", "http://deploy:hunter2sekrit@", 1)

	for _, tc := range []struct {
		name string
		args *value.Map
	}{
		{"a failing fetch", value.MapOf(
			"name", filepath.Join(t.TempDir(), "x"),
			"source", withCreds+"/missing", "skip_verify", true)},
		{"an archive it may not verify", value.MapOf(
			"name", filepath.Join(t.TempDir(), "y"),
			"source", withCreds+"/jdk.tar.gz")},
		{"a mismatched digest", value.MapOf(
			"name", filepath.Join(t.TempDir(), "z"),
			"source", withCreds+"/jdk.tar.gz", "source_hash", strings.Repeat("0", 64))},
	} {
		res := run(t, r, "archive.extracted", tc.args, false)
		if res.Succeeded() {
			t.Errorf("%s: succeeded, so the comment proves nothing", tc.name)
			continue
		}
		if strings.Contains(res.Comment, "hunter2sekrit") {
			t.Errorf("%s: the comment carries the credential: %q", tc.name, res.Comment)
		}
	}
}

// The cache path must not become a directory named after a credential,
// which every later `ls` of the cache would print. Salt strips it too.
func TestTheCachePathHoldsNoCredential(t *testing.T) {
	c := &exec.Context{Config: value.MapOf("cache_dir", filepath.Join("var", "cache")), Env: "base"}
	got := remoteArchivePath(c, "https://deploy:hunter2sekrit@artifacts.example.com:8443/vmop/jdk.tar.gz")
	if got == "" {
		t.Fatal("no cache path")
	}
	if strings.Contains(got, "hunter2sekrit") || strings.Contains(got, "deploy") {
		t.Errorf("the cache path carries the credential: %q", got)
	}
	if !strings.Contains(got, "artifacts.example.com") {
		t.Errorf("the cache path does not name the host: %q", got)
	}
	if strings.Contains(filepath.Base(filepath.Dir(got)), ":") {
		t.Errorf("a port colon reached a directory name: %q", got)
	}
}

// Streaming is the point: an archive is never held whole in memory, so
// one larger than a managed file's limit still works.
func TestALargeArchiveIsStreamedNotBuffered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.tar.gz")
	// Incompressible padding, so the gzip stream really is larger than
	// the limit rather than compressing back under it.
	pad := make([]byte, maxRemoteSource+(1<<20))
	rng := rand.New(rand.NewSource(1))
	if _, err := rng.Read(pad); err != nil {
		t.Fatal(err)
	}
	makeTarGz(t, path, map[string]string{"big/data": string(pad), "big/README": "big\n"})
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) <= maxRemoteSource {
		t.Skipf("the fixture compressed to %d bytes, under the %d byte file.managed limit",
			len(body), maxRemoteSource)
	}
	sum := sha256.Sum256(body)

	server, _ := archiveServer(t, body, "", "")
	r := New()
	dest := filepath.Join(t.TempDir(), "out")

	res := run(t, r, "archive.extracted", value.MapOf(
		"name", dest, "source", server.URL+"/jdk.tar.gz",
		"source_hash", hex.EncodeToString(sum[:])), false)

	if !res.Succeeded() {
		t.Fatalf("a %d byte archive failed: %v %q", len(body), res.Result, res.Comment)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "big", "README")); err != nil || !bytes.Equal(b, []byte("big\n")) {
		t.Errorf("it was not extracted: %v %q", err, b)
	}
}
