package extension

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fileServer is a file server holding `_ext/` bundles.
type fileServer struct {
	files map[string][]byte
	// listErr makes the listing fail.
	listErr error
	// fetched counts what was actually downloaded.
	fetched int
}

func newFileServer() *fileServer { return &fileServer{files: map[string][]byte{}} }

func (f *fileServer) List(prefix string) ([]SourceFile, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []SourceFile
	for p, body := range f.files {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		out = append(out, SourceFile{Path: p, Digest: digestOf(body)})
	}
	return out, nil
}

func (f *fileServer) Fetch(p string) ([]byte, error) {
	body, ok := f.files[p]
	if !ok {
		return nil, fmt.Errorf("no such file %s", p)
	}
	f.fetched++
	return body, nil
}

func digestOf(body []byte) string {
	dir, err := os.MkdirTemp("", "digest-*")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return ""
	}
	digest, _ := DigestFile(path)
	return digest
}

// publish puts a signed bundle on the file server under `_ext/`.
func publish(t *testing.T, server *fileServer, name, version string, tamper func(map[string][]byte)) TrustKey {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "run"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := Build(dir, Manifest{
		Name: name, Version: version, Kind: "module",
		Executables: map[string]string{"freebsd/amd64": "run", "linux/amd64": "run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.Encode()
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := MerkleRoot(manifest.Files)
	if err != nil {
		t.Fatal(err)
	}

	published := map[string][]byte{
		"run":         []byte("#!/bin/sh\nexit 0\n"),
		ManifestName:  raw,
		SignatureName: Sign(private, root),
	}
	if tamper != nil {
		tamper(published)
	}
	for rel, body := range published {
		server.files[ExtPrefix+name+"/"+version+"/"+rel] = body
	}
	return TrustKey{Name: "release", Key: public}
}

func syncerFor(t *testing.T, server *fileServer, key TrustKey) (*Syncer, string) {
	t.Helper()
	cache := filepath.Join(t.TempDir(), "ext")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	return &Syncer{
		Source: server, Dir: cache,
		Options: LoadOptions{
			TrustKeys: []TrustKey{key}, RequireSignature: true,
			Platform: "freebsd/amd64",
		},
	}, cache
}

func TestSyncFetchesASignedBundle(t *testing.T) {
	server := newFileServer()
	key := publish(t, server, "echo", "1.0.0", nil)
	syncer, cache := syncerFor(t, server, key)

	report, err := syncer.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) != 1 || report.Changes[0].Status != "fetched" {
		t.Fatalf("the report is %+v", report.Changes)
	}
	if !report.Changed() {
		t.Error("the report does not say anything changed")
	}
	if len(report.Changes[0].Root) != 64 {
		t.Errorf("no root was reported: %q", report.Changes[0].Root)
	}

	// It landed, and it loads from the cache.
	store := &Store{Dir: cache, Options: syncer.Options}
	installed, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 1 || installed[0].Err != nil {
		t.Fatalf("the cache holds %+v", installed)
	}
	// The executable bit comes from the signed manifest.
	info, err := os.Stat(filepath.Join(cache, "echo", "1.0.0", "run"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the executable is mode %o", info.Mode().Perm())
	}
}

// A second synchronization with nothing new must not re-download.
func TestSyncSkipsWhatTheCacheAlreadyHas(t *testing.T) {
	server := newFileServer()
	key := publish(t, server, "echo", "1.0.0", nil)
	syncer, _ := syncerFor(t, server, key)

	if _, err := syncer.Sync(); err != nil {
		t.Fatal(err)
	}
	downloaded := server.fetched

	report, err := syncer.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if report.Changes[0].Status != "unchanged" {
		t.Errorf("a second sync reported %q", report.Changes[0].Status)
	}
	if report.Changed() {
		t.Error("a second sync says something changed")
	}
	if server.fetched != downloaded {
		t.Errorf("a second sync downloaded %d more files", server.fetched-downloaded)
	}
}

// A node running a good version of an extension must not lose it
// because somebody published a bad one.
func TestABadBundleDoesNotReplaceAGoodOne(t *testing.T) {
	server := newFileServer()
	key := publish(t, server, "echo", "1.0.0", nil)
	syncer, cache := syncerFor(t, server, key)

	if _, err := syncer.Sync(); err != nil {
		t.Fatal(err)
	}
	before, err := DigestFile(filepath.Join(cache, "echo", "1.0.0", "run"))
	if err != nil {
		t.Fatal(err)
	}

	// The publisher replaces the executable without re-signing.
	server.files[ExtPrefix+"echo/1.0.0/run"] = []byte("#!/bin/sh\nrm -rf /\n")

	report, err := syncer.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if report.Changes[0].Status != "refused" {
		t.Fatalf("a tampered bundle was %q", report.Changes[0].Status)
	}
	after, err := DigestFile(filepath.Join(cache, "echo", "1.0.0", "run"))
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("the good bundle in the cache was replaced by the bad one")
	}
}

// A bundle published at one path and signed as another would let a
// trusted extension be installed under a name it was not signed for.
func TestAPathThatDisagreesWithTheManifestIsRefused(t *testing.T) {
	server := newFileServer()
	key := publish(t, server, "echo", "1.0.0", nil)

	// The same signed bundle, republished under another name.
	for p, body := range map[string][]byte{} {
		_ = p
		_ = body
	}
	for _, rel := range []string{"run", ManifestName, SignatureName} {
		server.files[ExtPrefix+"vault/1.0.0/"+rel] = server.files[ExtPrefix+"echo/1.0.0/"+rel]
	}
	syncer, _ := syncerFor(t, server, key)

	report, err := syncer.Sync()
	if err != nil {
		t.Fatal(err)
	}
	var vault *Change
	for i := range report.Changes {
		if report.Changes[i].Name == "vault" {
			vault = &report.Changes[i]
		}
	}
	if vault == nil {
		t.Fatal("the republished bundle was not seen at all")
	}
	if vault.Status != "refused" {
		t.Fatalf("a bundle published under another name was %q", vault.Status)
	}
	if !strings.Contains(vault.Reason, "signed as") {
		t.Errorf("the refusal is %q", vault.Reason)
	}
}

// Fetching a version the node can never load fills the cache for
// nothing, and the refusal is more useful than the download.
func TestAPinnedExtensionAtAnotherVersionIsNotFetched(t *testing.T) {
	server := newFileServer()
	key := publish(t, server, "echo", "2.0.0", nil)
	syncer, _ := syncerFor(t, server, key)
	syncer.Pins = map[string]Pin{"echo": {Version: "1.0.0"}}

	report, err := syncer.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if report.Changes[0].Status != "skipped" {
		t.Errorf("a pinned-away version was %q", report.Changes[0].Status)
	}
	if server.fetched != 0 {
		t.Errorf("it downloaded %d files for a version it cannot use", server.fetched)
	}
}

// An unsigned bundle on the file server is refused, which is the whole
// difference from `saltutil.sync_all`.
func TestAnUnsignedBundleIsRefused(t *testing.T) {
	server := newFileServer()
	key := publish(t, server, "echo", "1.0.0", func(files map[string][]byte) {
		delete(files, SignatureName)
	})
	syncer, cache := syncerFor(t, server, key)

	report, err := syncer.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if report.Changes[0].Status != "refused" {
		t.Fatalf("an unsigned bundle was %q", report.Changes[0].Status)
	}
	if _, err := os.Stat(filepath.Join(cache, "echo", "1.0.0")); err == nil {
		t.Error("an unsigned bundle landed in the cache")
	}
}

// The per-kind variants of `saltutil.sync_modules` and friends.
func TestSyncCanBeLimitedToOneKind(t *testing.T) {
	server := newFileServer()
	key := publish(t, server, "echo", "1.0.0", nil)
	syncer, _ := syncerFor(t, server, key)
	syncer.Kinds = []string{"returner"}

	report, err := syncer.Sync()
	if err != nil {
		t.Fatal(err)
	}
	if report.Changes[0].Status != "skipped" {
		t.Errorf("a module extension was %q during a returner sync", report.Changes[0].Status)
	}
}

// A path on the file server becomes a directory in the cache.
func TestAPathThatWouldLeaveTheCacheIsIgnored(t *testing.T) {
	cases := []string{
		ExtPrefix + "../../etc/passwd/1.0.0/run",
		ExtPrefix + "echo/../../../run/x",
		ExtPrefix + "echo",
		ExtPrefix + "echo/1.0.0",
		"other/echo/1.0.0/run",
	}
	for _, p := range cases {
		if _, _, _, ok := splitExtPath(p); ok {
			t.Errorf("%q was accepted as a bundle path", p)
		}
	}
	name, version, rest, ok := splitExtPath(ExtPrefix + "echo/1.0.0/bin/run")
	if !ok || name != "echo" || version != "1.0.0" || rest != "bin/run" {
		t.Errorf("a good path read as %q %q %q %v", name, version, rest, ok)
	}
}
