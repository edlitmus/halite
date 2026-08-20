package fileserver

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/state"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPathContainment is the property this package exists to guarantee.
// Salt's CVE-2020-11652 was a directory traversal in exactly this code
// path, so no input may escape the configured root. SPEC section 13.5.
func TestPathContainment(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "srv", "salt")
	write(t, filepath.Join(root, "inside.txt"), "served\n")
	write(t, filepath.Join(tmp, "secret.txt"), "not served\n")

	r := NewRoots(map[string][]string{"base": {root}})

	// The file inside is served.
	if _, err := r.Resolve("base", "inside.txt"); err != nil {
		t.Fatalf("a file inside the root was not served: %v", err)
	}

	// Nothing outside is, however it is spelled.
	escapes := []string{
		"../secret.txt",
		"../../secret.txt",
		"./../secret.txt",
		"sub/../../secret.txt",
		"/../secret.txt",
		"....//secret.txt",
		"..%2fsecret.txt",
		`..\secret.txt`,
		"/etc/passwd",
		"//etc/passwd",
	}
	for _, p := range escapes {
		got, err := r.Resolve("base", p)
		if err == nil {
			t.Errorf("%q resolved to %q; it must not escape the root", p, got)
			continue
		}
		// Whatever the failure, the resolved path must never be outside.
		if got != "" && !strings.HasPrefix(got, root) {
			t.Errorf("%q leaked a path outside the root: %q", p, got)
		}
	}
}

// TestSymlinkContainment covers what a lexical check cannot see: a link
// inside the tree that points out of it.
func TestSymlinkContainment(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "srv")
	write(t, filepath.Join(root, "real.txt"), "fine\n")
	write(t, filepath.Join(tmp, "outside.txt"), "secret\n")

	if err := os.Symlink(filepath.Join(tmp, "outside.txt"), filepath.Join(root, "escape.txt")); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}
	if err := os.Symlink("real.txt", filepath.Join(root, "internal.txt")); err != nil {
		t.Fatal(err)
	}

	r := NewRoots(map[string][]string{"base": {root}})

	// A link out of the tree is refused whatever the setting.
	if _, err := r.Resolve("base", "escape.txt"); err == nil {
		t.Error("a symlink pointing outside the root must be refused")
	}
	r.FollowSymlinks = true
	got, err := r.Resolve("base", "escape.txt")
	if err == nil {
		t.Errorf("a symlink pointing outside the root must be refused even with following enabled, got %q", got)
	}
	if !errors.Is(err, ErrOutsideRoot) {
		t.Errorf("the refusal should say why: %v", err)
	}

	// A link within the tree is served only when following is on.
	if _, err := r.Resolve("base", "internal.txt"); err != nil {
		t.Errorf("an internal symlink should be served when following is on: %v", err)
	}
	r.FollowSymlinks = false
	if _, err := r.Resolve("base", "internal.txt"); err == nil {
		t.Error("an internal symlink should be refused when following is off")
	}
}

func TestRootItselfMayBeASymlink(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "real-root")
	write(t, filepath.Join(real, "f.txt"), "x\n")
	link := filepath.Join(tmp, "link-root")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	r := NewRoots(map[string][]string{"base": {link}})
	if _, err := r.Resolve("base", "f.txt"); err != nil {
		t.Errorf("a root that is itself a symlink should still serve: %v", err)
	}
}

func TestNeighbouringDirectoryIsNotInsideTheRoot(t *testing.T) {
	// /srv/salt-other must not count as inside /srv/salt, which a naive
	// string prefix check would allow.
	tmp := t.TempDir()
	root := filepath.Join(tmp, "salt")
	write(t, filepath.Join(root, "ok.txt"), "x\n")
	write(t, filepath.Join(tmp, "salt-other", "leak.txt"), "y\n")

	r := NewRoots(map[string][]string{"base": {root}})
	if _, err := r.Resolve("base", "../salt-other/leak.txt"); err == nil {
		t.Error("a neighbouring directory must not be reachable")
	}
}

func TestMultipleRootsSearchInOrder(t *testing.T) {
	tmp := t.TempDir()
	first := filepath.Join(tmp, "first")
	second := filepath.Join(tmp, "second")
	write(t, filepath.Join(first, "shared.txt"), "from first\n")
	write(t, filepath.Join(second, "shared.txt"), "from second\n")
	write(t, filepath.Join(second, "only.txt"), "only in second\n")

	r := NewRoots(map[string][]string{"base": {first, second}})

	b, _, err := r.Read("base", "shared.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "from first\n" {
		t.Errorf("the first root should win, got %q", b)
	}
	b, _, err = r.Read("base", "only.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "only in second\n" {
		t.Errorf("the second root should be searched, got %q", b)
	}
}

func TestSLSNameResolution(t *testing.T) {
	tmp := t.TempDir()
	write(t, filepath.Join(tmp, "web.sls"), "flat\n")
	write(t, filepath.Join(tmp, "db", "init.sls"), "package\n")
	write(t, filepath.Join(tmp, "deep", "nested", "leaf.sls"), "leaf\n")

	r := NewRoots(map[string][]string{"base": {tmp}})
	cases := map[string]string{
		"web":              "flat\n",
		"db":               "package\n",
		"deep.nested.leaf": "leaf\n",
	}
	for sls, want := range cases {
		b, _, err := r.Source("base", sls)
		if err != nil {
			t.Errorf("%s: %v", sls, err)
			continue
		}
		if string(b) != want {
			t.Errorf("%s = %q, want %q", sls, b, want)
		}
	}

	_, _, err := r.Source("base", "absent")
	if !errors.Is(err, state.ErrNotFound) {
		t.Errorf("a missing SLS should report ErrNotFound, got %v", err)
	}
}

func TestURISchemeHandling(t *testing.T) {
	cases := []struct{ uri, want string }{
		{"salt://web/nginx.conf", "web/nginx.conf"},
		{"halite://web/nginx.conf", "web/nginx.conf"},
		{"salt://web/nginx.conf?saltenv=prod", "web/nginx.conf"},
		{"halite://web/nginx.conf?env=prod", "web/nginx.conf"},
		{"/plain/path", "/plain/path"},
	}
	for _, c := range cases {
		if got := StripScheme(c.uri); got != c.want {
			t.Errorf("StripScheme(%q) = %q, want %q", c.uri, got, c.want)
		}
	}

	// Both query spellings select an environment; salt:// is permanent,
	// not deprecated. SPEC section 13.1.
	if got := EnvFromURI("salt://f?saltenv=prod", "base"); got != "prod" {
		t.Errorf("saltenv = %q", got)
	}
	if got := EnvFromURI("halite://f?env=staging", "base"); got != "staging" {
		t.Errorf("env = %q", got)
	}
	if got := EnvFromURI("salt://f", "base"); got != "base" {
		t.Errorf("default = %q", got)
	}
	if got := EnvFromURI("salt://f?other=1", "base"); got != "base" {
		t.Errorf("unrelated query = %q", got)
	}

	if !IsManagedURI("salt://x") || !IsManagedURI("halite://x") {
		t.Error("a managed URI was not recognised")
	}
	if IsManagedURI("http://x") || IsManagedURI("/local") {
		t.Error("an unmanaged path was treated as managed")
	}
}

func TestIgnoreGlobs(t *testing.T) {
	tmp := t.TempDir()
	write(t, filepath.Join(tmp, "keep.sls"), "x\n")
	write(t, filepath.Join(tmp, "notes.swp"), "y\n")
	write(t, filepath.Join(tmp, "sub", "notes.swp"), "z\n")

	r := NewRoots(map[string][]string{"base": {tmp}})
	r.IgnoreGlobs = []string{"*.swp"}

	if _, err := r.Resolve("base", "keep.sls"); err != nil {
		t.Errorf("an unignored file should be served: %v", err)
	}
	if _, err := r.Resolve("base", "notes.swp"); err == nil {
		t.Error("an ignored file should not be served")
	}
	if _, err := r.Resolve("base", "sub/notes.swp"); err == nil {
		t.Error("an ignored file should not be served from a subdirectory")
	}

	names, err := r.List("base")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if strings.HasSuffix(n, ".swp") {
			t.Errorf("List returned an ignored file: %s", n)
		}
	}
}

func TestListIsSortedAndDeduplicated(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a")
	b := filepath.Join(tmp, "b")
	write(t, filepath.Join(a, "shared.sls"), "1\n")
	write(t, filepath.Join(b, "shared.sls"), "2\n")
	write(t, filepath.Join(b, "only.sls"), "3\n")

	r := NewRoots(map[string][]string{"base": {a, b}})
	names, err := r.List("base")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("names = %v, want the union without duplicates", names)
	}
	if names[0] != "only.sls" || names[1] != "shared.sls" {
		t.Errorf("names = %v, want them sorted", names)
	}
}

func TestEnvsPutsBaseFirst(t *testing.T) {
	r := NewRoots(map[string][]string{"prod": {"/x"}, "base": {"/y"}, "dev": {"/z"}})
	envs := r.Envs()
	if envs[0] != "base" {
		t.Errorf("envs = %v; base leads so compilation order is stable", envs)
	}
	if len(envs) != 3 {
		t.Errorf("envs = %v", envs)
	}
}

func TestUnknownEnvironmentIsNamed(t *testing.T) {
	r := NewRoots(map[string][]string{"base": {"/x"}})
	_, err := r.Resolve("nosuch", "f")
	if err == nil || !strings.Contains(err.Error(), "nosuch") {
		t.Errorf("err = %v", err)
	}
	if _, err := r.List("nosuch"); err == nil {
		t.Error("listing an unknown environment should be an error")
	}
}

func TestTemplateLoaderResolvesTheSpellingsATreeUses(t *testing.T) {
	tmp := t.TempDir()
	write(t, filepath.Join(tmp, "macros.jinja"), "macro body\n")
	write(t, filepath.Join(tmp, "web", "part.sls"), "part body\n")

	r := NewRoots(map[string][]string{"base": {tmp}})
	l := r.Templates("base")

	for _, name := range []string{"macros.jinja", "salt://macros.jinja", "web/part.sls", "web/part"} {
		if _, _, err := l.Load(name); err != nil {
			t.Errorf("%q did not resolve: %v", name, err)
		}
	}
	if _, _, err := l.Load("../outside"); err == nil {
		t.Error("a traversing template name must be refused")
	}
	if _, _, err := l.Load("absent"); err == nil {
		t.Error("a missing template must be an error")
	}
}

func TestFetcherResolvesAndHashes(t *testing.T) {
	tmp := t.TempDir()
	write(t, filepath.Join(tmp, "app.conf"), "port = 80\n")
	prod := filepath.Join(tmp, "prod")
	write(t, filepath.Join(prod, "app.conf"), "port = 443\n")

	r := NewRoots(map[string][]string{"base": {tmp}, "prod": {prod}})
	f := NewFetcher(r)

	path, err := f.Fetch("base", "salt://app.conf")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "port = 80\n" {
		t.Errorf("fetched %q", b)
	}

	// The query string selects the environment, overriding the caller's.
	path, err = f.Fetch("base", "salt://app.conf?saltenv=prod")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "port = 443\n" {
		t.Errorf("environment override fetched %q", b)
	}

	algorithm, digest, err := f.Hash("base", "salt://app.conf")
	if err != nil {
		t.Fatal(err)
	}
	if algorithm != "sha256" || len(digest) != 64 {
		t.Errorf("hash = %s %s", algorithm, digest)
	}

	if !f.Exists("base", "salt://app.conf") {
		t.Error("Exists = false for a served file")
	}
	if f.Exists("base", "salt://absent.conf") {
		t.Error("Exists = true for a missing file")
	}
	// A fetch that escapes the root is refused, and says which
	// environment it was looking in.
	if _, err := f.Fetch("base", "salt://../escape"); err == nil {
		t.Error("a traversing fetch must be refused")
	}
}

func TestHashAlgorithms(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "f")
	write(t, p, "abc")

	// The known digests of "abc", so a change to the hashing is caught.
	want := map[string]string{
		"sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		"sha512": "ddaf35a193617abacc417349ae20413112e6fa4e89a97ea20a9eeee64b55d39a" +
			"2192992a274fc1a836ba3c23a3feebbd454d4423643ce80e2a9ac94fa54ca49f",
	}
	for algorithm, digest := range want {
		got, err := hashOf(p, algorithm)
		if err != nil {
			t.Errorf("%s: %v", algorithm, err)
			continue
		}
		if got != digest {
			t.Errorf("%s = %s, want %s", algorithm, got, digest)
		}
	}
	if _, err := hashOf(p, "md5"); err == nil {
		t.Error("md5 is not offered for content comparison and should be refused")
	}
	if _, err := hashOf(filepath.Join(tmp, "absent"), "sha256"); err == nil {
		t.Error("hashing a missing file should be an error")
	}
}
