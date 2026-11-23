package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/treesign"
)

// Issue 764's node-side check, through the real hub handler and the real
// transport client: a stub of either would prove less than the pair, which
// is where the 304 and the published digest come from.

type signedLab struct {
	*lab
	key     *ecdsa.PrivateKey
	trusted jobsign.SignerKey
	node    fileserver.Fetch
}

func newSignedLab(t *testing.T, files map[string]string) *signedLab {
	t.Helper()
	l := newLab(t).withFiles(t, files)
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := &signedLab{lab: l, key: k, trusted: jobsign.SignerKey{Name: "tree-ci", Key: &k.PublicKey}}
	s.node = l.enrolled(t, "web1.example")
	return s
}

func (s *signedLab) write(t *testing.T, rel, body string) {
	t.Helper()
	p := filepath.Join(s.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sign lists the served tree as `halite-hub tree manifest` does and signs
// it, returning the manifest's bytes.
func (s *signedLab) sign(t *testing.T, env string, issued time.Time, mutate func(*treesign.Manifest)) []byte {
	t.Helper()
	entries, err := s.server.Files.TreeEntries("base", "sha256")
	if err != nil {
		t.Fatal(err)
	}
	m := &treesign.Manifest{Env: env, Issued: treesign.FormatTime(issued), HashType: "sha256", Files: entries}
	if mutate != nil {
		mutate(m)
	}
	raw, err := treesign.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	line, err := treesign.SignatureLine(s.key, raw)
	if err != nil {
		t.Fatal(err)
	}
	s.write(t, treesign.ManifestPath, string(raw))
	s.write(t, treesign.SignaturePath, line+"\n")
	return raw
}

type refusals struct {
	mu sync.Mutex
	by map[treesign.Reason]int
}

func (r *refusals) count(reason treesign.Reason) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.by[reason]++
}

func (r *refusals) of(reason treesign.Reason) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.by[reason]
}

func (s *signedLab) remote(t *testing.T, mode fileserver.SignatureMode, keys ...jobsign.SignerKey) (*fileserver.Remote, *refusals, string) {
	t.Helper()
	state := t.TempDir()
	counted := &refusals{by: map[treesign.Reason]int{}}
	r := fileserver.NewRemote(s.node, t.TempDir(), []string{"base"})
	r.Guard = &fileserver.TreeGuard{Mode: mode, Keys: keys, Records: treesign.Store{Dir: state},
		Objects: filepath.Join(t.TempDir(), "objects"), Refused: counted.count}
	return r.ForRun(), counted, state
}

var signedFiles = map[string]string{
	"top.sls":        "base:\n  '*':\n    - web\n",
	"web/init.sls":   "nginx:\n  pkg.installed: []\n",
	"web/nginx.conf": "worker_processes 4;\n",
	"web/site.tar":   "not really a tarball\n",
}

func TestASignedTreeIsReadThroughItsManifest(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	raw := s.sign(t, "base", time.Now(), nil)
	r, counted, state := s.remote(t, fileserver.SignatureRequired, s.trusted)

	src, _, err := r.Source("base", "web")
	if err != nil || !strings.Contains(string(src), "nginx") {
		t.Fatalf("Source: %q, %v", src, err)
	}
	path, err := r.Fetch("base", "salt://web/site.tar")
	if err != nil {
		t.Fatal(err)
	}
	// From the content-addressed store, keeping the basename, so that a
	// state choosing an archive's format by suffix still can.
	if !strings.Contains(filepath.ToSlash(path), "/objects/sha256/") || filepath.Base(path) != "site.tar" {
		t.Errorf("the verified copy is at %s", path)
	}
	m, _ := treesign.Parse(raw)
	want, _ := m.Lookup("web/site.tar")
	if algo, digest, err := r.Hash("base", "salt://web/site.tar"); err != nil || algo != "sha256" || digest != want.Hash {
		t.Errorf("Hash: %s %s %v, want the manifest's %s", algo, digest, err, want.Hash)
	}
	rec, err := (treesign.Store{Dir: state}).Load("base")
	if err != nil || rec == nil || rec.ID != treesign.ID(raw) || rec.Signer != "tree-ci" {
		t.Fatalf("the high-water mark: %+v, %v", rec, err)
	}
	for _, reason := range treesign.Reasons {
		if counted.of(reason) != 0 {
			t.Errorf("a clean read counted a %s refusal", reason)
		}
	}
}

func TestAMissingManifestIsRefusedOnlyWhereRequired(t *testing.T) {
	s := newSignedLab(t, signedFiles)

	r, _, _ := s.remote(t, fileserver.SignatureRequired, s.trusted)
	_, err := r.Read("base", "top.sls")
	if treesign.ReasonOf(err) != treesign.ReasonMissing || !strings.Contains(err.Error(), "_halite/manifest.json") {
		t.Fatalf("true: %v", err)
	}

	r, counted, _ := s.remote(t, fileserver.SignatureWarn, s.trusted)
	if body, err := r.Read("base", "top.sls"); err != nil || !strings.Contains(string(body), "base:") {
		t.Fatalf("warn applied nothing: %q, %v", body, err)
	}
	if counted.of(treesign.ReasonMissing) != 1 {
		t.Errorf("warn counted %d missing manifests", counted.of(treesign.ReasonMissing))
	}

	r, _, state := s.remote(t, fileserver.SignatureOff, s.trusted)
	if _, err := r.Read("base", "top.sls"); err != nil {
		t.Fatalf("false: %v", err)
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Error("a node with the requirement off recorded a revision")
	}
}

func TestAManifestSignedByAnUntrustedKeyIsRefused(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	s.sign(t, "base", time.Now(), nil)
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	r, _, _ := s.remote(t, fileserver.SignatureRequired, jobsign.SignerKey{Name: "other", Key: &other.PublicKey})
	if _, err := r.Read("base", "top.sls"); treesign.ReasonOf(err) != treesign.ReasonSignature {
		t.Fatalf("got %v", err)
	}
	r, _, _ = s.remote(t, fileserver.SignatureRequired)
	if _, err := r.Read("base", "top.sls"); treesign.ReasonOf(err) != treesign.ReasonNoKeys ||
		!strings.Contains(err.Error(), "state_signer_keys") {
		t.Fatalf("no keys: %v", err)
	}
}

func TestAnotherEnvironmentsManifestIsRefused(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	s.sign(t, "dev", time.Now(), nil)
	r, _, _ := s.remote(t, fileserver.SignatureRequired, s.trusted)
	if _, err := r.Read("base", "top.sls"); treesign.ReasonOf(err) != treesign.ReasonEnv {
		t.Fatalf("got %v", err)
	}
}

func TestAnExpiredOrTooOldManifestIsRefused(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	s.sign(t, "base", time.Now().Add(-2*time.Hour), func(m *treesign.Manifest) {
		m.Expires = treesign.FormatTime(time.Now().Add(-time.Hour))
	})
	r, _, _ := s.remote(t, fileserver.SignatureRequired, s.trusted)
	if _, err := r.Read("base", "top.sls"); treesign.ReasonOf(err) != treesign.ReasonExpired {
		t.Fatalf("expired: %v", err)
	}

	s.sign(t, "base", time.Now().Add(-48*time.Hour), nil)
	r, _, _ = s.remote(t, fileserver.SignatureRequired, s.trusted)
	r.Guard.MaxAge = 24 * time.Hour
	_, err := r.Read("base", "top.sls")
	if treesign.ReasonOf(err) != treesign.ReasonAge || !strings.Contains(err.Error(), "state_manifest_max_age") {
		t.Fatalf("too old: %v", err)
	}
}

// A new run picks up a new revision; an older one is a rollback whatever
// signed it; and two revisions for one instant are an equivocation.
func TestTheHighWaterMarkRefusesARollback(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	now := time.Now()
	old := s.sign(t, "base", now.Add(-time.Hour), nil)
	oldSig, _ := os.ReadFile(filepath.Join(s.root, treesign.SignaturePath))
	first, _, state := s.remote(t, fileserver.SignatureRequired, s.trusted)
	if _, err := first.Read("base", "top.sls"); err != nil {
		t.Fatal(err)
	}
	s.write(t, "top.sls", "base:\n  '*':\n    - web\n    - extra\n")
	s.sign(t, "base", now, nil)
	second := first.ForRun()
	if body, err := second.Read("base", "top.sls"); err != nil || !strings.Contains(string(body), "extra") {
		t.Fatalf("the newer revision: %q, %v", body, err)
	}

	// The hub puts the old, validly signed manifest and its content back.
	s.write(t, "top.sls", signedFiles["top.sls"])
	s.write(t, treesign.ManifestPath, string(old))
	s.write(t, treesign.SignaturePath, string(oldSig))
	_, err := first.ForRun().Read("base", "top.sls")
	if treesign.ReasonOf(err) != treesign.ReasonRollback || !strings.Contains(err.Error(), "older revision") {
		t.Fatalf("a rollback: %v", err)
	}
	rec, _ := (treesign.Store{Dir: state}).Load("base")
	if rec.Issued != treesign.FormatTime(now) {
		t.Errorf("the rollback moved the mark to %s", rec.Issued)
	}

	// Same instant, different bytes.
	s.write(t, "top.sls", "something else\n")
	s.sign(t, "base", now, nil)
	if _, err := first.ForRun().Read("base", "top.sls"); treesign.ReasonOf(err) != treesign.ReasonEquivocation {
		t.Fatalf("an equivocation: %v", err)
	}
}

// The hub serves it, the signer never published it: not found.
func TestAPathNotInTheManifestIsNotFoundEvenThoughTheHubServesIt(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	s.sign(t, "base", time.Now(), nil)
	s.write(t, "evil.sls", "rm:\n  cmd.run:\n    - name: rm -rf /\n")
	s.write(t, "web/added.conf", "x\n")
	r, counted, _ := s.remote(t, fileserver.SignatureRequired, s.trusted)

	_, err := r.Read("base", "evil.sls")
	if !errors.Is(err, fs.ErrNotExist) || treesign.ReasonOf(err) != treesign.ReasonNotInManifest {
		t.Fatalf("got %v", err)
	}
	if _, _, err := r.Source("base", "evil"); err == nil {
		t.Fatal("Source found a state the signer never published")
	}
	if r.Exists("base", "salt://evil.sls") {
		t.Error("Exists says yes")
	}
	// Listings come from the manifest, so the hub cannot add to a
	// file.recurse either.
	under, err := r.ListUnder("base", "salt://web")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range under {
		if p == "added.conf" {
			t.Error("the listing includes a file the hub added")
		}
	}
	if len(under) != 3 {
		t.Errorf("ListUnder: %v", under)
	}
	if counted.of(treesign.ReasonNotInManifest) == 0 {
		t.Error("nothing was counted")
	}
}

// The hub publishes a digest for whatever it sends; only the manifest's
// counts. And a body this run already verified, changed on the hub later
// in the run, fails the run at that file rather than mixing revisions.
func TestABodyThatDiffersFromTheManifestIsRefused(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	s.sign(t, "base", time.Now(), nil)
	r, _, _ := s.remote(t, fileserver.SignatureRequired, s.trusted)
	if _, err := r.Read("base", "web/nginx.conf"); err != nil {
		t.Fatal(err)
	}
	s.write(t, "web/nginx.conf", "worker_processes 400;\n")
	_, err := r.Read("base", "web/nginx.conf")
	if treesign.ReasonOf(err) != treesign.ReasonDigest || !strings.Contains(err.Error(), "the hub published sha256:") {
		t.Fatalf("a changed body: %v", err)
	}
	_, err = r.Read("base", "web/init.sls")
	if err != nil {
		t.Fatalf("an unchanged file in the same run: %v", err)
	}
	// And in Source, the refusal is the answer, not "no such state".
	s.write(t, "web/init.sls", "changed\n")
	if _, _, err := r.ForRun().Source("base", "web"); treesign.ReasonOf(err) != treesign.ReasonDigest {
		t.Fatalf("Source hid the refusal: %v", err)
	}
}

func TestWarnModeCountsEverythingAndRefusesNothing(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	s.sign(t, "base", time.Now(), nil)
	s.write(t, "web/nginx.conf", "worker_processes 400;\n")
	s.write(t, "extra.sls", "x\n")
	r, counted, _ := s.remote(t, fileserver.SignatureWarn, s.trusted)
	if body, err := r.Read("base", "web/nginx.conf"); err != nil || !strings.Contains(string(body), "400") {
		t.Fatalf("a changed body: %q, %v", body, err)
	}
	if _, err := r.Read("base", "extra.sls"); err != nil {
		t.Fatalf("an unsigned file: %v", err)
	}
	if counted.of(treesign.ReasonDigest) != 1 || counted.of(treesign.ReasonNotInManifest) != 1 {
		t.Errorf("counted %v", counted.by)
	}
}

// The record is on disk before any file of the revision is fetched.
func TestTheRevisionIsRecordedBeforeAnyFileIsRead(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	s.sign(t, "base", time.Now(), nil)
	state := t.TempDir()
	watcher := &recordWatcher{Fetch: s.node, record: filepath.Join(state, "base.json")}
	r := fileserver.NewRemote(watcher, t.TempDir(), []string{"base"})
	r.Guard = &fileserver.TreeGuard{Mode: fileserver.SignatureRequired, Keys: []jobsign.SignerKey{s.trusted},
		Records: treesign.Store{Dir: state}, Objects: filepath.Join(t.TempDir(), "objects")}
	if _, err := r.Read("base", "top.sls"); err != nil {
		t.Fatal(err)
	}
	if watcher.contentFetches == 0 || watcher.withoutRecord != 0 {
		t.Fatalf("%d content fetch(es), %d before the record was written", watcher.contentFetches, watcher.withoutRecord)
	}
}

type recordWatcher struct {
	fileserver.Fetch
	record                        string
	contentFetches, withoutRecord int
}

func (w *recordWatcher) FetchFile(ctx context.Context, env, path, etag string) ([]byte, string, bool, error) {
	if !treesign.IsReserved(path) {
		w.contentFetches++
		if _, err := os.Stat(w.record); err != nil {
			w.withoutRecord++
		}
	}
	return w.Fetch.FetchFile(ctx, env, path, etag)
}

// Every file of the accepted revision cached: an outage applies it. One
// missing: refused, rather than a mixture of the cache and nothing.
func TestAnOutageAppliesTheAcceptedRevisionWhole(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	s.sign(t, "base", time.Now(), nil)
	down := &switchable{Fetch: s.node}
	state, cache, objects := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "objects")
	build := func() *fileserver.Remote {
		r := fileserver.NewRemote(down, cache, []string{"base"})
		r.Unreachable = func(err error) bool { return errors.Is(err, errHubDown) }
		r.Guard = &fileserver.TreeGuard{Mode: fileserver.SignatureRequired, Keys: []jobsign.SignerKey{s.trusted},
			Records: treesign.Store{Dir: state}, Objects: objects}
		return r.ForRun()
	}
	r := build()
	for _, p := range []string{"top.sls", "web/init.sls"} {
		if _, err := r.Read("base", p); err != nil {
			t.Fatal(err)
		}
	}
	down.down = true
	offline := build()
	if _, err := offline.Read("base", "top.sls"); err != nil {
		t.Fatalf("a cached file of the accepted revision during an outage: %v", err)
	}
	if _, err := offline.Read("base", "web/nginx.conf"); treesign.ReasonOf(err) != treesign.ReasonOffline {
		t.Fatalf("a file never fetched during an outage: %v", err)
	}
}

var errHubDown = errors.New("connection refused")

type switchable struct {
	fileserver.Fetch
	down bool
}

func (s *switchable) FetchFile(ctx context.Context, env, path, etag string) ([]byte, string, bool, error) {
	if s.down {
		return nil, "", false, errHubDown
	}
	return s.Fetch.FetchFile(ctx, env, path, etag)
}

// Two runs at different revisions read the same path; each keeps its own
// revision's body, because each object is named by its digest, so the
// newer run's fetch cannot overwrite the file the older run was handed.
// (The older run asking again after the hub moved on is refused instead:
// one revision per run.)
func TestTwoRunsAtDifferentRevisionsDoNotShareABody(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	now := time.Now()
	s.sign(t, "base", now.Add(-time.Minute), nil)
	base, _, _ := s.remote(t, fileserver.SignatureRequired, s.trusted)
	older := base.ForRun()
	a, err := older.Fetch("base", "salt://web/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	s.write(t, "web/nginx.conf", "worker_processes 8;\n")
	s.sign(t, "base", now, nil)
	newer := base.ForRun()
	b, err := newer.Fetch("base", "salt://web/nginx.conf")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both revisions' bodies are at %s", a)
	}
	got, _ := os.ReadFile(a)
	if string(got) != signedFiles["web/nginx.conf"] {
		t.Errorf("the older run's copy now holds %q", got)
	}
	if _, err := older.Fetch("base", "salt://web/nginx.conf"); treesign.ReasonOf(err) != treesign.ReasonDigest {
		t.Errorf("the older run asking again after the hub moved on: %v", err)
	}
}

// Objects no environment's current or previous manifest names are
// removed when a new revision is accepted; the previous revision's stay,
// for an exec job still reading it.
func TestObjectsTwoRevisionsOldAreCollected(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	now := time.Now()
	base, _, _ := s.remote(t, fileserver.SignatureRequired, s.trusted)
	var paths []string
	for i, body := range []string{"one\n", "two\n", "three\n"} {
		s.write(t, "web/nginx.conf", body)
		s.sign(t, "base", now.Add(time.Duration(i-3)*time.Minute), nil)
		p, err := base.ForRun().Fetch("base", "salt://web/nginx.conf")
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Errorf("the first revision's object is still there: %v", err)
	}
	for _, p := range paths[1:] {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("a current or previous revision's object was removed: %v", err)
		}
	}
}

// Under warn a listing is the hub's, as without the feature, so a
// recursive state lays out what it always did; what true would have
// left out is counted.
func TestWarnModeListsWhatTheHubServes(t *testing.T) {
	s := newSignedLab(t, signedFiles)
	s.sign(t, "base", time.Now(), nil)
	s.write(t, "web/added.conf", "x\n")
	r, counted, _ := s.remote(t, fileserver.SignatureWarn, s.trusted)
	under, err := r.ListUnder("base", "salt://web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(under, ","), "added.conf") {
		t.Errorf("warn left an unsigned file out of the listing: %v", under)
	}
	if counted.of(treesign.ReasonNotInManifest) != 1 {
		t.Errorf("counted %v", counted.by)
	}
}
