//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A certificate whose state names no owner keeps the one it had when it is
// reissued, and so does a key that is regenerated. Both are written through
// a temporary file and a rename, and before this only a requested owner was
// put back afterwards, so a certificate whose group was set some other way
// went to whoever ran the state at its next renewal. DIVERGENCE 5.255.
//
// The reissue is forced by a changed name rather than by waiting out the
// renewal window: both go through the same write, and a changed name is the
// one a test can provoke on a certificate it has just issued.
func TestAReissueKeepsTheOwnerItReplaced(t *testing.T) {
	gid, _ := otherGroup(t)
	r := New()
	dir := t.TempDir()
	key := filepath.Join(dir, "metrics.key")
	cert := filepath.Join(dir, "metrics.crt")

	keyArgs := func(algo string) *value.Map { return value.MapOf("name", key, "algo", algo) }
	certArgs := func(name string) *value.Map {
		return value.MapOf("name", cert, "private_key", key, "CN", name,
			"subject_alt_names", []any{"DNS:" + name}, "days_valid", int64(90), "days_remaining", int64(30))
	}
	if res, _ := r.States.Call(newCtx(false), "x509.private_key_managed", keyArgs("ec")); !res.HasChanges() {
		t.Fatalf("the key was not written: %+v", res)
	}
	if res, _ := r.States.Call(newCtx(false), "x509.certificate_managed", certArgs("old.example")); !res.HasChanges() {
		t.Fatalf("the certificate was not issued: %+v", res)
	}
	for _, path := range []string{key, cert} {
		if err := os.Chown(path, -1, gid); err != nil {
			t.Fatal(err)
		}
	}

	res, err := r.States.Call(newCtx(false), "x509.certificate_managed", certArgs("new.example"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("the certificate was not reissued: %+v", res)
	}
	if got := gidOf(t, cert); got != gid {
		t.Errorf("a reissue with no owner requested moved the certificate from group %d to %d", gid, got)
	}

	// A key of another kind is a regenerated key, written the same way.
	res, err = r.States.Call(newCtx(false), "x509.private_key_managed", keyArgs("rsa"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasChanges() {
		t.Fatalf("the key was not regenerated: %+v", res)
	}
	if got := gidOf(t, key); got != gid {
		t.Errorf("a regenerated key with no owner requested moved from group %d to %d", gid, got)
	}
}
