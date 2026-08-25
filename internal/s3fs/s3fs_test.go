package s3fs

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
)

// fakeS3 answers ListObjectsV2 and GET, and checks that every request
// carried a plausible SigV4 Authorization header.
//
// It is not a second implementation of the signature — that is checked
// against AWS's published vectors in `internal/awsauth`. What it checks
// here is that this backend signs at all, signs for the right region
// and service, and does not leak a credential into a URL.
type fakeS3 struct {
	server  *httptest.Server
	objects map[string]string
	// requests records every path asked for.
	requests []string
	// unsigned counts requests that arrived without a signature.
	unsigned int
	// wrongScope counts requests signed for another region or service.
	wrongScope int
	// listFails makes the listing fail.
	listFails bool
}

func newFakeS3(t *testing.T, objects map[string]string) *fakeS3 {
	t.Helper()
	f := &fakeS3{objects: objects}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.URL.Path+"?"+r.URL.RawQuery)

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, awsauth.Algorithm) {
			f.unsigned++
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if !strings.Contains(auth, "/us-east-1/s3/aws4_request") {
			f.wrongScope++
			w.WriteHeader(http.StatusForbidden)
			return
		}
		// A credential must never reach the query string.
		if strings.Contains(r.URL.RawQuery, "AWSAccessKeyId") ||
			strings.Contains(r.URL.RawQuery, "Signature=") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if r.URL.Query().Get("list-type") == "2" {
			f.writeListing(w, r)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/")
		if _, isPathStyle := f.objects[key]; !isPathStyle {
			// Path-style addressing puts the bucket first.
			if _, rest, ok := strings.Cut(key, "/"); ok {
				key = rest
			}
		}
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeS3) writeListing(w http.ResponseWriter, r *http.Request) {
	if f.listFails {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	type item struct {
		Key  string `xml:"Key"`
		ETag string `xml:"ETag"`
		Size int64  `xml:"Size"`
	}
	var contents []item
	keys := make([]string, 0, len(f.objects))
	for k := range f.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if prefix != "" && !strings.HasPrefix(k, prefix) {
			continue
		}
		contents = append(contents, item{Key: k, ETag: `"x"`, Size: int64(len(f.objects[k]))})
	}
	out := struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		IsTruncated bool     `xml:"IsTruncated"`
		Contents    []item   `xml:"Contents"`
	}{IsTruncated: false, Contents: contents}
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(out)
}

func backendFor(t *testing.T, f *fakeS3, adjust func(*Options)) *Backend {
	t.Helper()
	endpoint := f.server.URL
	opts := Options{
		Buckets: []Bucket{{
			Name: "estate", Region: "us-east-1",
			Endpoint: endpoint, PathStyle: true,
		}},
		CacheDir: filepath.Join(t.TempDir(), "s3fs"),
		Provider: &awsauth.Provider{
			Explicit: awsauth.Credentials{
				AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret",
			},
		},
		Client: f.server.Client(),
		Now:    func() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) },
		Log:    func(level, msg string, kv ...any) { t.Logf("%s: %s %v", level, msg, kv) },
	}
	if adjust != nil {
		adjust(&opts)
	}
	// httptest serves https, and the check in New only permits https.
	if !strings.HasPrefix(opts.Buckets[0].Endpoint, "https://") {
		opts.Buckets[0].Endpoint = strings.Replace(endpoint, "http://", "https://", 1)
	}
	b, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestABucketBecomesEnvironments(t *testing.T) {
	f := newFakeS3(t, map[string]string{
		"base/top.sls":    "base:\n  '*':\n    - web\n",
		"base/web.sls":    "nginx:\n  pkg.installed: []\n",
		"staging/top.sls": "staging:\n  '*':\n    - web\n",
		"staging/web.sls": "nginx:\n  pkg.installed: []\n",
	})
	b := backendFor(t, f, nil)
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}

	envs := b.Envs()
	if strings.Join(envs, ",") != "base,staging" {
		t.Fatalf("the environments are %v", envs)
	}
	body, err := os.ReadFile(filepath.Join(b.Roots()["base"][0], "top.sls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "'*'") {
		t.Errorf("the served file is %q", body)
	}
	if f.unsigned != 0 {
		t.Errorf("%d requests arrived unsigned", f.unsigned)
	}
	if f.wrongScope != 0 {
		t.Errorf("%d requests were signed for the wrong scope", f.wrongScope)
	}
}

// A prefix is stripped, so a bucket holding `states/base/top.sls`
// serves `top.sls` in `base`.
func TestAPrefixIsStripped(t *testing.T) {
	f := newFakeS3(t, map[string]string{
		"states/base/top.sls": "base: {}\n",
		"unrelated/thing.txt": "not the tree\n",
	})
	b := backendFor(t, f, func(o *Options) { o.Buckets[0].Prefix = "states/" })
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if envs := b.Envs(); strings.Join(envs, ",") != "base" {
		t.Fatalf("the environments are %v", envs)
	}
	if _, err := os.Stat(filepath.Join(b.Roots()["base"][0], "top.sls")); err != nil {
		t.Errorf("the prefix was not stripped: %v", err)
	}
}

// A bucket that is one environment.
func TestAWholeBucketCanBeOneEnvironment(t *testing.T) {
	f := newFakeS3(t, map[string]string{"top.sls": "base: {}\n", "web.sls": "x: {}\n"})
	b := backendFor(t, f, func(o *Options) { o.Buckets[0].Env = "base" })
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if envs := b.Envs(); strings.Join(envs, ",") != "base" {
		t.Fatalf("the environments are %v", envs)
	}
	for _, name := range []string{"top.sls", "web.sls"} {
		if _, err := os.Stat(filepath.Join(b.Roots()["base"][0], name)); err != nil {
			t.Errorf("%s is not served: %v", name, err)
		}
	}
}

// A key is not a path: `..` in one is a legal object name in S3 and a
// traversal here.
func TestAKeyCannotEscapeTheCache(t *testing.T) {
	for _, key := range []string{"../../etc/passwd", "a/../../b", "/absolute"} {
		if _, err := safeRelative(key); err == nil {
			t.Errorf("%q was accepted as a path", key)
		}
	}
	if got, err := safeRelative("web/nginx.conf"); err != nil || got != "web/nginx.conf" {
		t.Errorf("a good key read as %q %v", got, err)
	}
}

// A network blip must not empty the file server.
func TestAFailedUpdateLeavesTheServedTreeInPlace(t *testing.T) {
	f := newFakeS3(t, map[string]string{"base/top.sls": "base: {}\n"})
	b := backendFor(t, f, nil)
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := b.Roots()

	f.listFails = true
	if err := b.Update(context.Background()); err == nil {
		t.Error("an update against a failing bucket reported success")
	}
	after := b.Roots()
	if len(after["base"]) == 0 || after["base"][0] != before["base"][0] {
		t.Errorf("a failed update emptied the file server: %v", after)
	}
}

// A second update with nothing changed must not re-download.
func TestUnchangedObjectsAreNotRefetched(t *testing.T) {
	f := newFakeS3(t, map[string]string{"base/top.sls": "base: {}\n", "base/web.sls": "x: {}\n"})
	b := backendFor(t, f, nil)
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := len(f.requests)

	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The listing happens again; the objects do not.
	if extra := len(f.requests) - first; extra > 1 {
		t.Errorf("a second update made %d requests beyond the listing", extra-1)
	}
}

func TestAllowAndDenyFilterEnvironments(t *testing.T) {
	f := newFakeS3(t, map[string]string{
		"base/top.sls":      "base: {}\n",
		"staging/top.sls":   "staging: {}\n",
		"wip-thing/top.sls": "wip: {}\n",
	})
	b := backendFor(t, f, func(o *Options) { o.DenyEnvs = []string{"wip-*"} })
	if err := b.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, env := range b.Envs() {
		if strings.HasPrefix(env, "wip-") {
			t.Errorf("a denied environment is served: %v", b.Envs())
		}
	}
}

// An endpoint built for the commercial partition is wrong in China, and
// wrong in a way that fails when somebody depends on it.
func TestTheEndpointComesFromThePartition(t *testing.T) {
	cases := []struct {
		partition string
		bucket    Bucket
		want      string
	}{
		{"aws", Bucket{Name: "estate", Region: "eu-west-1"},
			"https://estate.s3.eu-west-1.amazonaws.com"},
		{"aws-cn", Bucket{Name: "estate", Region: "cn-north-1"},
			"https://estate.s3.cn-north-1.amazonaws.com.cn"},
		{"aws", Bucket{Name: "estate", Region: "us-east-1", PathStyle: true},
			"https://s3.us-east-1.amazonaws.com"},
	}
	for _, c := range cases {
		o := &Options{Partition: c.partition}
		if got := o.host(c.bucket); got != c.want {
			t.Errorf("%s: host = %s, want %s", c.partition, got, c.want)
		}
	}
	dual := &Options{Partition: "aws", DualStack: true}
	if got := dual.host(Bucket{Name: "estate", Region: "us-east-1"}); !strings.Contains(got, "dualstack") {
		t.Errorf("dual-stack gave %s", got)
	}
}

// A key holding a space or a plus must reach S3 as the key it is.
func TestAKeyIsEncodedForItsURL(t *testing.T) {
	cases := map[string]string{
		"base/top.sls":    "base/top.sls",
		"base/a file.sls": "base/a%20file.sls",
		"base/a+b.sls":    "base/a+b.sls",
		"base/café.sls":   "base/caf%C3%A9.sls",
	}
	for key, want := range cases {
		if got := escapeKey(key); got != want {
			t.Errorf("escapeKey(%q) = %q, want %q", key, got, want)
		}
	}
	// And the escaping is what a URL parser reads back as the key.
	parsed, err := url.Parse("https://example.com/" + escapeKey("base/a file.sls"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/base/a file.sls" {
		t.Errorf("the round trip gave %q", parsed.Path)
	}
}

// A plaintext endpoint is a way to serve an estate whatever the network
// says.
func TestAPlaintextEndpointIsRefused(t *testing.T) {
	_, err := New(Options{
		Buckets:  []Bucket{{Name: "estate", Endpoint: "http://minio.example:9000"}},
		CacheDir: t.TempDir(),
		Provider: &awsauth.Provider{},
	})
	if err == nil {
		t.Fatal("an http endpoint was accepted")
	}
	if !strings.Contains(err.Error(), "plaintext") {
		t.Errorf("the refusal is %v", err)
	}
}

func TestAMisconfiguredBackendIsRefused(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"no buckets", Options{CacheDir: "/tmp", Provider: &awsauth.Provider{}}, "no buckets"},
		{"no cache", Options{Buckets: []Bucket{{Name: "b"}}, Provider: &awsauth.Provider{}}, "cache"},
		{"no provider", Options{Buckets: []Bucket{{Name: "b"}}, CacheDir: "/tmp"}, "credential"},
		{"an unnamed bucket", Options{
			Buckets: []Bucket{{}}, CacheDir: "/tmp", Provider: &awsauth.Provider{},
		}, "needs a name"},
	}
	for _, c := range cases {
		if _, err := New(c.opts); err == nil {
			t.Errorf("%s was accepted", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: the refusal is %v", c.name, err)
		}
	}
}
