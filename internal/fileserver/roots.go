// Package fileserver resolves halite:// and salt:// paths against
// configured roots.
//
// Path containment is the security property this package exists to
// guarantee. Salt's CVE-2020-11652 was a directory traversal in exactly
// this code path, so every request resolves the path and confirms the
// result is inside the configured root *after* symlink resolution, and the
// property is covered by a test asserting that no input escapes. SPEC
// section 13.5.
package fileserver

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/template"
)

// ErrOutsideRoot is returned when a requested path resolves outside every
// configured root.
var ErrOutsideRoot = errors.New("path resolves outside the configured root")

// Roots is a roots-backed file server: environment names to an ordered
// list of directories, searched in order with the first match winning.
type Roots struct {
	// Dirs maps an environment to its ordered search path.
	//
	// Read through `dirsFor`, never directly, because a backend that
	// fetches — gitfs — replaces it while handlers are serving.
	Dirs map[string][]string
	// mu guards Dirs. Zero-valued and unused by a file server whose
	// roots never change, which is every one that serves only `roots`.
	mu sync.RWMutex
	// FollowSymlinks permits a symlink inside a served tree to be
	// followed. A symlink pointing outside the root is refused whatever
	// this says.
	FollowSymlinks bool
	// IgnoreGlobs hides matching paths from listing and from fetching.
	IgnoreGlobs []string
	// IgnoreRegexes does the same with RE2 patterns, for the shapes a
	// glob cannot express. SPEC 13.5 requires both, and both apply to
	// every backend.
	IgnoreRegexes []*regexp.Regexp
}

// SetIgnoreRegexes compiles the patterns, reporting the one that is
// wrong rather than silently hiding nothing.
//
// A file server that quietly ignores a malformed hide rule serves the
// files it was told to hide, which is the failure that matters here.
func (r *Roots) SetIgnoreRegexes(patterns []string) error {
	var compiled []*regexp.Regexp
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("file_ignore_regex %q: %w", pattern, err)
		}
		compiled = append(compiled, re)
	}
	r.IgnoreRegexes = compiled
	return nil
}

// NewRoots builds a file server over the given roots.
func NewRoots(dirs map[string][]string) *Roots {
	return &Roots{Dirs: dirs}
}

// Envs lists the environments, with the default first and the rest sorted,
// so that compilation order is stable.
func (r *Roots) Envs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.Dirs))
	for env := range r.Dirs {
		out = append(out, env)
	}
	sort.Strings(out)
	for i, e := range out {
		if e == "base" && i != 0 {
			out = append([]string{"base"}, append(out[:i:i], out[i+1:]...)...)
			break
		}
	}
	return out
}

// Resolve turns a relative path into an absolute one inside a root,
// searching the environment's directories in order.
//
// The returned path is guaranteed to be inside the root it was found in,
// with symlinks resolved.
func (r *Roots) Resolve(env, rel string) (string, error) {
	dirs, ok := r.dirsFor(env)
	if !ok {
		return "", fmt.Errorf("environment %q has no roots configured", env)
	}
	clean := path.Clean("/" + strings.ReplaceAll(rel, "\\", "/"))
	if r.ignored(clean) {
		return "", fs.ErrNotExist
	}

	var lastErr error = fs.ErrNotExist
	for _, dir := range dirs {
		full, err := containedPath(dir, clean, r.FollowSymlinks)
		if err != nil {
			if errors.Is(err, ErrOutsideRoot) {
				// A traversal attempt is reported rather than quietly
				// falling through to the next root.
				return "", err
			}
			lastErr = err
			continue
		}
		if _, err := os.Stat(full); err != nil {
			lastErr = err
			continue
		}
		return full, nil
	}
	return "", lastErr
}

// containedPath joins a root and a cleaned relative path and confirms the
// result is inside the root after symlink resolution.
func containedPath(root, cleanRel string, followSymlinks bool) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// A root that is itself a symlink is resolved once, so that the
	// containment comparison is between two real paths.
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	}

	joined := filepath.Join(absRoot, filepath.FromSlash(cleanRel))
	if !within(absRoot, joined) {
		return "", fmt.Errorf("%w: %s", ErrOutsideRoot, cleanRel)
	}

	// The lexical check above stops `../` traversal. This second check
	// stops a symlink inside the tree from pointing out of it, which the
	// lexical check cannot see.
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		if os.IsNotExist(err) {
			// A path that does not exist yet cannot escape; the caller's
			// Stat reports the absence.
			return joined, nil
		}
		return "", err
	}
	if real != joined && !followSymlinks {
		// The path is a symlink and following is disabled.
		if !within(absRoot, real) {
			return "", fmt.Errorf("%w: %s", ErrOutsideRoot, cleanRel)
		}
		return "", fmt.Errorf("%s is a symlink and fileserver_follow_symlinks is false", cleanRel)
	}
	if !within(absRoot, real) {
		return "", fmt.Errorf("%w: %s resolves to %s", ErrOutsideRoot, cleanRel, real)
	}
	return joined, nil
}

// within reports whether p is root or is inside it, comparing whole path
// components so that /srv/salt-other is not treated as inside /srv/salt.
func within(root, p string) bool {
	if p == root {
		return true
	}
	return strings.HasPrefix(p, root+string(filepath.Separator))
}

func (r *Roots) ignored(clean string) bool {
	for _, g := range r.IgnoreGlobs {
		if ok, err := path.Match(g, strings.TrimPrefix(clean, "/")); err == nil && ok {
			return true
		}
		if ok, err := path.Match(g, path.Base(clean)); err == nil && ok {
			return true
		}
	}
	rel := strings.TrimPrefix(clean, "/")
	for _, re := range r.IgnoreRegexes {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

// Read returns the contents of a path in an environment.
func (r *Roots) Read(env, rel string) ([]byte, string, error) {
	full, err := r.Resolve(env, rel)
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, "", err
	}
	return b, full, nil
}

// ---- state.Loader ----

// slsCandidates lists the file names a dotted SLS name may live in, in the
// order Salt searches them.
func slsCandidates(sls string) []string {
	rel := strings.ReplaceAll(sls, ".", "/")
	return []string{rel + ".sls", rel + "/init.sls"}
}

// Source implements state.Loader: it resolves a dotted SLS name to its
// bytes.
func (r *Roots) Source(env, sls string) ([]byte, string, error) {
	for _, candidate := range slsCandidates(sls) {
		b, full, err := r.Read(env, candidate)
		if err == nil {
			return b, full, nil
		}
		if errors.Is(err, ErrOutsideRoot) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("%w: %s in environment %s", state.ErrNotFound, sls, env)
}

// Templates implements state.Loader: template includes and imports address
// files by path within the environment.
func (r *Roots) Templates(env string) template.Loader {
	return envTemplates{r: r, env: env}
}

type envTemplates struct {
	r   *Roots
	env string
}

// Load implements template.Loader.
func (e envTemplates) Load(name string) (string, string, error) {
	// A template include may be written as a path, as a salt:// URI, or as
	// a dotted SLS name; all three appear in real trees.
	for _, candidate := range templateCandidates(name) {
		b, full, err := e.r.Read(e.env, candidate)
		if err == nil {
			return string(b), full, nil
		}
		if errors.Is(err, ErrOutsideRoot) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("%w: %s", template.ErrNotFound, name)
}

func templateCandidates(name string) []string {
	rel := StripScheme(name)
	out := []string{rel}
	if !strings.Contains(rel, "/") && strings.Contains(rel, ".") && !strings.HasSuffix(rel, ".sls") {
		out = append(out, slsCandidates(rel)...)
	}
	if !strings.HasSuffix(rel, ".sls") {
		out = append(out, rel+".sls")
	}
	return out
}

// StripScheme removes a halite:// or salt:// prefix and any ?env= query.
//
// salt:// is accepted permanently rather than deprecated, because it
// appears in tens of thousands of lines of existing SLS and there is no
// value in churning it. SPEC section 13.1.
func StripScheme(uri string) string {
	s := uri
	for _, scheme := range []string{"halite://", "salt://"} {
		if strings.HasPrefix(s, scheme) {
			s = s[len(scheme):]
			break
		}
	}
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	return s
}

// EnvFromURI reads the environment out of a halite:// or salt:// query
// string. Both `env` and `saltenv` are accepted.
func EnvFromURI(uri, def string) string {
	i := strings.IndexByte(uri, '?')
	if i < 0 {
		return def
	}
	for _, pair := range strings.Split(uri[i+1:], "&") {
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		if k == "env" || k == "saltenv" {
			return v
		}
	}
	return def
}

// IsManagedURI reports whether a source string names a file this server
// serves, as opposed to an http:// or a local path.
func IsManagedURI(uri string) bool {
	return strings.HasPrefix(uri, "halite://") || strings.HasPrefix(uri, "salt://")
}

// List walks an environment's roots and returns every file path, relative
// and slash-separated, sorted and de-duplicated.
func (r *Roots) List(env string) ([]string, error) {
	dirs, ok := r.dirsFor(env)
	if !ok {
		return nil, fmt.Errorf("environment %q has no roots configured", env)
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		absRoot, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		err = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(absRoot, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if r.ignored("/" + rel) {
				return nil
			}
			if seen[rel] {
				return nil
			}
			seen[rel] = true
			out = append(out, rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// dirsFor is the search path for an environment.
func (r *Roots) dirsFor(env string) ([]string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	dirs, ok := r.Dirs[env]
	return dirs, ok
}

// SetDirs replaces the whole search path.
//
// Wholesale rather than merged, because a branch that has gone away
// must stop being served: an update that only added would keep serving
// an environment nobody has any more.
func (r *Roots) SetDirs(dirs map[string][]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Dirs = dirs
}

// SnapshotDirs is the current search path, copied.
func (r *Roots) SnapshotDirs() map[string][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string][]string, len(r.Dirs))
	for env, list := range r.Dirs {
		out[env] = append([]string(nil), list...)
	}
	return out
}
