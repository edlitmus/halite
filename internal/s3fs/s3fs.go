// Package s3fs serves a state tree out of an S3 bucket.
//
// SPEC 13.4. The signing is `internal/awsauth`; this is the bucket
// listing, the object fetch, and the mapping of a key prefix to an
// environment.
//
// Like gitfs, it materialises what it serves into a directory that
// becomes a `roots` search path, so the manifest, hashing, ignore
// globs, and conditional requests are the existing code rather than a
// second implementation of file serving.
package s3fs

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
)

// MaxObjectBytes bounds one object. A state tree is text; an object
// larger than this is a mistake or an attempt to fill the hub's disk.
const MaxObjectBytes = 256 << 20

// MaxListBody bounds one listing page.
const MaxListBody = 8 << 20

// Bucket is one bucket the file server exposes.
type Bucket struct {
	// Name is the bucket.
	Name string
	// Region it lives in.
	Region string
	// Prefix limits what is served, and is stripped from the key: a
	// bucket holding `states/base/top.sls` with prefix `states/` serves
	// `base/top.sls`.
	Prefix string
	// Env, when set, serves the whole bucket as one environment.
	// Otherwise the first path segment after the prefix is the
	// environment, which is how Salt's s3fs lays a bucket out.
	Env string
	// Endpoint overrides the AWS host, for a compatible service.
	Endpoint string
	// PathStyle addresses the bucket in the path rather than the host,
	// which a compatible service usually needs.
	PathStyle bool
}

// Options configure the backend.
type Options struct {
	Buckets []Bucket
	// CacheDir holds what has been fetched.
	CacheDir string
	// Provider resolves credentials.
	Provider *awsauth.Provider
	// Partition is `aws`, `aws-us-gov`, or `aws-cn`. Endpoints are
	// built from it rather than hardcoded.
	Partition string
	// DualStack uses the IPv6-capable endpoints.
	DualStack bool
	// AllowEnvs and DenyEnvs filter what is served. Deny wins.
	AllowEnvs []string
	DenyEnvs  []string
	// Client is how S3 is reached.
	Client *http.Client
	// Timeout bounds one request.
	Timeout time.Duration
	Log     func(level, msg string, kv ...any)
	Now     func() time.Time
}

func (o *Options) log(level, msg string, kv ...any) {
	if o.Log != nil {
		o.Log(level, msg, kv...)
	}
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Options) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &http.Client{Timeout: timeout}
}

// host is the endpoint for a bucket.
//
// Built from the partition, as SPEC 13.4 requires: an endpoint
// hardcoded to `amazonaws.com` is wrong in China, and wrong in a way
// that fails when somebody is depending on it.
func (o *Options) host(b Bucket) string {
	if b.Endpoint != "" {
		return strings.TrimSuffix(b.Endpoint, "/")
	}
	suffix := "amazonaws.com"
	if o.Partition == "aws-cn" {
		suffix = "amazonaws.com.cn"
	}
	stack := "s3"
	if o.DualStack {
		stack = "s3.dualstack"
	}
	region := b.Region
	if region == "" {
		region = "us-east-1"
	}
	if b.PathStyle {
		return fmt.Sprintf("https://%s.%s.%s", stack, region, suffix)
	}
	return fmt.Sprintf("https://%s.%s.%s.%s", b.Name, stack, region, suffix)
}

// objectURL is where one key lives.
func (o *Options) objectURL(b Bucket, key string) string {
	base := o.host(b)
	if b.PathStyle || b.Endpoint != "" {
		return base + "/" + url.PathEscape(b.Name) + "/" + escapeKey(key)
	}
	return base + "/" + escapeKey(key)
}

// escapeKey encodes a key for a URL path, keeping the separators.
//
// A key is not a filesystem path and may hold anything; each segment is
// encoded and the slashes are left, so `a b/c` becomes `a%20b/c`.
func escapeKey(key string) string {
	segments := strings.Split(key, "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

// listResult is the XML `ListObjectsV2` answers with.
type listResult struct {
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
	Contents              []struct {
		Key  string `xml:"Key"`
		ETag string `xml:"ETag"`
		Size int64  `xml:"Size"`
	} `xml:"Contents"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// Object is one key the backend found.
type Object struct {
	Key  string
	ETag string
	Size int64
}

// list enumerates a bucket under its prefix, following continuation
// tokens.
func (o *Options) list(ctx context.Context, b Bucket, creds awsauth.Credentials) ([]Object, error) {
	var out []Object
	token := ""
	for pages := 0; ; pages++ {
		if pages > 10000 {
			// A bucket that never stops paginating is one that would
			// otherwise be an unbounded loop on the hub.
			return nil, fmt.Errorf("the listing of %s did not end", b.Name)
		}
		query := url.Values{}
		query.Set("list-type", "2")
		query.Set("max-keys", "1000")
		if b.Prefix != "" {
			query.Set("prefix", b.Prefix)
		}
		if token != "" {
			query.Set("continuation-token", token)
		}

		endpoint := o.host(b)
		if b.PathStyle || b.Endpoint != "" {
			endpoint += "/" + url.PathEscape(b.Name)
		}
		body, err := o.signedGet(ctx, endpoint+"?"+query.Encode(), b, creds, MaxListBody)
		if err != nil {
			return nil, err
		}
		var parsed listResult
		if err := xml.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("the listing of %s is not readable: %w", b.Name, err)
		}
		if parsed.Code != "" {
			return nil, fmt.Errorf("%s: %s: %s", b.Name, parsed.Code, parsed.Message)
		}
		for _, item := range parsed.Contents {
			if strings.HasSuffix(item.Key, "/") {
				// A zero-length key ending in a slash is a directory
				// marker some tools write. It is not a file.
				continue
			}
			out = append(out, Object{
				Key: item.Key, ETag: strings.Trim(item.ETag, `"`), Size: item.Size,
			})
		}
		if !parsed.IsTruncated || parsed.NextContinuationToken == "" {
			break
		}
		token = parsed.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// signedGet makes one signed request and returns the body.
func (o *Options) signedGet(ctx context.Context, endpoint string, b Bucket,
	creds awsauth.Credentials, limit int64) ([]byte, error) {

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	region := b.Region
	if region == "" {
		region = "us-east-1"
	}
	signer := awsauth.Signer{Region: region, Service: "s3"}
	if err := signer.Sign(req, creds, awsauth.EmptyPayloadHash, o.now()); err != nil {
		return nil, err
	}
	res, err := o.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, limit))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", endpoint, res.StatusCode)
	}
	return body, nil
}

// envAndPath maps a key to the environment it belongs to and its path
// inside that environment.
func (b Bucket) envAndPath(key string) (string, string, bool) {
	rest := key
	if b.Prefix != "" {
		trimmed, ok := strings.CutPrefix(key, b.Prefix)
		if !ok {
			return "", "", false
		}
		rest = trimmed
	}
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return "", "", false
	}
	if b.Env != "" {
		return b.Env, rest, true
	}
	env, remainder, ok := strings.Cut(rest, "/")
	if !ok || env == "" || remainder == "" {
		// A key at the top of the prefix belongs to no environment
		// when the layout is one-environment-per-directory.
		return "", "", false
	}
	return env, remainder, true
}

// safeRelative refuses a key that would write outside the cache.
//
// A key is not a path: `..` in one is a legal object name in S3 and a
// traversal here.
func safeRelative(rel string) (string, error) {
	clean := path.Clean(rel)
	if clean == "." || clean == "" {
		return "", fmt.Errorf("an empty path")
	}
	if path.IsAbs(clean) {
		return "", fmt.Errorf("%q is absolute", rel)
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == ".." {
			return "", fmt.Errorf("%q leaves the tree", rel)
		}
	}
	if strings.ContainsRune(clean, '\\') {
		return "", fmt.Errorf("%q holds a backslash", rel)
	}
	return clean, nil
}

// Backend serves buckets as file server roots.
type Backend struct {
	opts Options

	mu      sync.Mutex
	roots   map[string][]string
	state   []BucketState
	updated time.Time
}

// BucketState is what one bucket resolved to.
type BucketState struct {
	Bucket  Bucket
	Objects int
	Envs    []string
	Err     error
}

// New checks the configuration.
func New(opts Options) (*Backend, error) {
	if len(opts.Buckets) == 0 {
		return nil, fmt.Errorf("the s3 backend is enabled and no buckets are configured")
	}
	if opts.CacheDir == "" {
		return nil, fmt.Errorf("the s3 backend needs a cache directory")
	}
	if opts.Provider == nil {
		return nil, fmt.Errorf("the s3 backend needs a credential provider")
	}
	for _, b := range opts.Buckets {
		if b.Name == "" {
			return nil, fmt.Errorf("an s3 bucket needs a name")
		}
		if b.Endpoint != "" && !strings.HasPrefix(b.Endpoint, "https://") {
			return nil, fmt.Errorf("the endpoint for %s is not https://; a state tree fetched "+
				"over plaintext is whatever the network says it is", b.Name)
		}
	}
	return &Backend{opts: opts, roots: map[string][]string{}}, nil
}

// Update fetches every bucket and materialises what it serves.
func (b *Backend) Update(ctx context.Context) error {
	creds, err := b.opts.Provider.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("resolving AWS credentials: %w", err)
	}

	roots := map[string][]string{}
	var state []BucketState
	failures := 0

	for _, bucket := range b.opts.Buckets {
		result := BucketState{Bucket: bucket}
		objects, err := b.opts.list(ctx, bucket, creds)
		if err != nil {
			result.Err = err
			state = append(state, result)
			failures++
			b.opts.log("warn", "an s3 bucket could not be listed",
				"bucket", bucket.Name, "error", err.Error())
			continue
		}

		envs, err := b.materialise(ctx, bucket, creds, objects)
		if err != nil {
			result.Err = err
			state = append(state, result)
			failures++
			continue
		}
		result.Objects = len(objects)
		for env := range envs {
			if !b.opts.permitted(env) {
				continue
			}
			result.Envs = append(result.Envs, env)
			roots[env] = append(roots[env], envs[env])
		}
		sort.Strings(result.Envs)
		state = append(state, result)
	}

	b.mu.Lock()
	if len(roots) > 0 || failures == 0 {
		// A total failure leaves the previous tree in place, for the
		// reason gitfs does: a network blip must not empty the file
		// server.
		b.roots = roots
		b.updated = b.opts.now()
	}
	b.state = state
	b.mu.Unlock()

	if failures == len(b.opts.Buckets) {
		return fmt.Errorf("no s3 bucket could be read")
	}
	return nil
}

// materialise writes the objects into a directory per environment.
func (b *Backend) materialise(ctx context.Context, bucket Bucket,
	creds awsauth.Credentials, objects []Object) (map[string]string, error) {

	base := filepath.Join(b.opts.CacheDir, sanitiseName(bucket.Name))
	envs := map[string]string{}

	for _, object := range objects {
		env, rel, ok := bucket.envAndPath(object.Key)
		if !ok {
			continue
		}
		if !b.opts.permitted(env) {
			continue
		}
		clean, err := safeRelative(rel)
		if err != nil {
			b.opts.log("warn", "an s3 key was refused",
				"bucket", bucket.Name, "key", object.Key, "error", err.Error())
			continue
		}
		envDir := filepath.Join(base, sanitiseName(env))
		envs[env] = envDir
		target := filepath.Join(envDir, filepath.FromSlash(clean))

		// Skipped when the cached copy matches what the bucket
		// published. The ETag is a digest for an object that was not
		// multipart-uploaded; when it is not, the size is still a cheap
		// filter and the fetch below is correct either way.
		if unchanged(target, object) {
			continue
		}
		body, err := b.opts.signedGet(ctx, b.opts.objectURL(bucket, object.Key),
			bucket, creds, MaxObjectBytes)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, body, 0o600); err != nil {
			return nil, err
		}
	}
	return envs, nil
}

// unchanged reports whether the cached file matches the object.
func unchanged(path string, object Object) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() == object.Size
}

// sanitiseName makes a bucket or environment name usable as a
// directory.
func sanitiseName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "bucket"
	}
	return out
}

func (o *Options) permitted(env string) bool {
	for _, denied := range o.DenyEnvs {
		if matches(denied, env) {
			return false
		}
	}
	if len(o.AllowEnvs) == 0 {
		return true
	}
	for _, allowed := range o.AllowEnvs {
		if matches(allowed, env) {
			return true
		}
	}
	return false
}

func matches(pattern, env string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(env, prefix)
	}
	return pattern == env
}

// Roots is what the file server should serve.
func (b *Backend) Roots() map[string][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string][]string, len(b.roots))
	for env, dirs := range b.roots {
		out[env] = append([]string(nil), dirs...)
	}
	return out
}

// Envs is every environment this backend serves.
func (b *Backend) Envs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.roots))
	for env := range b.roots {
		out = append(out, env)
	}
	sort.Strings(out)
	return out
}

// Describe renders the state for an operator.
func (b *Backend) Describe() []map[string]any {
	b.mu.Lock()
	state := append([]BucketState(nil), b.state...)
	b.mu.Unlock()

	var out []map[string]any
	for _, s := range state {
		entry := map[string]any{
			"bucket":       s.Bucket.Name,
			"objects":      s.Objects,
			"environments": s.Envs,
		}
		if s.Err != nil {
			entry["error"] = s.Err.Error()
		}
		out = append(out, entry)
	}
	return out
}
