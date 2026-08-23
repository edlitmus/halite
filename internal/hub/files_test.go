package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/fileserver"
)

// withFiles gives the lab a state tree to serve.
func (l *lab) withFiles(t *testing.T, files map[string]string) *lab {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l.server.Files = fileserver.NewRoots(map[string][]string{"base": {root}})
	l.root = root
	return l
}

func TestTheHubServesItsTreeAndTheNodeCachesIt(t *testing.T) {
	l := newLab(t).withFiles(t, map[string]string{
		"top.sls":        "base:\n  '*':\n    - web\n",
		"web.sls":        "nginx:\n  pkg.installed: []\n",
		"web/nginx.conf": "worker_processes 4;\n",
	})
	node := l.enrolled(t, "web1.example")
	remote := fileserver.NewRemote(node, t.TempDir(), []string{"base"})

	m, err := remote.ManifestFor("base", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 3 {
		t.Fatalf("the manifest lists %d files: %+v", len(m.Files), m.Files)
	}
	for _, f := range m.Files {
		if f.Hash == "" || f.Size == 0 {
			t.Errorf("%s has no hash or no size: %+v", f.Path, f)
		}
	}

	// The compiler's view.
	src, path, err := remote.Source("base", "web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "nginx") {
		t.Errorf("web.sls came back as %q", src)
	}
	if !strings.Contains(path, "web") {
		t.Errorf("the reported path is %q, and a diagnostic has to name the file", path)
	}

	// A module's view: a local path to the cached copy.
	cached, err := remote.Fetch("base", "salt://web/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(cached)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "worker_processes 4;\n" {
		t.Errorf("the cached file holds %q", body)
	}

	// A second fetch is a conditional request, and the contents are
	// still right.
	again, err := remote.Fetch("base", "salt://web/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	if again != cached {
		t.Errorf("the second fetch cached to %s and the first to %s", again, cached)
	}
	body, _ = os.ReadFile(again)
	if string(body) != "worker_processes 4;\n" {
		t.Errorf("after a conditional request the cached file holds %q", body)
	}

	// A file that is not served is not found, and says so without
	// leaking whether it exists elsewhere.
	if _, err := remote.Fetch("base", "salt://absent.conf"); err == nil {
		t.Error("a file that is not in the tree was fetched")
	}
}

// Salt's CVE-2020-11652 was a traversal in this code path. SPEC 13.5
// requires every request to be checked, the manifest included.
func TestTheFileServerRefusesEveryEscape(t *testing.T) {
	l := newLab(t).withFiles(t, map[string]string{"web.sls": "x\n"})
	secret := filepath.Join(filepath.Dir(l.root), "secret")
	if err := os.WriteFile(secret, []byte("not yours\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	node := l.enrolled(t, "web1.example")

	for _, escape := range []string{
		"../secret",
		"../../etc/passwd",
		"web/../../secret",
		"./../secret",
		"/etc/passwd",
		"..%2fsecret",
	} {
		body, _, _, err := node.FetchFile(context.Background(), "base", escape, "")
		if err == nil && strings.Contains(string(body), "not yours") {
			t.Errorf("%q read a file outside the root", escape)
		}
		if err == nil {
			t.Errorf("%q was served (%d bytes)", escape, len(body))
		}
	}

	// The manifest takes a prefix and it goes through the same
	// resolver, which clamps a `..` to the root rather than refusing
	// it. What matters is the result: nothing outside the root is ever
	// listed.
	for _, prefix := range []string{"../", "..", "../../", "/"} {
		raw, err := node.FileManifest(context.Background(), "base", prefix)
		if err != nil {
			continue
		}
		if strings.Contains(string(raw), "secret") {
			t.Errorf("a manifest with prefix %q listed something outside the root: %s", prefix, raw)
		}
	}
}

// A node caches under its own directory, and a hub that has been
// compromised must not be able to write outside it.
func TestTheCacheRefusesAPathThatWouldEscapeIt(t *testing.T) {
	remote := fileserver.NewRemote(nil, t.TempDir(), []string{"base"})
	for _, bad := range []string{"../escape", "../../etc/passwd", "a/../../b"} {
		if _, err := remote.Fetch("base", "salt://"+bad); err == nil {
			t.Errorf("%q was cached", bad)
		}
	}
	if _, err := remote.Fetch("../etc", "salt://x"); err == nil {
		t.Error("an environment that is a path was accepted")
	}
}

// An unenrolled peer cannot read the tree: the file server is behind
// the same certificate as everything else.
func TestTheTreeNeedsACertificate(t *testing.T) {
	l := newLab(t).withFiles(t, map[string]string{"web.sls": "x\n"})
	stranger, _ := l.node(t)
	if _, _, _, err := stranger.FetchFile(context.Background(), "base", "web.sls", ""); err == nil {
		t.Fatal("a peer with no certificate read the tree")
	}
}

// SPEC 13.5: both hide settings apply to every backend, and a
// malformed one is reported rather than quietly hiding nothing — a
// file server that ignores a bad hide rule serves what it was told to
// hide.
func TestTheHideSettingsApplyToListingAndToFetching(t *testing.T) {
	l := newLab(t).withFiles(t, map[string]string{
		"web.sls":            "x\n",
		"secrets/token.txt":  "hunter2\n",
		"web/nginx.conf.bak": "old\n",
	})
	l.server.Files.IgnoreGlobs = []string{"*.bak"}
	if err := l.server.Files.SetIgnoreRegexes([]string{`^secrets/`}); err != nil {
		t.Fatal(err)
	}
	node := l.enrolled(t, "web1.example")

	raw, err := node.FileManifest(context.Background(), "base", "")
	if err != nil {
		t.Fatal(err)
	}
	listing := string(raw)
	if strings.Contains(listing, "token.txt") {
		t.Errorf("file_ignore_regex did not hide a path from the listing: %s", listing)
	}
	if strings.Contains(listing, ".bak") {
		t.Errorf("file_ignore_glob did not hide a path from the listing: %s", listing)
	}
	if !strings.Contains(listing, "web.sls") {
		t.Errorf("the listing hid everything: %s", listing)
	}

	// Hidden from fetching too, not only from the listing.
	for _, hidden := range []string{"secrets/token.txt", "web/nginx.conf.bak"} {
		body, _, _, err := node.FetchFile(context.Background(), "base", hidden, "")
		if err == nil {
			t.Errorf("%s was served (%d bytes)", hidden, len(body))
		}
	}

	// A pattern that will not compile is refused by name.
	if err := l.server.Files.SetIgnoreRegexes([]string{"([unclosed"}); err == nil {
		t.Error("a malformed file_ignore_regex was accepted")
	}
}
