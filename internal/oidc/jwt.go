// Package oidc verifies OpenID Connect tokens and resolves an operator's
// roles from their claims.
//
// SPEC 23.4 calls this the modern path and the recommended one. It is
// written against `crypto/rsa`, `crypto/ecdsa`, and `encoding/json`
// because SPEC 4.2 allows no third-party code — and because a JWT
// library is a place where an accepted algorithm list is somebody
// else's default.
package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Algorithms SPEC 23.4 accepts. Nothing else verifies.
//
// The list is here, in one place, rather than being whatever a library
// defaults to. `none` is absent, and so is every `HS*`: a symmetric
// algorithm against a token from an external issuer is the algorithm
// confusion attack, where an attacker signs a token with the public key
// as the HMAC secret and a naive verifier accepts it.
var algorithms = map[string]algorithm{
	"RS256": {kind: keyRSA, hash: crypto.SHA256, pss: false},
	"RS384": {kind: keyRSA, hash: crypto.SHA384, pss: false},
	"RS512": {kind: keyRSA, hash: crypto.SHA512, pss: false},
	"PS256": {kind: keyRSA, hash: crypto.SHA256, pss: true},
	"PS384": {kind: keyRSA, hash: crypto.SHA384, pss: true},
	"PS512": {kind: keyRSA, hash: crypto.SHA512, pss: true},
	"ES256": {kind: keyEC, hash: crypto.SHA256, size: 32},
	"ES384": {kind: keyEC, hash: crypto.SHA384, size: 48},
	"ES512": {kind: keyEC, hash: crypto.SHA512, size: 66},
}

type keyKind int

const (
	keyRSA keyKind = iota
	keyEC
)

type algorithm struct {
	kind keyKind
	hash crypto.Hash
	pss  bool
	// size is the byte width of one half of an ECDSA signature, which
	// is fixed by the curve and is how r and s are split.
	size int
}

// Header is a JWT's first segment.
type Header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// Claims are the registered claims this build reads, plus everything
// else the issuer sent.
type Claims struct {
	Issuer    string   `json:"iss"`
	Subject   string   `json:"sub"`
	Audience  Audience `json:"aud"`
	Expiry    *int64   `json:"exp"`
	NotBefore *int64   `json:"nbf"`
	IssuedAt  *int64   `json:"iat"`
	Nonce     string   `json:"nonce"`
	Email     string   `json:"email"`
	Name      string   `json:"preferred_username"`

	// All is every claim as sent, for the role mapping to read a path
	// out of. SPEC 23.4 lets an estate name the claim its groups are
	// in, and it is not always one of the above.
	All map[string]any `json:"-"`
}

// Audience is `aud`, which the specification allows to be a string or an
// array of strings.
type Audience []string

func (a *Audience) UnmarshalJSON(raw []byte) error {
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		*a = Audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return fmt.Errorf("aud is neither a string nor a list of strings")
	}
	*a = Audience(many)
	return nil
}

func (a Audience) contains(want string) bool {
	for _, got := range a {
		if got == want {
			return true
		}
	}
	return false
}

// VerifyOptions is what a token is checked against.
type VerifyOptions struct {
	// Issuer must equal the token's `iss` exactly.
	Issuer string
	// Audience must appear in the token's `aud`.
	Audience string
	// Nonce, when set, must equal the token's. It is set for an
	// interactive login and empty for client credentials, which has no
	// browser round trip to bind.
	Nonce string
	// Skew is how much clock difference is tolerated on `exp` and
	// `nbf`. Small on purpose: it is an allowance for drift, not a
	// grace period.
	Skew time.Duration
	// Now is the clock, for the tests.
	Now func() time.Time
}

func (o VerifyOptions) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o VerifyOptions) skew() time.Duration {
	if o.Skew <= 0 {
		return 60 * time.Second
	}
	if o.Skew > 5*time.Minute {
		// A skew large enough to matter is a broken clock, and a
		// verifier that tolerates it is accepting expired tokens.
		return 5 * time.Minute
	}
	return o.Skew
}

// ErrUnknownKey is what Verify answers when the token names a key the
// set does not have, so a caller knows a refresh is worth trying.
var ErrUnknownKey = errors.New("the token names a key this key set does not have")

// Parse splits a token and reads its header and claims without checking
// anything.
//
// Separate from Verify so that nothing in this package can be tempted to
// read a claim before the signature is checked: Verify is the only way
// to get claims that mean anything, and this returns them for the one
// legitimate use, which is finding out which key to fetch.
func Parse(token string) (Header, Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Header{}, Claims{}, fmt.Errorf("a JWT has three segments, this has %d", len(parts))
	}
	headerJSON, err := decodeSegment(parts[0])
	if err != nil {
		return Header{}, Claims{}, fmt.Errorf("the header is not readable: %w", err)
	}
	var header Header
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return Header{}, Claims{}, fmt.Errorf("the header is not readable: %w", err)
	}
	claimsJSON, err := decodeSegment(parts[1])
	if err != nil {
		return Header{}, Claims{}, fmt.Errorf("the claims are not readable: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return Header{}, Claims{}, fmt.Errorf("the claims are not readable: %w", err)
	}
	if err := json.Unmarshal(claimsJSON, &claims.All); err != nil {
		return Header{}, Claims{}, fmt.Errorf("the claims are not readable: %w", err)
	}
	return header, claims, nil
}

// Verify checks a token's signature against a key set and its claims
// against the options.
//
// The order matters and is deliberate: the algorithm is resolved from
// this package's own table, the key is chosen by `kid` and checked
// against the algorithm's key type, the signature is verified, and only
// then is a single claim read for meaning.
func Verify(token string, keys *KeySet, opts VerifyOptions) (Claims, error) {
	header, claims, err := Parse(token)
	if err != nil {
		return Claims{}, err
	}

	alg, ok := algorithms[header.Alg]
	if !ok {
		// Named rather than described, because "unsupported algorithm"
		// sends an operator to the wrong place when the answer is that
		// their provider is configured for HS256.
		return Claims{}, fmt.Errorf("the token is signed with %q, which is refused; SPEC 23.4 accepts %s",
			header.Alg, strings.Join(AcceptedAlgorithms(), ", "))
	}

	key, err := keys.find(header.Kid, header.Alg)
	if err != nil {
		return Claims{}, err
	}

	parts := strings.Split(token, ".")
	signed := parts[0] + "." + parts[1]
	signature, err := decodeSegment(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("the signature is not readable: %w", err)
	}
	if err := verifySignature(alg, key, []byte(signed), signature); err != nil {
		return Claims{}, err
	}

	if err := verifyClaims(claims, opts); err != nil {
		return Claims{}, err
	}
	return claims, nil
}

// AcceptedAlgorithms is the list SPEC 23.4 names, in order.
func AcceptedAlgorithms() []string {
	return []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}
}

func verifySignature(alg algorithm, key any, signed, signature []byte) error {
	digest := hashOf(alg.hash, signed)

	switch alg.kind {
	case keyRSA:
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			// The header said RSA and the key is not one. Accepting it
			// on the strength of the header is the confusion this check
			// exists to stop.
			return fmt.Errorf("the token is signed with an RSA algorithm and its key is not an RSA key")
		}
		if alg.pss {
			return rsa.VerifyPSS(pub, alg.hash, digest, signature,
				&rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: alg.hash})
		}
		return rsa.VerifyPKCS1v15(pub, alg.hash, digest, signature)
	case keyEC:
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("the token is signed with an ECDSA algorithm and its key is not an ECDSA key")
		}
		// JWS packs r and s as fixed-width big-endian halves, not as
		// the ASN.1 sequence `ecdsa.VerifyASN1` reads.
		if len(signature) != alg.size*2 {
			return fmt.Errorf("the signature is %d bytes and this curve needs %d",
				len(signature), alg.size*2)
		}
		r := new(big.Int).SetBytes(signature[:alg.size])
		s := new(big.Int).SetBytes(signature[alg.size:])
		if !ecdsa.Verify(pub, digest, r, s) {
			return errors.New("the signature did not verify")
		}
		return nil
	}
	return errors.New("the signature did not verify")
}

func hashOf(h crypto.Hash, b []byte) []byte {
	switch h {
	case crypto.SHA384:
		sum := sha512.Sum384(b)
		return sum[:]
	case crypto.SHA512:
		sum := sha512.Sum512(b)
		return sum[:]
	default:
		sum := sha256.Sum256(b)
		return sum[:]
	}
}

// verifyClaims checks everything SPEC 23.4 names.
func verifyClaims(c Claims, opts VerifyOptions) error {
	if opts.Issuer != "" && c.Issuer != opts.Issuer {
		return fmt.Errorf("the token is from %q and this service accepts %q", c.Issuer, opts.Issuer)
	}
	if opts.Audience != "" && !c.Audience.contains(opts.Audience) {
		return fmt.Errorf("the token is for %s and this service is %q",
			describeAudience(c.Audience), opts.Audience)
	}
	// SPEC 23.4: a token without an `exp` is refused. A token that
	// never expires is a password with a longer name.
	if c.Expiry == nil {
		return errors.New("the token has no expiry, and one that never expires is refused")
	}
	now := opts.now()
	skew := opts.skew()
	if now.After(time.Unix(*c.Expiry, 0).Add(skew)) {
		return fmt.Errorf("the token expired at %s", time.Unix(*c.Expiry, 0).UTC().Format(time.RFC3339))
	}
	if c.NotBefore != nil && now.Add(skew).Before(time.Unix(*c.NotBefore, 0)) {
		return fmt.Errorf("the token is not valid until %s",
			time.Unix(*c.NotBefore, 0).UTC().Format(time.RFC3339))
	}
	if opts.Nonce != "" && c.Nonce != opts.Nonce {
		// The nonce binds this token to the login that asked for it. A
		// mismatch is a replayed authorization response.
		return errors.New("the token's nonce does not match this login")
	}
	return nil
}

func describeAudience(a Audience) string {
	if len(a) == 0 {
		return "no audience"
	}
	return strings.Join(a, ", ")
}

// decodeSegment reads base64url without padding, which is what JWS uses.
func decodeSegment(seg string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(seg)
}
