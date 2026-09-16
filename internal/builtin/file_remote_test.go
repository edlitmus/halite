package builtin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

const testRepoKey = "-----BEGIN PGP PUBLIC KEY BLOCK-----\nnot really a key\n"

func testRepoKeySum() string {
	sum := sha256.Sum256([]byte(testRepoKey))
	return hex.EncodeToString(sum[:])
}

// artifactServer answers like the estate's artifact server: the body on
// a GET, and 401 unless the request carries basic auth when one is
// wanted. It records what it was asked, so a test can assert the
// credentials did not travel in the request line.
func artifactServer(t *testing.T, wantUser, wantPass string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.String())
		if wantUser != "" {
			user, pass, ok := r.BasicAuth()
			if !ok || user != wantUser || pass != wantPass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		switch r.URL.Path {
		case "/salt-archive-keyring.pgp":
			fmt.Fprint(w, testRepoKey)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "<html>404, and this must never become the file</html>")
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(s.Close)
	return s, &seen
}

// The estate's `/etc/apt/keyrings/salt-archive-keyring.pgp` is a
// `file.managed` with an https source and a bare sha256 `source_hash`.
// It failed with `open https://...: no such file or directory`, because
// the URL was handed to the filesystem.
func TestFileManagedFetchesAnHTTPSource(t *testing.T) {
	server, _ := artifactServer(t, "", "")
	r := New()
	path := filepath.Join(t.TempDir(), "salt-archive-keyring.pgp")

	res := run(t, r, "file.managed", value.MapOf(
		"name", path,
		"source", server.URL+"/salt-archive-keyring.pgp",
		"source_hash", testRepoKeySum(),
		"mode", "0644",
	), false)

	if !res.Succeeded() {
		t.Fatalf("result = %+v, comment %q", res.Result, res.Comment)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != testRepoKey {
		t.Errorf("contents = %q", b)
	}
}

// Salt refuses a remote source it cannot verify rather than fetching it:
// an unchecked download becomes the contents of a managed file.
func TestAnUnverifiableRemoteSourceIsRefused(t *testing.T) {
	server, seen := artifactServer(t, "", "")
	r := New()
	path := filepath.Join(t.TempDir(), "key.pgp")

	res := run(t, r, "file.managed", value.MapOf(
		"name", path,
		"source", server.URL+"/salt-archive-keyring.pgp",
	), false)

	if res.Succeeded() {
		t.Fatalf("an unverifiable source was accepted: %+v", res)
	}
	if !strings.Contains(res.Comment, "source_hash") || !strings.Contains(res.Comment, "skip_verify") {
		t.Errorf("the comment does not say what to do: %q", res.Comment)
	}
	if len(*seen) != 0 {
		t.Errorf("it was fetched anyway: %v", *seen)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the file was written")
	}
}

// skip_verify is the escape hatch, and it must actually fetch.
func TestSkipVerifyAcceptsAnUnverifiedRemoteSource(t *testing.T) {
	server, _ := artifactServer(t, "", "")
	r := New()
	path := filepath.Join(t.TempDir(), "key.pgp")

	res := run(t, r, "file.managed", value.MapOf(
		"name", path,
		"source", server.URL+"/salt-archive-keyring.pgp",
		"skip_verify", true,
	), false)

	if !res.Succeeded() {
		t.Fatalf("result = %+v, comment %q", res.Result, res.Comment)
	}
	if b, _ := os.ReadFile(path); string(b) != testRepoKey {
		t.Errorf("contents = %q", b)
	}
}

// The estate's openjdk source carries its credentials in the URL. They
// have to reach the server as an Authorization header -- Go never puts
// userinfo in the request line, so without this the server sees no
// credential at all and answers 401.
func TestCredentialsInTheURLBecomeBasicAuth(t *testing.T) {
	server, _ := artifactServer(t, "deploy", "hunter2sekrit")
	r := New()
	path := filepath.Join(t.TempDir(), "key.pgp")

	withCreds := strings.Replace(server.URL, "http://", "http://deploy:hunter2sekrit@", 1)
	res := run(t, r, "file.managed", value.MapOf(
		"name", path,
		"source", withCreds+"/salt-archive-keyring.pgp",
		"source_hash", testRepoKeySum(),
	), false)

	if !res.Succeeded() {
		t.Fatalf("result = %+v, comment %q", res.Result, res.Comment)
	}
	if b, _ := os.ReadFile(path); string(b) != testRepoKey {
		t.Errorf("contents = %q", b)
	}
}

// And the URL this build puts in its own messages has no credentials in
// it. The redactor of DIVERGENCE 5.103 would strip them at the sink,
// but a message built here has no reason to carry one at all -- and
// this is the state whose comment carried a live token into the job
// cache in the first place.
func TestAFailedFetchDoesNotNameTheCredential(t *testing.T) {
	server, _ := artifactServer(t, "deploy", "hunter2sekrit")
	r := New()

	withCreds := strings.Replace(server.URL, "http://", "http://deploy:hunter2sekrit@", 1)
	for _, tc := range []struct {
		name string
		args *value.Map
	}{
		{"a failing fetch", value.MapOf(
			"name", filepath.Join(t.TempDir(), "x"),
			"source", withCreds+"/missing",
			"skip_verify", true,
		)},
		{"a source it may not verify", value.MapOf(
			"name", filepath.Join(t.TempDir(), "y"),
			"source", withCreds+"/salt-archive-keyring.pgp",
		)},
		{"a mismatched digest", value.MapOf(
			"name", filepath.Join(t.TempDir(), "z"),
			"source", withCreds+"/salt-archive-keyring.pgp",
			"source_hash", strings.Repeat("0", 64),
		)},
	} {
		res := run(t, r, "file.managed", tc.args, false)
		if res.Succeeded() {
			t.Errorf("%s: succeeded, so the comment proves nothing", tc.name)
			continue
		}
		if strings.Contains(res.Comment, "hunter2sekrit") {
			t.Errorf("%s: the comment carries the credential: %q", tc.name, res.Comment)
		}
	}
}

// A 404 body is a perfectly valid body, and writing one into a managed
// file is how a node ends up with an HTML error page in /etc.
func TestANonSuccessStatusIsNotTheFileContents(t *testing.T) {
	server, _ := artifactServer(t, "", "")
	r := New()
	path := filepath.Join(t.TempDir(), "key.pgp")

	res := run(t, r, "file.managed", value.MapOf(
		"name", path,
		"source", server.URL+"/missing",
		"skip_verify", true,
	), false)

	if res.Succeeded() {
		t.Fatalf("a 404 was accepted: %+v", res)
	}
	if strings.Contains(res.Comment, "must never become the file") {
		t.Errorf("the error page reached the comment: %q", res.Comment)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the error page was written to the file")
	}
}

// A source whose hash does not match is not written. The check already
// existed; this pins it to the remote path, where it is the only thing
// standing between an upstream and /etc.
func TestAMismatchedSourceHashIsNotWritten(t *testing.T) {
	server, _ := artifactServer(t, "", "")
	r := New()
	path := filepath.Join(t.TempDir(), "key.pgp")

	res := run(t, r, "file.managed", value.MapOf(
		"name", path,
		"source", server.URL+"/salt-archive-keyring.pgp",
		"source_hash", strings.Repeat("0", 64),
	), false)

	if res.Succeeded() {
		t.Fatalf("a mismatched digest was accepted: %+v", res)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the file was written despite the digest")
	}
}

// The schemes Salt fetches and this build does not must say so, rather
// than being handed to the filesystem and answering with `open s3://...`.
func TestUnimplementedRemoteSchemesSaySo(t *testing.T) {
	r := New()
	for _, scheme := range []string{"ftp", "s3", "swift"} {
		path := filepath.Join(t.TempDir(), "x")
		res := run(t, r, "file.managed", value.MapOf(
			"name", path,
			"source", scheme+"://example.com/thing",
			"skip_verify", true,
		), false)

		if res.Succeeded() {
			t.Errorf("%s: accepted", scheme)
		}
		if !strings.Contains(res.Comment, "not implemented") {
			t.Errorf("%s: the comment does not say it is unimplemented: %q", scheme, res.Comment)
		}
		if strings.Contains(res.Comment, "no such file") {
			t.Errorf("%s: it reached the filesystem: %q", scheme, res.Comment)
		}
	}
}

// A local path is not a URL, and a Windows path has a one-letter scheme.
func TestLocalPathsAreNotTreatedAsRemote(t *testing.T) {
	for _, path := range []string{
		"/srv/salt/files/nginx.conf",
		`C:\salt\files\nginx.conf`,
		"relative/path.conf",
		"salt://web/nginx.conf",
		"halite://web/nginx.conf",
	} {
		if _, remote, _ := remoteScheme(path); remote {
			t.Errorf("%q was treated as a remote URL", path)
		}
	}
}
