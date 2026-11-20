package returner

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/edlitmus/halite/internal/job"
)

// A redirect from the receiver sends nothing anywhere else, fails the
// delivery and keeps the return (GHSA-3h7q-c9jf-rw88, DIVERGENCE 5.686).
//
// Built with New and a CA file, the way a real configuration builds it, and
// not with webhookTo, which swaps in the test server's own client and so
// would not have the redirect policy under test.
//
// Before the fix a 307 or 308 delivered the signed body to a plain-http
// server on another origin, and a 302 delivered a bodiless GET there that
// counted as success.
func TestARedirectSendsNothingElsewhereAndKeepsTheReturn(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		var elsewhere atomic.Int32
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			elsewhere.Add(1)
		}))
		receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, other.URL+"/elsewhere", code)
		}))

		dir := t.TempDir()
		caFile := filepath.Join(dir, "ca.pem")
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: receiver.Certificate().Raw})
		if err := os.WriteFile(caFile, pemBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		spool := filepath.Join(dir, "spool")
		built, err := New("webhook", Options{
			URL: receiver.URL + "/hook", Secret: "s3cret", StateDir: dir,
			SpoolDir: spool, CAFile: caFile, MaxAttempts: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := built.(*webhookReturner)

		_, err = r.postOnce(context.Background(), "return", []byte(`{"jid":"1"}`))
		if err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Errorf("%d: postOnce answered %v, want a failure naming the redirect", code, err)
		}
		if err := built.Return(context.Background(), &job.Return{JID: "1", NodeID: "web1.example"}); err != nil {
			t.Errorf("%d: Return: %v", code, err)
		}
		if n := elsewhere.Load(); n != 0 {
			t.Errorf("%d: the redirect's target received %d request(s), want none", code, n)
		}
		spooled, _ := os.ReadDir(spool)
		if len(spooled) == 0 {
			t.Errorf("%d: the refused return was not spooled", code)
		}
		receiver.Close()
		other.Close()
	}
}
