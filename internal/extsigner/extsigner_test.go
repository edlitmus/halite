package extsigner

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/edlitmus/halite/ext"
)

// fakeCaller scripts a signer extension's answers, the way
// internal/extpillar's own tests drive Bridged without a subprocess.
type fakeCaller struct {
	fn      map[string]json.RawMessage
	err     map[string]error
	lastArg json.RawMessage
}

func (f *fakeCaller) Call(_ context.Context, function string, _, kwargs any, _ *ext.CallContext) (json.RawMessage, error) {
	if kwargs != nil {
		f.lastArg, _ = json.Marshal(kwargs)
	}
	if err, ok := f.err[function]; ok {
		return nil, err
	}
	return f.fn[function], nil
}

func TestBridgedSignSendsTheDigestAndDecodesTheSignature(t *testing.T) {
	f := &fakeCaller{fn: map[string]json.RawMessage{
		"sign": json.RawMessage(`{"signature":"aGVsbG8="}`), // base64("hello")
	}}
	b := &Bridged{Name: "test", Ext: f}

	digest := [32]byte{1, 2, 3}
	got, err := b.Sign(context.Background(), digest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("signature = %q, want %q", got, "hello")
	}
	if !strings.Contains(string(f.lastArg), base64.StdEncoding.EncodeToString(digest[:])) {
		t.Errorf("the digest sent was %s, want it to carry the base64 digest", f.lastArg)
	}
}

func TestBridgedSignRejectsAMalformedAnswer(t *testing.T) {
	for name, fn := range map[string]json.RawMessage{
		"not json":   json.RawMessage(`not json`),
		"empty":      json.RawMessage(`{}`),
		"not base64": json.RawMessage(`{"signature":"not base64!!"}`),
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeCaller{fn: map[string]json.RawMessage{"sign": fn}}
			b := &Bridged{Name: "test", Ext: f}
			if _, err := b.Sign(context.Background(), [32]byte{}); err == nil {
				t.Error("a malformed answer was accepted")
			}
		})
	}
}

func TestBridgedPublicKeyDecodesAPKIXKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(der)})

	f := &fakeCaller{fn: map[string]json.RawMessage{"public_key": resp}}
	b := &Bridged{Name: "test", Ext: f}

	got, err := b.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(&key.PublicKey) {
		t.Error("the decoded public key does not match the one encoded")
	}
}

func TestBridgedPublicKeyRejectsANonECDSAKey(t *testing.T) {
	// An Ed25519 key, DER-encoded PKIX -- valid PKIX, wrong algorithm
	// for job signing, which must be refused by name rather than by a
	// panic on the type assertion.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := json.Marshal(map[string]string{"public_key": base64.StdEncoding.EncodeToString(der)})
	f := &fakeCaller{fn: map[string]json.RawMessage{"public_key": resp}}
	b := &Bridged{Name: "test", Ext: f}
	if _, err := b.PublicKey(context.Background()); err == nil {
		t.Error("an Ed25519 public key was accepted for job signing, which is ECDSA")
	}
}

func TestBridgedRefusesWithNoExtensionLoaded(t *testing.T) {
	b := &Bridged{Name: "test"}
	if _, err := b.Sign(context.Background(), [32]byte{}); err == nil {
		t.Error("Sign with no Ext succeeded")
	}
	if _, err := b.PublicKey(context.Background()); err == nil {
		t.Error("PublicKey with no Ext succeeded")
	}
}

func TestBridgedCallErrorIsSurfaced(t *testing.T) {
	f := &fakeCaller{err: map[string]error{"sign": errors.New("refused")}}
	b := &Bridged{Name: "test", Ext: f}
	_, err := b.Sign(context.Background(), [32]byte{})
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("err = %v, want it to carry the extension's own refusal", err)
	}
}
