package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxJWKSBody bounds a key set document. A provider's is a few
// kilobytes; this is generous and stops a compromised or hostile
// endpoint from being a way to exhaust this process.
const MaxJWKSBody = 1 << 20

// MinRefreshInterval is how often an unknown `kid` may cause a fetch.
//
// SPEC 23.4 asks for a bounded refresh, and this is the bound. Without
// it a stream of tokens naming key identifiers that do not exist is a
// way to make this service hammer the provider — a denial of service
// aimed through us at somebody else.
const MinRefreshInterval = 60 * time.Second

// KeySet holds a provider's public keys.
//
// It is safe for concurrent use, refreshes on its own schedule, and
// refreshes once out of turn when a token names a key it does not have,
// which is what makes a provider's key rotation invisible here.
type KeySet struct {
	// URL is the `jwks_uri` from the provider's discovery document.
	URL string
	// Client is how it is fetched. Nil takes a client with a timeout,
	// because a key set fetch inside a login must not hang the login.
	Client *http.Client
	// Now is the clock, for the tests.
	Now func() time.Time

	mu          sync.Mutex
	keys        map[string]any
	fetchedAt   time.Time
	expiresAt   time.Time
	lastAttempt time.Time
}

// NewKeySet answers with a key set that has fetched nothing yet.
func NewKeySet(url string) *KeySet {
	return &KeySet{URL: url}
}

func (k *KeySet) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

func (k *KeySet) client() *http.Client {
	if k.Client != nil {
		return k.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// find returns the key a token names.
//
// A set with one key and a token with no `kid` is the common small
// deployment, and it works. A set with several and a token with no
// `kid` is refused: guessing which key to try, and accepting a token if
// any of them verifies, turns key rotation into an attack surface.
func (k *KeySet) find(kid, alg string) (any, error) {
	if key, ok := k.lookup(kid); ok {
		return key, nil
	}
	// One fetch out of turn, rate limited. A rotation the provider has
	// already published should not need a restart here.
	if err := k.refresh(context.Background(), false); err != nil {
		return nil, fmt.Errorf("%w (%s)", ErrUnknownKey, err)
	}
	if key, ok := k.lookup(kid); ok {
		return key, nil
	}
	if kid == "" {
		return nil, fmt.Errorf("the token names no key and this key set has %d; "+
			"a token must carry a `kid` when the provider publishes more than one", k.size())
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownKey, kid)
}

func (k *KeySet) lookup(kid string) (any, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.keys) == 0 {
		return nil, false
	}
	if kid != "" {
		key, ok := k.keys[kid]
		return key, ok
	}
	if len(k.keys) != 1 {
		return nil, false
	}
	for _, key := range k.keys {
		return key, true
	}
	return nil, false
}

func (k *KeySet) size() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.keys)
}

// Refresh fetches the key set if it is stale.
func (k *KeySet) Refresh(ctx context.Context) error {
	return k.refresh(ctx, true)
}

func (k *KeySet) refresh(ctx context.Context, scheduled bool) error {
	k.mu.Lock()
	now := k.now()
	fresh := len(k.keys) > 0 && now.Before(k.expiresAt)
	if scheduled && fresh {
		k.mu.Unlock()
		return nil
	}
	if !scheduled && now.Sub(k.lastAttempt) < MinRefreshInterval {
		k.mu.Unlock()
		return fmt.Errorf("this key set was fetched less than %s ago", MinRefreshInterval)
	}
	k.lastAttempt = now
	url := k.URL
	k.mu.Unlock()

	if url == "" {
		return fmt.Errorf("this key set has no URL")
	}
	keys, ttl, err := fetchKeys(ctx, k.client(), url)
	if err != nil {
		return err
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	k.keys = keys
	k.fetchedAt = k.now()
	k.expiresAt = k.fetchedAt.Add(ttl)
	return nil
}

func fetchKeys(ctx context.Context, client *http.Client, url string) (map[string]any, time.Duration, error) {
	if !strings.HasPrefix(url, "https://") {
		// The keys that decide who an operator is do not travel over
		// plaintext.
		return nil, 0, fmt.Errorf("a key set URL must be https://; %q is not", url)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("%s answered %d", url, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxJWKSBody))
	if err != nil {
		return nil, 0, err
	}
	var doc struct {
		Keys []jsonWebKey `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, 0, fmt.Errorf("%s is not a key set: %w", url, err)
	}
	keys := map[string]any{}
	for _, jwk := range doc.Keys {
		// A key published for encryption is not one to verify a
		// signature with, and a provider publishes both.
		if jwk.Use != "" && jwk.Use != "sig" {
			continue
		}
		key, err := jwk.publicKey()
		if err != nil {
			// One unreadable key does not make the set unusable: a
			// provider that adds a key type this build does not know
			// should not lock every operator out.
			continue
		}
		keys[jwk.Kid] = key
	}
	if len(keys) == 0 {
		return nil, 0, fmt.Errorf("%s published no usable signing key", url)
	}
	return keys, cacheFor(res.Header), nil
}

// cacheFor reads `Cache-Control: max-age`, which SPEC 23.4 asks to be
// respected.
//
// Bounded at both ends. A provider that says five seconds would have
// this service fetch on every login; one that says a year would make a
// compromised key valid here long after it was withdrawn.
func cacheFor(h http.Header) time.Duration {
	const (
		floor   = 5 * time.Minute
		ceiling = 24 * time.Hour
	)
	for _, part := range strings.Split(h.Get("Cache-Control"), ",") {
		part = strings.TrimSpace(part)
		value, ok := strings.CutPrefix(part, "max-age=")
		if !ok {
			continue
		}
		seconds, err := strconv.Atoi(value)
		if err != nil {
			continue
		}
		d := time.Duration(seconds) * time.Second
		if d < floor {
			return floor
		}
		if d > ceiling {
			return ceiling
		}
		return d
	}
	return time.Hour
}

// jsonWebKey is RFC 7517, in the two key types SPEC 23.4's algorithms
// need.
type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	// RSA.
	N string `json:"n"`
	E string `json:"e"`
	// EC.
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (j jsonWebKey) publicKey() (any, error) {
	switch j.Kty {
	case "RSA":
		n, err := base64.RawURLEncoding.DecodeString(j.N)
		if err != nil {
			return nil, err
		}
		e, err := base64.RawURLEncoding.DecodeString(j.E)
		if err != nil {
			return nil, err
		}
		if len(e) == 0 || len(e) > 8 {
			return nil, fmt.Errorf("the exponent is %d bytes", len(e))
		}
		exponent := 0
		for _, b := range e {
			exponent = exponent<<8 | int(b)
		}
		key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exponent}
		// Below this a signature is forgeable with commodity hardware,
		// and a provider publishing one is misconfigured rather than
		// making a choice this service should honour.
		if key.N.BitLen() < 2048 {
			return nil, fmt.Errorf("the RSA key is %d bits, and 2048 is the floor", key.N.BitLen())
		}
		return key, nil
	case "EC":
		var curve elliptic.Curve
		switch j.Crv {
		case "P-256":
			curve = elliptic.P256()
		case "P-384":
			curve = elliptic.P384()
		case "P-521":
			curve = elliptic.P521()
		default:
			return nil, fmt.Errorf("%q is not a curve this build verifies against", j.Crv)
		}
		x, err := base64.RawURLEncoding.DecodeString(j.X)
		if err != nil {
			return nil, err
		}
		y, err := base64.RawURLEncoding.DecodeString(j.Y)
		if err != nil {
			return nil, err
		}
		key := &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !curve.IsOnCurve(key.X, key.Y) {
			// A point that is not on the curve is not a key. Accepting
			// one is how invalid-curve attacks start.
			return nil, fmt.Errorf("the EC key's point is not on %s", j.Crv)
		}
		return key, nil
	}
	return nil, fmt.Errorf("%q is not a key type this build verifies against", j.Kty)
}
