package jobsign

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Values of different types encode differently, however alike their text.
//
// v1 wrote a keyword value as its text, and each of the pairs below was
// the same bytes under it -- reproduced before this was changed. The
// first is the one that mattered: a node runs a dry run only for the
// boolean, so a hub could swap a signed `true` for `"true"` and the job
// ran for real under a signature that still verified. DIVERGENCE 5.290.
func TestValuesOfDifferentTypesEncodeDifferently(t *testing.T) {
	pairs := []struct {
		name       string
		one, other any
	}{
		{"a boolean and its text", true, "true"},
		{"false and its text", false, "false"},
		{"a list and its JSON", []any{"a", "b"}, `["a","b"]`},
		{"nil and its text", nil, "null"},
		{"a number and its text", json.Number("1"), "1"},
		{"a mapping and its JSON", map[string]any{"a": "b"}, `{"a":"b"}`},
		{"a nested boolean and its text", []any{true}, []any{"true"}},
		{"a list and a mapping", []any{"k", "v"}, map[string]any{"k": "v"}},
		{"an empty list and an empty mapping", []any{}, map[string]any{}},
		{"an empty string and nil", "", nil},
		// Re-cutting a container: two elements against one whose text
		// spans both.
		{"two strings and one", []any{"a", "b"}, []any{"a;str:1:b"}},
	}
	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			one, other := payload(), payload()
			one.Kwarg = map[string]any{"v": p.one}
			other.Kwarg = map[string]any{"v": p.other}
			if bytes.Equal(Canonical(one), Canonical(other)) {
				t.Errorf("%#v and %#v have the same canonical encoding:\n%s", p.one, p.other, Canonical(one))
			}
		})
	}
}

// The attack itself, end to end through Sign and Verify: a signed dry run
// with its `test` turned into a string does not verify.
func TestADryRunTamperedToAStringDoesNotVerify(t *testing.T) {
	k, err := GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	trusted := SignerKey{Name: "ops", Key: &k.PublicKey}
	signed := payload()
	signed.Kwarg = map[string]any{"test": true}
	sig, err := Sign(k, signed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify([]SignerKey{trusted}, signed, sig); err != nil {
		t.Fatalf("the untampered job does not verify: %v", err)
	}
	for _, swapped := range []any{"true", "True", json.Number("1"), []any{true}} {
		tampered := payload()
		tampered.Kwarg = map[string]any{"test": swapped}
		if _, err := Verify([]SignerKey{trusted}, tampered, sig); err == nil {
			t.Errorf("a signed `test: true` changed to %#v still verifies", swapped)
		}
	}
}

// The encoding is pinned byte for byte, because docs/operations.md
// states it for a signer that is not this program, and a change here that
// the page does not follow is a signer somewhere that stops working.
func TestTheEncodingIsTheDocumentedOne(t *testing.T) {
	p := Payload{
		JID:        "20261015T120000000001",
		Target:     "web1",
		TargetKind: "glob",
		Fun:        "state.apply",
		Arg:        []string{"nginx"},
		Kwarg: map[string]any{
			"test":   true,
			"pillar": map[string]any{"n": json.Number("3"), "l": []any{"a", nil}},
		},
		Env:     "base",
		Expires: time.Date(2026, 10, 15, 12, 15, 0, 0, time.UTC),
	}
	want := "halite-job-v2\x00" +
		"jid:21:20261015T120000000001;" +
		"target:4:web1;" +
		"target_kind:4:glob;" +
		"fun:11:state.apply;" +
		"argc:1;arg:5:nginx;" +
		"kwargc:2;" +
		"kwarg:6:pillar;map:2;key:1:l;list:2;str:1:a;null;key:1:n;num:1:3;" +
		"kwarg:4:test;bool:true;" +
		"env:4:base;" +
		"expires:20:2026-10-15T12:15:00Z;"
	if got := string(Canonical(p)); got != want {
		t.Errorf("the canonical encoding changed:\n got %q\nwant %q", got, want)
	}
}

// A signature over the v1 bytes is refused, and the refusal says why.
//
// Refused because v1 is the ambiguous format: accepting it alongside v2
// would leave the hole open for as long as it was accepted. Named,
// because during an upgrade the cause is a signer older than the node,
// and "does not verify" would send an operator looking at the key.
func TestAVersionOneSignatureIsRefusedByName(t *testing.T) {
	k, err := GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	p := payload()
	digest := sha256.Sum256(canonicalV1(p))
	der, err := ecdsa.SignASN1(rand.Reader, k, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify([]SignerKey{{Name: "ops", Key: &k.PublicKey}}, p, base64.StdEncoding.EncodeToString(der))
	if err == nil {
		t.Fatal("a v1 signature verified")
	}
	if !errors.Is(err, ErrOldFormat) {
		t.Errorf("the refusal does not say the signature is in the old format: %v", err)
	}
}

// Neither end deals in a signature without an expiry.
func TestASignatureWithoutAnExpiryIsRefused(t *testing.T) {
	k, err := GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	p := payload()
	p.Expires = time.Time{}
	if _, err := Sign(k, p); !errors.Is(err, ErrNoExpiry) {
		t.Errorf("Sign signed a job with no expiry: %v", err)
	}
	// Signed directly over the digest, as a bridged signer or an
	// external one would, so that Verify is what is tested.
	digest := Digest(p)
	der, err := ecdsa.SignASN1(rand.Reader, k, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	_, err = Verify([]SignerKey{{Name: "ops", Key: &k.PublicKey}}, p, base64.StdEncoding.EncodeToString(der))
	if !errors.Is(err, ErrNoExpiry) {
		t.Errorf("Verify accepted, or refused for another reason, a signature with no expiry: %v", err)
	}
}
