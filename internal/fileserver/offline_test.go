package fileserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var errDown = errors.New("connection refused")
var errNo = errors.New("the hub said no")

type stubHub struct {
	err      error
	manifest *Manifest
	calls    int
}

func (h *stubHub) FileManifest(context.Context, string, string) ([]byte, error) {
	h.calls++
	if h.err != nil {
		return nil, h.err
	}
	return json.Marshal(h.manifest)
}

func (h *stubHub) FetchFile(context.Context, string, string, string) ([]byte, string, bool, error) {
	h.calls++
	return nil, "", false, h.err
}

func offlineRemote(t *testing.T, hub *stubHub) *Remote {
	t.Helper()
	r := NewRemote(hub, t.TempDir(), []string{"base"})
	r.Unreachable = func(err error) bool { return errors.Is(err, errDown) }
	return r
}

func seed(t *testing.T, r *Remote, rel, body string) {
	t.Helper()
	p := filepath.Join(r.Dir, "files", "base", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The failure measured with a real hub: the file was in the cache and the run
// failed anyway.
func TestACachedFileIsServedWhenNoHubAnswers(t *testing.T) {
	r := offlineRemote(t, &stubHub{err: errDown})
	seed(t, r, "top.sls", "cached")
	got, err := r.Read("base", "top.sls")
	if err != nil || string(got) != "cached" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A hub that answered has been heard: its refusal is not an outage.
func TestAHubThatAnswersIsNotOverruledByTheCache(t *testing.T) {
	r := offlineRemote(t, &stubHub{err: errNo})
	seed(t, r, "top.sls", "cached")
	if _, err := r.Read("base", "top.sls"); !errors.Is(err, errNo) {
		t.Fatalf("got %v, want the hub's answer", err)
	}
}

func TestAFileNeverCachedStillFails(t *testing.T) {
	r := offlineRemote(t, &stubHub{err: errDown})
	if _, err := r.Read("base", "new.sls"); err == nil {
		t.Fatal("a file this node never held was served")
	}
	// And again inside the hold-off, which takes the other path.
	if _, err := r.Read("base", "new.sls"); err == nil {
		t.Fatal("a file this node never held was served during the hold-off")
	}
}

// A hub dropping packets costs a timeout per request, so a run over many
// files must not ask once per file.
func TestAnUnreachableHubIsAskedOnceNotPerFile(t *testing.T) {
	hub := &stubHub{err: errDown}
	r := offlineRemote(t, hub)
	seed(t, r, "a.sls", "a")
	seed(t, r, "b.sls", "b")
	for _, f := range []string{"a.sls", "b.sls", "a.sls"} {
		if _, err := r.Read("base", f); err != nil {
			t.Fatal(err)
		}
	}
	if hub.calls != 1 {
		t.Errorf("the hub was asked %d times", hub.calls)
	}
}

// What a restart during an outage depends on, and what a recursive state lays
// out from. A prefix is a directory: "web" must not take "webby".
func TestTheLastWholeListingAnswersDuringAnOutage(t *testing.T) {
	hub := &stubHub{manifest: &Manifest{Env: "base", Files: []Entry{
		{Path: "web/a"}, {Path: "webby/b"}, {Path: "top.sls"},
	}}}
	r := offlineRemote(t, hub)
	if r.HasCachedTree("base") {
		t.Fatal("a tree before anything was listed")
	}
	if _, err := r.ManifestFor("base", ""); err != nil {
		t.Fatal(err)
	}
	if !r.HasCachedTree("base") {
		t.Fatal("the listing was not kept")
	}
	hub.err = errDown
	m, err := r.ManifestFor("base", "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 1 || m.Files[0].Path != "web/a" {
		t.Errorf("got %+v", m.Files)
	}
}

// A part must not replace the whole: the next outage would lay out a fraction
// of the tree.
func TestAPartialListingIsNotKept(t *testing.T) {
	hub := &stubHub{manifest: &Manifest{Env: "base", Truncated: true, Files: []Entry{{Path: "a"}}}}
	r := offlineRemote(t, hub)
	if _, err := r.ManifestFor("base", ""); err != nil {
		t.Fatal(err)
	}
	if r.HasCachedTree("base") {
		t.Error("a truncated listing was kept")
	}
	hub.manifest.Truncated = false
	if _, err := r.ManifestFor("base", "a"); err != nil {
		t.Fatal(err)
	}
	if r.HasCachedTree("base") {
		t.Error("a prefix's listing was kept")
	}
}
