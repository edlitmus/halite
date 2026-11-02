package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A file replaced while it is being served goes out with its own digest.
//
// `file` opened the file, then hashed it by *path*, a second open, then sent
// the first handle. A deploy that writes the new tree over the old with a
// rename, which is how a tree is deployed without a half-written file in it,
// landing between the two sent the old body under the new body's digest. The
// node refuses a body whose digest differs, so it failed safe and retried; it
// was still a refusal the hub caused and could not explain. DIVERGENCE 5.530.
func TestAFileReplacedWhileItIsServedGoesOutWithItsOwnDigest(t *testing.T) {
	l := newLab(t).withFiles(t, map[string]string{"app.conf": "version one\n"})
	path := filepath.Join(l.root, "app.conf")
	l.server.fileAfterOpen = func() {
		next := path + ".new"
		if err := os.WriteFile(next, []byte("version two, which is longer\n"), 0o644); err != nil {
			t.Error(err)
			return
		}
		if err := os.Rename(next, path); err != nil {
			t.Error(err)
		}
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/files/base/app.conf", nil)
	l.server.file(w, r, "web1.example", "base", "app.conf")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	sum := sha256.Sum256(w.Body.Bytes())
	want := "sha256:" + hex.EncodeToString(sum[:])
	if got := w.Header().Get("X-Halite-Hash"); got != want {
		t.Errorf("the body %q was sent under digest %s, which is the digest of %s", w.Body.String(), got, want)
	}
	if got := w.Header().Get("ETag"); got != `"`+want+`"` {
		t.Errorf("the entity tag %s is not the body's digest", got)
	}
}
