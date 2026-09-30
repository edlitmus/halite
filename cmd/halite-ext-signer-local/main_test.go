package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/jobsign"
)

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestHandleSignsADigest(t *testing.T) {
	key := testKey(t)
	h := &handler{key: key}
	digest := []byte("0123456789012345678901234567890x") // 32 bytes' worth of anything
	digest = digest[:32]
	kwargs, _ := json.Marshal(signRequest{Digest: base64.StdEncoding.EncodeToString(digest)})

	out, err := h.handle(ext.Call{Function: "sign", Kwargs: kwargs})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result = %T", out)
	}
	sigB64, _ := m["signature"].(string)
	der, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		t.Fatalf("signature is not base64: %v", err)
	}
	if !ecdsa.VerifyASN1(&key.PublicKey, digest, der) {
		t.Error("the returned signature does not verify against the key's own public half")
	}
}

func TestHandleSignRejectsAMissingDigest(t *testing.T) {
	h := &handler{key: testKey(t)}
	if _, err := h.handle(ext.Call{Function: "sign"}); err == nil {
		t.Error("sign with no digest at all was accepted")
	}
	kwargs, _ := json.Marshal(signRequest{Digest: ""})
	if _, err := h.handle(ext.Call{Function: "sign", Kwargs: kwargs}); err == nil {
		t.Error("sign with an empty digest was accepted")
	}
}

func TestHandleSignRejectsANonBase64Digest(t *testing.T) {
	h := &handler{key: testKey(t)}
	kwargs, _ := json.Marshal(signRequest{Digest: "not base64!!"})
	if _, err := h.handle(ext.Call{Function: "sign", Kwargs: kwargs}); err == nil {
		t.Error("a non-base64 digest was accepted")
	}
}

func TestHandlePublicKeyReportsThePairedKey(t *testing.T) {
	key := testKey(t)
	h := &handler{key: key}
	out, err := h.handle(ext.Call{Function: "public_key"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	der, err := base64.StdEncoding.DecodeString(m["public_key"].(string))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		t.Error("the reported public key does not match the handler's own key")
	}
}

func TestHandleRefusesAnUnknownFunction(t *testing.T) {
	h := &handler{key: testKey(t)}
	if _, err := h.handle(ext.Call{Function: "detach"}); err == nil {
		t.Error("an unknown function was accepted")
	}
}

func TestLoadKeyNeedsThePathSet(t *testing.T) {
	if _, err := loadKey(""); err == nil {
		t.Error("an empty path was accepted")
	}
}

func TestLoadKeyReadsAPKCS8PEMFile(t *testing.T) {
	key := testKey(t)
	pemBytes, err := jobsign.EncodePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "signer.key")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(key) {
		t.Error("the key read back does not match the one written")
	}
}

func TestLoadKeyNamesTheFileOnAFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signer.key")
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadKey(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v, want it to name %s", err, path)
	}
}

// TestFunctionsDeclareWhatTheHandlerAnswers pins the signature list
// against the two functions handle actually implements, so a third one
// added to handle without a matching entry here -- or the reverse --
// is a `sys.list_extensions` answer nobody can call.
func TestFunctionsDeclareWhatTheHandlerAnswers(t *testing.T) {
	names := map[string]bool{}
	for _, s := range functions() {
		names[s.Function] = true
	}
	for _, want := range []string{"sign", "public_key"} {
		if !names[want] {
			t.Errorf("functions() does not declare %q", want)
		}
	}
	if len(names) != 2 {
		t.Errorf("functions() declares %d functions, want exactly sign and public_key", len(names))
	}
}
