package oidc

import (
	"context"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// keyServer publishes a set that can be rotated and counts fetches.
type keyServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	fetches int
	kid     string
	signer  *signer
	cache   string
	// status, when set, is answered instead of the set.
	status int
	// gate, when set, holds every fetch until it is closed.
	gate chan struct{}
}

func newKeyServer(t *testing.T, kid string) *keyServer {
	t.Helper()
	k := &keyServer{kid: kid, signer: newRSASigner(t, kid), cache: "max-age=3600"}
	k.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.fetches++
		kid, pub, cache, status, gate := k.kid, &k.signer.rsaKey.PublicKey, k.cache, k.status, k.gate
		k.mu.Unlock()
		if gate != nil {
			<-gate
		}
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if cache != "" {
			w.Header().Set("Cache-Control", cache)
		}
		writeJSON(w, map[string]any{"keys": []jsonWebKey{{
			Kty: "RSA", Kid: kid, Use: "sig",
			N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	}))
	t.Cleanup(k.server.Close)
	return k
}

func (k *keyServer) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.fetches
}

func (k *keyServer) rotate(t *testing.T, kid string) {
	t.Helper()
	k.mu.Lock()
	defer k.mu.Unlock()
	k.kid, k.signer = kid, newRSASigner(t, kid)
}

func keySetFor(k *keyServer, now func() time.Time) *KeySet {
	return &KeySet{URL: k.server.URL, Client: k.server.Client(), Now: now}
}

func TestAKeySetIsFetchedOnceAndCached(t *testing.T) {
	k := newKeyServer(t, "k1")
	clock := time.Now()
	set := keySetFor(k, func() time.Time { return clock })

	for i := 0; i < 3; i++ {
		if err := set.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if k.count() != 1 {
		t.Errorf("%d fetches for three refreshes inside the cache window", k.count())
	}
	// Past `max-age`, it fetches again.
	clock = clock.Add(2 * time.Hour)
	if err := set.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if k.count() != 2 {
		t.Errorf("%d fetches after the cache expired", k.count())
	}
}

// A provider rotates a key and publishes the new one. A token signed
// with it names a `kid` this set has not seen, and one fetch out of turn
// is what makes the rotation invisible here.
func TestAnUnknownKidCausesOneRefresh(t *testing.T) {
	k := newKeyServer(t, "k1")
	clock := time.Now()
	set := keySetFor(k, func() time.Time { return clock })
	if err := set.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	k.rotate(t, "k2")
	// The clock moves past the rate limit but not past the cache
	// window, so only the unknown `kid` can cause this fetch.
	clock = clock.Add(2 * MinRefreshInterval)

	token := k.signer.token(t, "RS256", goodClaims(clock.Add(time.Hour)))
	if _, err := Verify(token, set, VerifyOptions{
		Issuer: "https://idp.example", Audience: "halite",
		Now: func() time.Time { return clock },
	}); err != nil {
		t.Fatalf("a token signed with a rotated key was refused: %v", err)
	}
	if k.count() != 2 {
		t.Errorf("%d fetches; the rotation should have caused exactly one more", k.count())
	}
}

// Without a bound, a stream of tokens naming key identifiers that do not
// exist makes this service hammer the provider — a denial of service
// aimed through us at somebody else.
func TestTheRefreshOnAnUnknownKidIsRateLimited(t *testing.T) {
	k := newKeyServer(t, "k1")
	clock := time.Now()
	set := keySetFor(k, func() time.Time { return clock })
	if err := set.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The set was just fetched, so an unknown `kid` immediately after
	// is refused without a second fetch: the key genuinely is not
	// there, and asking again in the same second cannot change that.
	for i := 0; i < 20; i++ {
		_, _ = set.find("a-kid-that-does-not-exist", "RS256")
	}
	if k.count() != 1 {
		t.Errorf("%d fetches for twenty unknown key identifiers inside the interval", k.count())
	}

	// After the interval, one more is allowed — and only one, however
	// many unknown identifiers arrive.
	clock = clock.Add(2 * MinRefreshInterval)
	for i := 0; i < 20; i++ {
		_, _ = set.find("still-not-there", "RS256")
	}
	if k.count() != 2 {
		t.Errorf("%d fetches after the interval passed", k.count())
	}
}

// A provider that says five seconds would have this service fetch on
// every login; one that says a year would keep a withdrawn key valid
// here long after it was withdrawn.
func TestTheCacheWindowIsBoundedAtBothEnds(t *testing.T) {
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"max-age=5", 5 * time.Minute},
		{"max-age=1800", 30 * time.Minute},
		{"max-age=99999999", 24 * time.Hour},
		{"no-store", time.Hour},
		{"", time.Hour},
		{"public, max-age=600", 10 * time.Minute},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.header != "" {
			h.Set("Cache-Control", c.header)
		}
		if got := cacheFor(h); got != c.want {
			t.Errorf("%q gave %s, want %s", c.header, got, c.want)
		}
	}
}

// The keys that decide who an operator is do not travel over plaintext.
func TestAPlaintextKeySetURLIsRefused(t *testing.T) {
	set := &KeySet{URL: "http://idp.example/keys"}
	err := set.Refresh(context.Background())
	if err == nil {
		t.Fatal("an http key set URL was accepted")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

// A provider that adds a key type this build does not verify against
// should not lock every operator out — but a set with nothing usable is
// an error rather than an empty set that refuses every token obscurely.
func TestAnUnreadableKeyIsSkippedAndAnEmptySetIsAnError(t *testing.T) {
	mixed := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []map[string]any{
			{"kty": "OKP", "kid": "ed25519", "crv": "Ed25519", "x": "abc"},
			{"kty": "RSA", "kid": "k1", "use": "sig", "n": "bad-base64!!", "e": "AQAB"},
		}})
	}))
	defer mixed.Close()
	set := &KeySet{URL: mixed.URL, Client: mixed.Client()}
	err := set.Refresh(context.Background())
	if err == nil {
		t.Fatal("a set with no usable key was accepted")
	}
	if !strings.Contains(err.Error(), "no usable signing key") {
		t.Errorf("the error is %v", err)
	}
}

// A key published for encryption is not one to verify a signature with,
// and a provider publishes both.
func TestAnEncryptionKeyIsNotUsedForSignatures(t *testing.T) {
	s := newRSASigner(t, "enc")
	pub := &s.rsaKey.PublicKey
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"keys": []jsonWebKey{{
			Kty: "RSA", Kid: "enc", Use: "enc",
			N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	}))
	defer server.Close()

	set := &KeySet{URL: server.URL, Client: server.Client()}
	if err := set.Refresh(context.Background()); err == nil {
		t.Error("a set holding only an encryption key was accepted for signing")
	}
}

// Below 2048 bits a signature is forgeable with commodity hardware.
func TestASmallRSAKeyIsRefused(t *testing.T) {
	small := jsonWebKey{
		Kty: "RSA", Kid: "k1",
		N: base64.RawURLEncoding.EncodeToString(big.NewInt(0).SetBytes(make([]byte, 128)).Bytes()),
		E: base64.RawURLEncoding.EncodeToString(big.NewInt(65537).Bytes()),
	}
	if _, err := small.publicKey(); err == nil {
		t.Error("a 1024-bit key was accepted")
	}
}

// A point that is not on the curve is not a key, and accepting one is
// how invalid-curve attacks start.
func TestAnECPointOffTheCurveIsRefused(t *testing.T) {
	bad := jsonWebKey{
		Kty: "EC", Kid: "k1", Crv: "P-256",
		X: base64.RawURLEncoding.EncodeToString([]byte{1}),
		Y: base64.RawURLEncoding.EncodeToString([]byte{2}),
	}
	if _, err := bad.publicKey(); err == nil {
		t.Error("a point off the curve was accepted as a key")
	}
}

// A key the provider has withdrawn stops verifying once the set's cache
// window has passed. Nothing refreshed a set on its schedule: `Refresh`
// had no caller, so the only fetch after the first was the one an
// unknown `kid` causes, and a withdrawn key, which no token names as
// unknown, stayed trusted until the process restarted -- the case a
// provider withdraws a key for. DIVERGENCE 5.474.
func TestAWithdrawnKeyIsRefusedOnceTheSetExpires(t *testing.T) {
	k := newKeyServer(t, "k1")
	clock := time.Now()
	set := keySetFor(k, func() time.Time { return clock })
	opts := VerifyOptions{Issuer: "https://idp.example", Audience: "halite", Now: func() time.Time { return clock }}

	withdrawn := k.signer
	if _, err := Verify(withdrawn.token(t, "RS256", goodClaims(clock.Add(3*time.Hour))), set, opts); err != nil {
		t.Fatalf("a published key was refused: %v", err)
	}
	k.rotate(t, "k2")

	// Inside the window the set is not asked again: the cache is the
	// provider's own word on how long its keys stand.
	clock = clock.Add(30 * time.Minute)
	if _, err := Verify(withdrawn.token(t, "RS256", goodClaims(clock.Add(time.Hour))), set, opts); err != nil {
		t.Fatalf("inside the cache window the set changed: %v", err)
	}
	clock = clock.Add(time.Hour)
	if _, err := Verify(withdrawn.token(t, "RS256", goodClaims(clock.Add(time.Hour))), set, opts); err == nil {
		t.Fatal("a key the provider withdrew still verified after its set expired")
	}
	if _, err := Verify(k.signer.token(t, "RS256", goodClaims(clock.Add(time.Hour))), set, opts); err != nil {
		t.Errorf("the key that replaced it was refused: %v", err)
	}
}

// A provider that is down when the set expires leaves the keys it last
// published in use, and is asked again no more than once per
// MinRefreshInterval: logins do not stop because the provider is
// briefly unreachable, and do not each become a request to it.
func TestAnExpiredSetIsKeptWhileTheProviderIsDown(t *testing.T) {
	k := newKeyServer(t, "k1")
	clock := time.Now()
	set := keySetFor(k, func() time.Time { return clock })
	opts := VerifyOptions{Issuer: "https://idp.example", Audience: "halite", Now: func() time.Time { return clock }}
	if err := set.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	k.mu.Lock()
	k.status = http.StatusServiceUnavailable
	k.mu.Unlock()
	clock = clock.Add(2 * time.Hour)
	for i := 0; i < 5; i++ {
		if _, err := Verify(k.signer.token(t, "RS256", goodClaims(clock.Add(time.Hour))), set, opts); err != nil {
			t.Fatalf("an unreachable provider stopped logins with keys already held: %v", err)
		}
	}
	if n := k.count(); n != 2 {
		t.Errorf("%d fetches; want the first and one retry", n)
	}
}

// A token that names a key while the set is being fetched waits for the
// fetch. It used to be refused at once ("fetched less than a minute
// ago"), so during a rotation every login but the first failed until
// the fetch that would have let them in had finished. DIVERGENCE 5.474.
func TestATokenArrivingMidFetchWaitsForIt(t *testing.T) {
	k := newKeyServer(t, "k1")
	clock := time.Now()
	set := keySetFor(k, func() time.Time { return clock })
	opts := VerifyOptions{Issuer: "https://idp.example", Audience: "halite", Now: func() time.Time { return clock }}
	if err := set.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	k.rotate(t, "k2")
	clock = clock.Add(2 * MinRefreshInterval)
	k.mu.Lock()
	k.gate = make(chan struct{})
	gate := k.gate
	k.mu.Unlock()

	const logins = 10
	errs := make(chan error, logins)
	for i := 0; i < logins; i++ {
		go func() {
			_, err := Verify(k.signer.token(t, "RS256", goodClaims(clock.Add(time.Hour))), set, opts)
			errs <- err
		}()
	}
	for k.count() < 2 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	for i := 0; i < logins; i++ {
		if err := <-errs; err != nil {
			t.Errorf("a login during the fetch failed: %v", err)
		}
	}
	if n := k.count(); n != 2 {
		t.Errorf("%d fetches; want one for the rotation", n)
	}
}
