package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every URL the discovery document names must be https, as the issuer
// must: the key set decides which tokens are genuine, the token endpoint
// is sent the client secret, and the authorization endpoint is where an
// operator types a password. Only the issuer was checked.
func TestADiscoveryDocumentNamingAPlaintextEndpointIsRefused(t *testing.T) {
	for _, name := range []string{"jwks_uri", "token_endpoint", "authorization_endpoint"} {
		t.Run(name, func(t *testing.T) {
			f := newFakeIssuer(t)
			f.adjustDiscovery = func(d *Discovery) {
				plain := strings.Replace(f.server.URL, "https://", "http://", 1)
				switch name {
				case "jwks_uri":
					d.JWKSURI = plain + "/keys"
				case "token_endpoint":
					d.TokenEndpoint = plain + "/token"
				case "authorization_endpoint":
					d.AuthEndpoint = plain + "/authorize"
				}
			}
			p := providerFor(t, f, nil)
			_, err := p.discover(context.Background())
			if err == nil {
				t.Fatalf("a discovery document naming a plaintext %s was accepted", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("the refusal does not say which URL: %v", err)
			}
		})
	}
}

// plainServer is an http listener that counts what reaches it, standing
// in for whoever is on the path when a fetch is downgraded.
func plainServer(t *testing.T) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var hits atomic.Int32
	var auth atomic.Value
	auth.Store("")
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		writeJSON(w, map[string]any{"keys": []any{}})
	}))
	t.Cleanup(s.Close)
	return s, &hits, &auth
}

// A key set at an https address that redirects to plaintext is not
// fetched from plaintext. fetchKeys checked the URL it was given and the
// client followed the redirect it was not.
func TestAKeySetRedirectToPlaintextIsNotFollowed(t *testing.T) {
	f := newFakeIssuer(t)
	plain, hits, _ := plainServer(t)
	f.redirects = map[string]string{"/keys-moved": plain.URL + "/keys"}
	f.adjustDiscovery = func(d *Discovery) { d.JWKSURI = f.server.URL + "/keys-moved" }
	p := providerFor(t, f, nil)

	_, err := p.ClientCredentials(context.Background())
	if err == nil {
		t.Fatal("a token was verified against keys fetched over a downgraded redirect")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the plaintext server was asked %d times", n)
	}
	if !strings.Contains(err.Error(), "only follows a redirect to https") {
		t.Errorf("the error does not say why: %v", err)
	}
}

// The token endpoint is sent the client secret in Basic auth. A 307
// keeps the method and the body, and the standard library forwards the
// Authorization header to a redirect on the same host, which a
// downgrade from https to http is.
func TestATokenEndpointRedirectToPlaintextIsNotFollowed(t *testing.T) {
	f := newFakeIssuer(t)
	plain, hits, auth := plainServer(t)
	f.redirects = map[string]string{"/token-moved": plain.URL + "/token"}
	f.adjustDiscovery = func(d *Discovery) { d.TokenEndpoint = f.server.URL + "/token-moved" }
	p := providerFor(t, f, nil)

	if _, err := p.ClientCredentials(context.Background()); err == nil {
		t.Fatal("a token exchange followed a redirect to plaintext and succeeded")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the plaintext server was asked %d times; it was sent Authorization %q",
			n, auth.Load())
	}
}

// A redirect that stays on https is still followed: the change refuses
// a downgrade, not a provider that moved its keys.
func TestAKeySetRedirectThatStaysOnHTTPSIsFollowed(t *testing.T) {
	f := newFakeIssuer(t)
	f.redirects = map[string]string{"/keys-moved": f.server.URL + "/keys"}
	f.adjustDiscovery = func(d *Discovery) { d.JWKSURI = f.server.URL + "/keys-moved" }
	p := providerFor(t, f, nil)

	if _, err := p.ClientCredentials(context.Background()); err != nil {
		t.Fatalf("an https-to-https redirect was refused: %v", err)
	}
}

// A provider that is down is asked once per DiscoveryRetryInterval, not
// once per login started. `POST /v1/login/oidc` is unauthenticated, so
// without this anybody could turn this service into a way of sending
// requests to an identity provider that is already failing.
func TestAFailedDiscoveryIsNotRepeatedAtOnce(t *testing.T) {
	f := newFakeIssuer(t)
	f.discoveryStatus = http.StatusServiceUnavailable
	now := time.Now()
	p := providerFor(t, f, func(c *Config) { c.Now = func() time.Time { return now } })

	for i := 0; i < 5; i++ {
		if _, err := p.StartAuth(context.Background()); err == nil {
			t.Fatal("a login started against a provider that is down")
		}
	}
	if n := f.discoveries.Load(); n != 1 {
		t.Fatalf("five logins asked the provider %d times; want once", n)
	}

	// Once the interval has passed, and the provider is back, it is
	// asked again and the login starts.
	f.discoveryStatus = 0
	now = now.Add(DiscoveryRetryInterval + time.Second)
	if _, err := p.StartAuth(context.Background()); err != nil {
		t.Fatalf("the provider came back and a login still did not start: %v", err)
	}
	if n := f.discoveries.Load(); n != 2 {
		t.Errorf("the provider was asked %d times; want twice", n)
	}
}

// A caller that gave up is not the provider failing, so a cancelled
// discovery does not hold everybody else out.
func TestACancelledDiscoveryIsNotHeldAgainstTheNext(t *testing.T) {
	f := newFakeIssuer(t)
	p := providerFor(t, f, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.StartAuth(ctx); err == nil {
		t.Fatal("a cancelled discovery succeeded")
	}
	if _, err := p.StartAuth(context.Background()); err != nil {
		t.Fatalf("a cancelled discovery held the next login out: %v", err)
	}
}
