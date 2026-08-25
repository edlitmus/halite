package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeIssuer is a provider: discovery, a key set, and a token endpoint.
type fakeIssuer struct {
	server *httptest.Server
	signer *signer
	// exchanged records what the token endpoint was sent.
	exchanged url.Values
	// nonce is what the next minted token will carry.
	nonce string
	// audience overrides what the token is minted for.
	audience string
	// issuerOverride makes the discovery document describe someone
	// else, which is the redirect-to-another-provider case.
	issuerOverride string
	t              *testing.T
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	f := &fakeIssuer{signer: newRSASigner(t, "k1"), t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := f.server.URL
		if f.issuerOverride != "" {
			issuer = f.issuerOverride
		}
		writeJSON(w, Discovery{
			Issuer:        issuer,
			AuthEndpoint:  f.server.URL + "/authorize",
			TokenEndpoint: f.server.URL + "/token",
			JWKSURI:       f.server.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=3600")
		pub := &f.signer.rsaKey.PublicKey
		writeJSON(w, map[string]any{"keys": []jsonWebKey{{
			Kty: "RSA", Kid: "k1", Use: "sig", Alg: "RS256",
			N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.exchanged = r.Form
		audience := f.audience
		if audience == "" {
			audience = "halite"
		}
		claims := map[string]any{
			"iss": f.server.URL, "sub": "ed", "aud": audience,
			"exp":    time.Now().Add(time.Hour).Unix(),
			"groups": []string{"platform", "oncall"},
			"email":  "ed@example.com",
		}
		if f.nonce != "" {
			claims["nonce"] = f.nonce
		}
		writeJSON(w, tokenResponse{
			IDToken: f.signer.token(f.t, "RS256", claims), TokenType: "Bearer",
		})
	})
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// providerFor builds a provider pointed at the fake issuer, verifying
// its certificate as a real one would rather than skipping.
func providerFor(t *testing.T, f *fakeIssuer, adjust func(*Config)) *Provider {
	t.Helper()
	cfg := Config{
		Issuer:       f.server.URL,
		ClientID:     "halite",
		ClientSecret: "s3cret",
		Audience:     "halite",
		RedirectURL:  "https://api.example/v1/login/callback",
		GroupsClaim:  "groups",
		RoleMap:      map[string][]string{"platform": {"operator"}, "oncall": {"responder"}},
		Client:       f.server.Client(),
	}
	if adjust != nil {
		adjust(&cfg)
	}
	// The fake issuer is https, but on 127.0.0.1 rather than a name.
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheInteractiveFlowUsesPKCEWithS256(t *testing.T) {
	f := newFakeIssuer(t)
	p := providerFor(t, f, nil)

	req, err := p.StartAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(req.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("the challenge method is %q; plain sends the verifier itself",
			q.Get("code_challenge_method"))
	}
	// The browser carries the digest, never the verifier.
	digest := sha256.Sum256([]byte(req.Verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Error("the challenge is not the digest of the verifier")
	}
	if strings.Contains(req.URL, req.Verifier) {
		t.Error("the verifier is in the URL the browser follows")
	}
	if q.Get("state") == "" || q.Get("nonce") == "" {
		t.Error("the request carries no state or nonce")
	}
	if q.Get("response_type") != "code" {
		t.Errorf("the response type is %q", q.Get("response_type"))
	}
}

func TestExchangeVerifiesAndMapsRoles(t *testing.T) {
	f := newFakeIssuer(t)
	p := providerFor(t, f, nil)

	req, err := p.StartAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.nonce = req.Nonce

	identity, err := p.Exchange(context.Background(), "the-code", req)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Principal != "oidc:ed" {
		t.Errorf("the principal is %q", identity.Principal)
	}
	if len(identity.Roles) != 2 || identity.Roles[0] != "operator" || identity.Roles[1] != "responder" {
		t.Errorf("the roles are %v", identity.Roles)
	}
	// The verifier goes to the token endpoint and nowhere else.
	if f.exchanged.Get("code_verifier") != req.Verifier {
		t.Error("the verifier did not reach the token exchange")
	}
	if f.exchanged.Get("grant_type") != "authorization_code" {
		t.Errorf("the grant type is %q", f.exchanged.Get("grant_type"))
	}
}

// The nonce binds the token to the login that asked for it. A token
// minted for a different login is a replayed authorization response.
func TestATokenForAnotherLoginIsRefused(t *testing.T) {
	f := newFakeIssuer(t)
	p := providerFor(t, f, nil)

	req, err := p.StartAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.nonce = "some-other-login"

	if _, err := p.Exchange(context.Background(), "the-code", req); err == nil {
		t.Fatal("a token minted for another login was accepted")
	}
}

func TestClientCredentialsNeedsNoNonce(t *testing.T) {
	f := newFakeIssuer(t)
	p := providerFor(t, f, nil)

	identity, err := p.ClientCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if identity.Principal != "oidc:ed" {
		t.Errorf("the principal is %q", identity.Principal)
	}
	if f.exchanged.Get("grant_type") != "client_credentials" {
		t.Errorf("the grant type is %q", f.exchanged.Get("grant_type"))
	}
}

// A discovery document that describes another issuer would quietly move
// which provider this service trusts.
func TestADiscoveryDocumentForAnotherIssuerIsRefused(t *testing.T) {
	f := newFakeIssuer(t)
	f.issuerOverride = "https://evil.example"
	p := providerFor(t, f, nil)

	_, err := p.ClientCredentials(context.Background())
	if err == nil {
		t.Fatal("a document describing another issuer was accepted")
	}
	if !strings.Contains(err.Error(), "evil.example") {
		t.Errorf("the refusal does not name what it found: %v", err)
	}
}

// A token minted for somebody else's audience is somebody else's token.
func TestATokenForAnotherAudienceIsRefused(t *testing.T) {
	f := newFakeIssuer(t)
	f.audience = "another-service"
	p := providerFor(t, f, nil)

	if _, err := p.ClientCredentials(context.Background()); err == nil {
		t.Fatal("a token for another audience was accepted")
	}
}

// The provider's directory is not this estate's authorization model.
func TestAnUnmappedGroupGrantsNothing(t *testing.T) {
	f := newFakeIssuer(t)
	p := providerFor(t, f, func(c *Config) {
		c.RoleMap = map[string][]string{"some-other-group": {"administrator"}}
	})

	identity, err := p.ClientCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(identity.Roles) != 0 {
		t.Errorf("unmapped groups granted %v", identity.Roles)
	}
	// And the operator can be told which groups they had, which is
	// actionable where "access denied" is not.
	if len(identity.Groups) != 2 {
		t.Errorf("the groups were not recorded: %v", identity.Groups)
	}
	if unmapped := UnmappedGroups(identity.Groups, p.cfg.RoleMap); len(unmapped) != 2 {
		t.Errorf("the unmapped groups are %v", unmapped)
	}
}

// The issuer decides who an operator is, so it does not travel over
// plaintext.
func TestAPlaintextIssuerIsRefused(t *testing.T) {
	if _, err := New(Config{Issuer: "http://idp.example", ClientID: "halite"}); err == nil {
		t.Fatal("an http issuer was accepted")
	}
}

func TestTheGroupsClaimPathIsConfigurable(t *testing.T) {
	claims := map[string]any{
		"resource_access": map[string]any{
			"halite": map[string]any{"roles": []any{"platform", "oncall"}},
		},
		"groups": "single-group",
	}
	nested := ClaimStrings(claims, "resource_access:halite:roles")
	if len(nested) != 2 || nested[0] != "platform" {
		t.Errorf("a nested path read %v", nested)
	}
	// A provider with one group per operator often sends a string.
	if one := ClaimStrings(claims, "groups"); len(one) != 1 || one[0] != "single-group" {
		t.Errorf("a string claim read %v", one)
	}
	if missing := ClaimStrings(claims, "nope:nowhere"); len(missing) != 0 {
		t.Errorf("a missing path read %v", missing)
	}
}

// Two groups mapping to the same role produce one, and the order the
// provider used does not change what the token records.
func TestRoleMappingIsASortedSet(t *testing.T) {
	table := map[string][]string{
		"platform": {"operator", "reader"},
		"oncall":   {"operator"},
	}
	forward := MapRoles([]string{"platform", "oncall"}, table)
	backward := MapRoles([]string{"oncall", "platform"}, table)
	if len(forward) != 2 || forward[0] != "operator" || forward[1] != "reader" {
		t.Errorf("the roles are %v", forward)
	}
	if strings.Join(forward, ",") != strings.Join(backward, ",") {
		t.Errorf("order changed the result: %v vs %v", forward, backward)
	}
}
