// Package gitfs serves a state tree out of a git repository.
//
// SPEC 13.3 implements this by invoking the system `git` binary rather
// than linking pygit2 or libgit2 — together a large C dependency with
// its own CVE history — so the estate inherits the operating system's
// git patching cadence. The same argument SPEC 21.1 makes for using the
// system `ssh` binary in agentless mode.
//
// The shape is deliberate: a bare mirror is fetched and verified, and
// the ref that is served is materialised into a directory. Everything
// downstream — the manifest, hashing, ignore globs, conditional
// requests, ranges — is the `roots` backend's code, unchanged. A gitfs
// that served blobs through its own path would be a second
// implementation of file serving, and the second one is the one with
// the traversal bug in it.
package gitfs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Remote is one repository the file server exposes.
type Remote struct {
	// URL is what `git fetch` is given.
	URL string
	// Name identifies it in logs and in the cache. Empty takes a digest
	// of the URL, which is what SPEC 13.3 names the mirror directory
	// by.
	Name string
	// Root is a subdirectory inside the repository to serve, for a
	// repository that holds the tree under `salt/` or similar.
	Root string
	// Base is the branch that becomes the `base` environment. Empty
	// takes the backend's.
	Base string
	// RefTypes limits what is exposed: `branches`, `tags`, or both.
	// Empty takes the backend's.
	RefTypes []string
	// Insecure permits an http:// or git:// URL. Off by default: an
	// unauthenticated, unencrypted transport is a way to serve an
	// estate whatever the network says.
	Insecure bool
}

// mirrorDir is the directory this remote is mirrored into.
//
// A digest of the URL, as SPEC 13.3 says, because a URL is not a usable
// directory name and two remotes may share a repository name.
func (r Remote) mirrorDir() string {
	sum := sha256.Sum256([]byte(r.URL))
	return hex.EncodeToString(sum[:])[:32]
}

// label is what this remote is called in a message.
func (r Remote) label() string {
	if r.Name != "" {
		return r.Name
	}
	return r.URL
}

// Ref is one branch or tag, and the environment it maps to.
type Ref struct {
	// Name is the short ref: `main`, `v1.2.0`.
	Name string
	// Env is the environment it is served as.
	Env string
	// Commit is the object the ref points at.
	Commit string
	// Tag records that this came from a tag rather than a branch,
	// which decides whether a signature is checked with
	// `git verify-tag` or `git verify-commit`.
	Tag bool
}

// Options configure the backend.
type Options struct {
	// Remotes are the repositories to serve.
	Remotes []Remote
	// CacheDir holds the mirrors and the materialised trees. SPEC 13.3
	// puts it under `/var/cache/halite/gitfs`.
	CacheDir string
	// Base is the branch that becomes `base` when a remote does not
	// name its own.
	Base string
	// RefTypes limits what is exposed. Empty takes branches only,
	// which is what an estate almost always wants: every tag becoming
	// an environment turns a release history into a file server.
	RefTypes []string
	// AllowEnvs and DenyEnvs filter the environments. Deny wins.
	AllowEnvs []string
	DenyEnvs  []string
	// VerifySignatures requires the served ref's tip to carry a
	// signature from a key in Keyring. A ref that fails is not served.
	VerifySignatures bool
	// Keyring is the GnuPG home holding the trusted keys. Verification
	// with no keyring would check against whatever the hub's own user
	// happens to trust, which is not a configuration decision anybody
	// made.
	Keyring string
	// Git is the binary. Empty takes `git` from the path.
	Git string
	// Timeout bounds one git invocation.
	Timeout time.Duration
	// Env adds variables to git's environment, for credentials that
	// must not appear in a command line. SPEC 13.3.
	Env []string
	// Log receives what the backend wants an operator to know.
	Log func(level, msg string, kv ...any)
	// Now is the clock, for the tests.
	Now func() time.Time
}

func (o *Options) log(level, msg string, kv ...any) {
	if o.Log != nil {
		o.Log(level, msg, kv...)
	}
}

func (o *Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 5 * time.Minute
	}
	return o.Timeout
}

func (o *Options) git() string {
	if o.Git == "" {
		return "git"
	}
	return o.Git
}

func (o *Options) base() string {
	if o.Base == "" {
		return "main"
	}
	return o.Base
}

func (o *Options) refTypes() []string {
	if len(o.RefTypes) == 0 {
		return []string{"branches"}
	}
	return o.RefTypes
}

// envFor maps a ref name to an environment name.
//
// The base branch becomes `base`; everything else keeps its own name
// with the characters an environment cannot hold replaced. A branch
// called `feature/thing` becomes `feature-thing`, because an
// environment name reaches a directory name and a URL path, and a
// slash in either is a different thing entirely.
func envFor(ref, base string) string {
	if ref == base {
		return "base"
	}
	var b strings.Builder
	for _, r := range ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	// A name that is only separators, or that reads as a path segment
	// with meaning, is not an environment.
	name := strings.Trim(b.String(), "-.")
	if name == "" || name == "." || name == ".." {
		return ""
	}
	return name
}

// permitted reports whether an environment passes the allow and deny
// lists. Deny wins, because a denial is the answer somebody wrote down
// deliberately.
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

// matches is an exact name or a trailing `*`, which is what Salt's
// allowlists accept.
func matches(pattern, env string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.HasPrefix(env, prefix)
	}
	return pattern == env
}

// sortedEnvs is every environment a set of refs maps to, in order.
func sortedEnvs(refs []Ref) []string {
	seen := map[string]bool{}
	var out []string
	for _, ref := range refs {
		if seen[ref.Env] {
			continue
		}
		seen[ref.Env] = true
		out = append(out, ref.Env)
	}
	sort.Strings(out)
	return out
}

// checkURL refuses a transport that would let the network decide what
// an estate applies.
func checkURL(remote Remote) error {
	if remote.URL == "" {
		return fmt.Errorf("a gitfs remote needs a URL")
	}
	switch {
	case strings.HasPrefix(remote.URL, "https://"),
		strings.HasPrefix(remote.URL, "ssh://"),
		strings.HasPrefix(remote.URL, "file://"),
		isLocalPath(remote.URL, runtime.GOOS == "windows"):
		return nil
	case isSCPStyle(remote.URL):
		return nil
	}
	if remote.Insecure {
		return nil
	}
	return fmt.Errorf("%s is not an encrypted transport; a state tree fetched over it is "+
		"whatever the network says it is. Set `insecure: true` on the remote to accept that",
		remote.URL)
}

// isLocalPath reports whether a URL is a path on this machine's own
// filesystem, which needs no transport and so needs no encryption.
//
// It takes the platform rather than reading runtime.GOOS, so that both
// conventions can be checked from one host — the same reason the
// configuration layout does.
//
// The check used to be "starts with a slash", which is a local path on
// unix and nothing at all on Windows. A hub there configured with
// `C:\srv\states` was told its own disk "is not an encrypted transport".
//
// A UNC path is deliberately not local. `\\server\share` is SMB over the
// network, which is exactly the kind of transport this function exists
// to refuse; it needs `insecure: true` like any other.
func isLocalPath(url string, windows bool) bool {
	if !windows {
		return strings.HasPrefix(url, "/")
	}
	if strings.HasPrefix(url, `\\`) || strings.HasPrefix(url, "//") {
		return false
	}
	// A drive-relative or root-relative path: `\srv\states`.
	if strings.HasPrefix(url, `\`) || strings.HasPrefix(url, "/") {
		return true
	}
	// A drive-qualified path: `C:\srv\states`, or `C:/srv/states`,
	// which git accepts too.
	if len(url) >= 3 && url[1] == ':' && isDriveLetter(url[0]) {
		return url[2] == '\\' || url[2] == '/'
	}
	return false
}

func isDriveLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// isSCPStyle reports whether a URL is git's `git@host:path` form, which
// is ssh and therefore encrypted.
//
// The `@` has to come before the `:`, which it does in every scp-style
// URL and does not in a Windows path. Without that ordering,
// `C:\Users\some.name@corp\states` was read as an ssh remote and handed
// to git as one: a local directory would have been resolved over the
// network, against a host named after a drive letter.
func isSCPStyle(url string) bool {
	at := strings.Index(url, "@")
	colon := strings.Index(url, ":")
	return at > 0 && colon > at
}
