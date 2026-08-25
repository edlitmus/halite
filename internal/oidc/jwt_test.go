package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

// signer holds a key pair and mints tokens with it, the way a provider
// would.
type signer struct {
	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey
	kid    string
}

func newRSASigner(t *testing.T, kid string) *signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &signer{rsaKey: key, kid: kid}
}

func newECSigner(t *testing.T, kid string) *signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &signer{ecKey: key, kid: kid}
}

func seg(v any) string {
	raw, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// token mints a signed JWT with the given algorithm and claims.
func (s *signer) token(t *testing.T, alg string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": alg, "typ": "JWT"}
	if s.kid != "" {
		header["kid"] = s.kid
	}
	signed := seg(header) + "." + seg(claims)
	digest := sha256.Sum256([]byte(signed))

	var signature []byte
	var err error
	switch alg {
	case "RS256":
		signature, err = rsa.SignPKCS1v15(rand.Reader, s.rsaKey, crypto.SHA256, digest[:])
	case "PS256":
		signature, err = rsa.SignPSS(rand.Reader, s.rsaKey, crypto.SHA256, digest[:],
			&rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256})
	case "ES256":
		var r, sv *big.Int
		r, sv, err = ecdsa.Sign(rand.Reader, s.ecKey, digest[:])
		if err == nil {
			signature = append(leftPad(r.Bytes(), 32), leftPad(sv.Bytes(), 32)...)
		}
	default:
		t.Fatalf("the test signer cannot sign %s", alg)
	}
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func leftPad(b []byte, width int) []byte {
	if len(b) >= width {
		return b
	}
	out := make([]byte, width)
	copy(out[width-len(b):], b)
	return out
}

// keySetOf builds a key set holding these public keys, without a fetch.
func keySetOf(keys map[string]any) *KeySet {
	return &KeySet{keys: keys, expiresAt: time.Now().Add(time.Hour)}
}

func goodClaims(exp time.Time) map[string]any {
	return map[string]any{
		"iss": "https://idp.example",
		"sub": "ed",
		"aud": "halite",
		"exp": exp.Unix(),
		"iat": time.Now().Unix(),
	}
}

func goodOpts() VerifyOptions {
	return VerifyOptions{Issuer: "https://idp.example", Audience: "halite"}
}

func TestAValidTokenVerifies(t *testing.T) {
	s := newRSASigner(t, "k1")
	keys := keySetOf(map[string]any{"k1": &s.rsaKey.PublicKey})
	token := s.token(t, "RS256", goodClaims(time.Now().Add(time.Hour)))

	claims, err := Verify(token, keys, goodOpts())
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "ed" {
		t.Errorf("the subject is %q", claims.Subject)
	}
	if claims.All["iss"] != "https://idp.example" {
		t.Errorf("the raw claims are missing: %v", claims.All)
	}
}

func TestECDSAVerifies(t *testing.T) {
	s := newECSigner(t, "k1")
	keys := keySetOf(map[string]any{"k1": &s.ecKey.PublicKey})
	token := s.token(t, "ES256", goodClaims(time.Now().Add(time.Hour)))

	if _, err := Verify(token, keys, goodOpts()); err != nil {
		t.Fatal(err)
	}
}

func TestPSSVerifies(t *testing.T) {
	s := newRSASigner(t, "k1")
	keys := keySetOf(map[string]any{"k1": &s.rsaKey.PublicKey})
	token := s.token(t, "PS256", goodClaims(time.Now().Add(time.Hour)))

	if _, err := Verify(token, keys, goodOpts()); err != nil {
		t.Fatal(err)
	}
}

// `alg: none` is the original JWT vulnerability and it is refused by
// name, because the accepted list is this package's own rather than a
// library's default.
func TestAlgNoneIsRefused(t *testing.T) {
	claims := goodClaims(time.Now().Add(time.Hour))
	token := seg(map[string]any{"alg": "none", "typ": "JWT"}) + "." + seg(claims) + "."

	_, err := Verify(token, keySetOf(map[string]any{"k1": &newRSASigner(t, "k1").rsaKey.PublicKey}), goodOpts())
	if err == nil {
		t.Fatal("an unsigned token verified")
	}
	if !strings.Contains(err.Error(), "none") {
		t.Errorf("the refusal does not name the algorithm: %v", err)
	}
}

// The algorithm confusion attack: an attacker takes the provider's
// public key, signs a token with it as an HMAC secret, and claims HS256.
// A verifier that trusts the header's `alg` accepts it.
func TestAnHMACTokenSignedWithThePublicKeyIsRefused(t *testing.T) {
	s := newRSASigner(t, "k1")
	pub := &s.rsaKey.PublicKey

	claims := goodClaims(time.Now().Add(time.Hour))
	signed := seg(map[string]any{"alg": "HS256", "kid": "k1", "typ": "JWT"}) + "." + seg(claims)
	mac := hmac.New(sha256.New, pub.N.Bytes())
	mac.Write([]byte(signed))
	token := signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if _, err := Verify(token, keySetOf(map[string]any{"k1": pub}), goodOpts()); err == nil {
		t.Fatal("a token signed with the public key as an HMAC secret verified")
	}
}

// The other half of the same class: the header says RSA and the key
// found is an EC key. Verifying on the strength of the header is what
// must not happen.
func TestAnAlgorithmThatDoesNotMatchTheKeyTypeIsRefused(t *testing.T) {
	ec := newECSigner(t, "k1")
	rsaSigner := newRSASigner(t, "k1")
	// The token is a genuine RS256 token; the set holds an EC key
	// under that `kid`.
	token := rsaSigner.token(t, "RS256", goodClaims(time.Now().Add(time.Hour)))

	_, err := Verify(token, keySetOf(map[string]any{"k1": &ec.ecKey.PublicKey}), goodOpts())
	if err == nil {
		t.Fatal("an RSA algorithm verified against an EC key")
	}
	if !strings.Contains(err.Error(), "RSA") {
		t.Errorf("the refusal does not say what happened: %v", err)
	}
}

func TestATamperedTokenIsRefused(t *testing.T) {
	s := newRSASigner(t, "k1")
	keys := keySetOf(map[string]any{"k1": &s.rsaKey.PublicKey})
	token := s.token(t, "RS256", goodClaims(time.Now().Add(time.Hour)))

	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + seg(map[string]any{
		"iss": "https://idp.example", "sub": "root", "aud": "halite",
		"exp": time.Now().Add(time.Hour).Unix(),
	}) + "." + parts[2]

	if _, err := Verify(tampered, keys, goodOpts()); err == nil {
		t.Fatal("a token whose subject was changed verified")
	}
}

// SPEC 23.4: a token without an `exp` is refused. One that never
// expires is a password with a longer name.
func TestATokenWithNoExpiryIsRefused(t *testing.T) {
	s := newRSASigner(t, "k1")
	keys := keySetOf(map[string]any{"k1": &s.rsaKey.PublicKey})
	token := s.token(t, "RS256", map[string]any{
		"iss": "https://idp.example", "sub": "ed", "aud": "halite",
	})

	_, err := Verify(token, keys, goodOpts())
	if err == nil {
		t.Fatal("a token with no expiry verified")
	}
	if !strings.Contains(err.Error(), "expiry") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

func TestTheRegisteredClaimsAreChecked(t *testing.T) {
	s := newRSASigner(t, "k1")
	keys := keySetOf(map[string]any{"k1": &s.rsaKey.PublicKey})
	now := time.Now()

	cases := []struct {
		name   string
		claims map[string]any
		opts   VerifyOptions
		want   string
	}{
		{
			name:   "expired",
			claims: goodClaims(now.Add(-2 * time.Hour)),
			opts:   goodOpts(),
			want:   "expired",
		},
		{
			name: "not yet valid",
			claims: withClaim(goodClaims(now.Add(time.Hour)),
				"nbf", now.Add(time.Hour).Unix()),
			opts: goodOpts(),
			want: "not valid until",
		},
		{
			name:   "another issuer",
			claims: withClaim(goodClaims(now.Add(time.Hour)), "iss", "https://evil.example"),
			opts:   goodOpts(),
			want:   "this service accepts",
		},
		{
			name:   "another audience",
			claims: withClaim(goodClaims(now.Add(time.Hour)), "aud", "someone-else"),
			opts:   goodOpts(),
			want:   "this service is",
		},
		{
			name:   "a replayed nonce",
			claims: withClaim(goodClaims(now.Add(time.Hour)), "nonce", "old"),
			opts:   VerifyOptions{Issuer: "https://idp.example", Audience: "halite", Nonce: "fresh"},
			want:   "nonce",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Verify(s.token(t, "RS256", c.claims), keys, c.opts)
			if err == nil {
				t.Fatalf("%s was accepted", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal is %v, want it to mention %q", err, c.want)
			}
		})
	}
}

// `aud` may be a string or an array, and a token for several audiences
// including this one is valid.
func TestAnAudienceArrayIsAccepted(t *testing.T) {
	s := newRSASigner(t, "k1")
	keys := keySetOf(map[string]any{"k1": &s.rsaKey.PublicKey})
	claims := withClaim(goodClaims(time.Now().Add(time.Hour)),
		"aud", []string{"something-else", "halite"})

	if _, err := Verify(s.token(t, "RS256", claims), keys, goodOpts()); err != nil {
		t.Errorf("a token listing this audience among others was refused: %v", err)
	}
}

// Guessing which key to try, and accepting the token if any verifies,
// turns key rotation into an attack surface.
func TestATokenWithNoKidIsRefusedWhenTheSetHasSeveral(t *testing.T) {
	a := newRSASigner(t, "")
	b := newRSASigner(t, "")
	keys := keySetOf(map[string]any{
		"k1": &a.rsaKey.PublicKey,
		"k2": &b.rsaKey.PublicKey,
	})
	token := a.token(t, "RS256", goodClaims(time.Now().Add(time.Hour)))

	if _, err := Verify(token, keys, goodOpts()); err == nil {
		t.Fatal("a token with no kid verified against a set of two keys")
	}
}

// A set with one key is the common small deployment, and a token with no
// `kid` works there.
func TestATokenWithNoKidWorksWhenTheSetHasOne(t *testing.T) {
	s := newRSASigner(t, "")
	keys := keySetOf(map[string]any{"k1": &s.rsaKey.PublicKey})

	if _, err := Verify(s.token(t, "RS256", goodClaims(time.Now().Add(time.Hour))), keys, goodOpts()); err != nil {
		t.Errorf("a single-key set refused a token with no kid: %v", err)
	}
}

// Clock skew is an allowance for drift, not a grace period.
func TestSkewIsBounded(t *testing.T) {
	s := newRSASigner(t, "k1")
	keys := keySetOf(map[string]any{"k1": &s.rsaKey.PublicKey})
	expired := goodClaims(time.Now().Add(-30 * time.Minute))

	opts := goodOpts()
	opts.Skew = 24 * time.Hour
	if _, err := Verify(s.token(t, "RS256", expired), keys, opts); err == nil {
		t.Error("a day of skew was honoured, and a token expired half an hour ago verified")
	}
}

func TestAMalformedTokenIsRefusedByShape(t *testing.T) {
	keys := keySetOf(map[string]any{"k1": &newRSASigner(t, "k1").rsaKey.PublicKey})
	for _, token := range []string{"", "a", "a.b", "a.b.c.d", "!!!.???.***"} {
		if _, err := Verify(token, keys, goodOpts()); err == nil {
			t.Errorf("%q verified", token)
		}
	}
}

func withClaim(claims map[string]any, key string, value any) map[string]any {
	out := map[string]any{}
	for k, v := range claims {
		out[k] = v
	}
	out[key] = value
	return out
}
