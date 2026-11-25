package awsauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// podIdentityStandIn answers like the full-URI credential endpoint and
// records the Authorization header of every request.
//
// It refuses a request whose header is not `want` with a 401. That is
// this stand-in's choice, not a claim about what the EKS agent answers:
// the agent's own refusal is what TestLivePodIdentity... sees, and it is
// not written down here because nobody has captured it yet.
type podIdentityStandIn struct {
	mu   sync.Mutex
	want string
	seen []string
	// expires is the Expiration each answer carries.
	expires time.Time
}

func (s *podIdentityStandIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	got := r.Header.Get("Authorization")
	s.seen = append(s.seen, got)
	if got != s.want {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	json.NewEncoder(w).Encode(map[string]string{
		"AccessKeyId":     "ASIAPODIDENTITY",
		"SecretAccessKey": "secret",
		"Token":           "session",
		"Expiration":      s.expires.UTC().Format(time.RFC3339),
	})
}

func (s *podIdentityStandIn) headers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// The case issue 613 names: the URI and the token file EKS sets, and a
// request that has to carry the token to be answered. Before the fix the
// header was empty and the endpoint's refusal was the error.
func TestAPodIdentityRequestCarriesTheTokenFromTheFile(t *testing.T) {
	stand := &podIdentityStandIn{want: "pod-token-1", expires: time.Now().Add(time.Hour)}
	srv := httptest.NewServer(stand)
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "eks-pod-identity-token")
	if err := os.WriteFile(tokenFile, []byte("pod-token-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &Provider{Environ: envFrom(map[string]string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     srv.URL + "/v1/credentials",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": tokenFile,
	})}
	creds, err := p.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("Retrieve: %v (the endpoint saw Authorization %q)", err, stand.headers())
	}
	if creds.AccessKeyID != "ASIAPODIDENTITY" {
		t.Errorf("got key %q", creds.AccessKeyID)
	}
	if got := stand.headers(); len(got) != 1 || got[0] != "pod-token-1" {
		t.Errorf("the endpoint saw Authorization %q, want one request carrying the file's token", got)
	}
}

// EKS rotates the token file under a running pod. A token read once at
// start would be refused at the first refresh after the rotation, so the
// file is read again on every fetch.
func TestARotatedTokenFileIsReadAgainAtTheNextRefresh(t *testing.T) {
	now := time.Now()
	stand := &podIdentityStandIn{want: "first", expires: now.Add(10 * time.Minute)}
	srv := httptest.NewServer(stand)
	defer srv.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	clock := now
	p := &Provider{
		Now: func() time.Time { return clock },
		Environ: envFrom(map[string]string{
			"AWS_CONTAINER_CREDENTIALS_FULL_URI":     srv.URL,
			"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": tokenFile,
		}),
	}
	if _, err := p.Retrieve(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Rotate, and move past the credentials' expiry.
	if err := os.WriteFile(tokenFile, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	stand.mu.Lock()
	stand.want = "second"
	stand.expires = now.Add(time.Hour)
	stand.mu.Unlock()
	clock = now.Add(20 * time.Minute)
	if _, err := p.Retrieve(context.Background()); err != nil {
		t.Fatalf("the refresh after a rotation failed: %v (headers %q)", err, stand.headers())
	}
	if got := stand.headers(); len(got) != 2 || got[1] != "second" {
		t.Errorf("headers %q, want the refresh to carry the rotated token", got)
	}
}

// The file wins over the value when both are set, and the value is used
// when there is no file.
func TestTheTokenFileWinsOverTheTokenValue(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("from-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"both", map[string]string{
			"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": tokenFile,
			"AWS_CONTAINER_AUTHORIZATION_TOKEN":      "from-value",
		}, "from-file"},
		{"value only", map[string]string{
			"AWS_CONTAINER_AUTHORIZATION_TOKEN": "from-value",
		}, "from-value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stand := &podIdentityStandIn{want: tc.want, expires: time.Now().Add(time.Hour)}
			srv := httptest.NewServer(stand)
			defer srv.Close()
			tc.env["AWS_CONTAINER_CREDENTIALS_FULL_URI"] = srv.URL
			p := &Provider{Environ: envFrom(tc.env)}
			if _, err := p.Retrieve(context.Background()); err != nil {
				t.Fatalf("%v (headers %q)", err, stand.headers())
			}
		})
	}
}

// A token with a line break in it is refused by name, before any request.
func TestATokenWithALineBreakIsRefusedBeforeTheRequest(t *testing.T) {
	stand := &podIdentityStandIn{want: "x"}
	srv := httptest.NewServer(stand)
	defer srv.Close()
	p := &Provider{Environ: envFrom(map[string]string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI": srv.URL,
		"AWS_CONTAINER_AUTHORIZATION_TOKEN":  "abc\r\nX-Injected: 1",
	})}
	_, err := p.Retrieve(context.Background())
	if err == nil || !strings.Contains(err.Error(), "line break") {
		t.Fatalf("got %v, want the line break named", err)
	}
	if n := len(stand.headers()); n != 0 {
		t.Errorf("the endpoint was asked %d times", n)
	}
}
