package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// MaxDiscoveryBody bounds the discovery document.
const MaxDiscoveryBody = 1 << 20

// DiscoveryRetryInterval is how long a failed discovery is remembered
// before the provider is asked again.
//
// Discovery is fetched on first use and kept once it succeeds, but a
// failure was kept for nothing: every `POST /v1/login/oidc`, which is
// unauthenticated, made a fresh request to a provider that was down, so
// anybody could aim a stream of requests through this service at an
// identity provider that was already struggling. MinRefreshInterval is
// the same kind of bound on the key set. This one is shorter, because
// while it holds nobody can log in by OIDC even once the provider is
// back.
const DiscoveryRetryInterval = 10 * time.Second

// Discovery is the part of the provider's metadata this build uses.
type Discovery struct {
	Issuer        string `json:"issuer"`
	AuthEndpoint  string `json:"authorization_endpoint"`
	TokenEndpoint string `json:"token_endpoint"`
	JWKSURI       string `json:"jwks_uri"`
}

// Config is how an estate names its provider.
type Config struct {
	// Issuer is the provider's base URL. Discovery is fetched from
	// `<issuer>/.well-known/openid-configuration`.
	Issuer string
	// ClientID and ClientSecret identify this service to the provider.
	ClientID     string
	ClientSecret string
	// Audience the tokens must carry. Empty takes ClientID, which is
	// what a provider issues by default.
	Audience string
	// RedirectURL is where the provider sends the operator back to.
	RedirectURL string
	// Scopes to request. `openid` is always included.
	Scopes []string
	// GroupsClaim is the colon-delimited path to the claim holding the
	// operator's groups. SPEC 23.4 lets an estate name it because
	// providers disagree: `groups`, `roles`, and
	// `resource_access:halite:roles` are all real.
	GroupsClaim string
	// RoleMap maps a group from the provider to roles in the RBAC
	// policy. A group with no entry grants nothing.
	RoleMap map[string][]string
	// PrincipalClaim is which claim names the operator. `sub` is
	// stable and opaque; `email` and `preferred_username` are readable
	// and can be reassigned, which is why `sub` is the default.
	PrincipalClaim string
	// Skew tolerated on `exp` and `nbf`.
	Skew time.Duration

	// CAFile verifies the provider's certificate against an estate's
	// own CA. An identity provider behind an internal CA is the common
	// case, and without this the only ways to reach one are a public
	// certificate or skipping verification — and skipping it on the
	// service that decides who an operator is would be the worst place
	// in this program to do it.
	CAFile string
	// Client is how the provider is reached. Nil takes one with a
	// timeout, and with CAFile when it is set.
	Client *http.Client
	// Now is the clock, for the tests.
	Now func() time.Time
}

// Provider is a configured OIDC issuer.
type Provider struct {
	cfg   Config
	keys  *KeySet
	built *http.Client

	mu        sync.Mutex
	discovery Discovery
	fetched   bool
	// failed and failedAt are the last discovery failure, held for
	// DiscoveryRetryInterval.
	failed   error
	failedAt time.Time
}

// New builds a provider. Discovery happens on first use rather than
// here, so that a provider being briefly unreachable does not stop the
// service starting — an estate with local accounts as well should not
// lose those too.
func New(cfg Config) (*Provider, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("an OIDC provider needs an issuer")
	}
	if !strings.HasPrefix(cfg.Issuer, "https://") {
		return nil, fmt.Errorf("an OIDC issuer must be https://; %q is not", cfg.Issuer)
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("an OIDC provider needs a client id")
	}
	if cfg.PrincipalClaim == "" {
		cfg.PrincipalClaim = "sub"
	}
	if cfg.Audience == "" {
		cfg.Audience = cfg.ClientID
	}
	client := &http.Client{Timeout: 15 * time.Second}
	if cfg.CAFile != "" {
		pool, err := certPool(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		}
	}
	if cfg.Client != nil {
		client = cfg.Client
	}
	return &Provider{cfg: cfg, built: httpsOnly(client)}, nil
}

// httpsOnly is a copy of a client that refuses to follow a redirect to
// anything but https.
//
// Each URL this package fetches was checked for https before it was
// asked, and only that first URL: the standard library's client follows
// a 302 from https to http without a word. So a key set at an https
// address that redirected to a plaintext one was fetched in plaintext,
// and the keys that decide who an operator is came from whoever was on
// the path. The token endpoint is worse: a 307 keeps the method and the
// body.
//
// A copy, because the client may be one the caller passed in, and
// changing that one would change it for whatever else holds it.
func httpsOnly(c *http.Client) *http.Client {
	copied := *c
	next := c.CheckRedirect
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("%s redirected to %s, and this service only follows a redirect to https",
				via[len(via)-1].URL.Redacted(), req.URL.Redacted())
		}
		if next != nil {
			return next(req, via)
		}
		// The standard library's own limit, which setting a
		// CheckRedirect replaces.
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &copied
}

// requireHTTPS refuses a URL the discovery document names that is not
// https, for the reason the issuer itself must be: the key set says
// which tokens are genuine, the token endpoint is sent the client
// secret, and the authorization endpoint is where an operator types a
// password. The issuer was checked and these were not, so a document
// served over https could send all three somewhere in plaintext.
func requireHTTPS(endpoint, name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%s names %s %q, which is not an https URL", endpoint, name, raw)
	}
	return nil
}

// certPool reads a CA bundle.
func certPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s holds no certificate", path)
	}
	return pool, nil
}

func (p *Provider) client() *http.Client {
	return p.built
}

func (p *Provider) now() time.Time {
	if p.cfg.Now != nil {
		return p.cfg.Now()
	}
	return time.Now()
}

// discover fetches the provider's metadata once, and after a failure
// asks again no sooner than DiscoveryRetryInterval.
func (p *Provider) discover(ctx context.Context) (Discovery, error) {
	p.mu.Lock()
	if p.fetched {
		defer p.mu.Unlock()
		return p.discovery, nil
	}
	if p.failed != nil && p.now().Sub(p.failedAt) < DiscoveryRetryInterval {
		defer p.mu.Unlock()
		return Discovery{}, fmt.Errorf("%w (not asked again until %s has passed)",
			p.failed, DiscoveryRetryInterval)
	}
	p.mu.Unlock()

	doc, err := p.fetchDiscovery(ctx)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		// A caller that gave up is not the provider failing, and
		// holding one impatient client's cancellation against
		// everybody else would be an outage made of nothing.
		if ctx.Err() == nil {
			p.failed, p.failedAt = err, p.now()
		}
		return Discovery{}, err
	}
	p.discovery, p.fetched, p.failed = doc, true, nil
	p.keys = &KeySet{URL: doc.JWKSURI, Client: p.client(), Now: p.cfg.Now}
	return doc, nil
}

// fetchDiscovery asks the provider for its metadata and checks it.
func (p *Provider) fetchDiscovery(ctx context.Context) (Discovery, error) {
	endpoint := strings.TrimSuffix(p.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Discovery{}, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := p.client().Do(req)
	if err != nil {
		return Discovery{}, fmt.Errorf("reaching %s: %w", endpoint, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Discovery{}, fmt.Errorf("%s answered %d", endpoint, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxDiscoveryBody))
	if err != nil {
		return Discovery{}, err
	}
	var doc Discovery
	if err := json.Unmarshal(body, &doc); err != nil {
		return Discovery{}, fmt.Errorf("%s is not a discovery document: %w", endpoint, err)
	}
	// The document says who it is for, and it must be who we asked.
	// Without this a redirect to another provider's metadata would
	// quietly move which issuer this service trusts.
	if doc.Issuer != strings.TrimSuffix(p.cfg.Issuer, "/") && doc.Issuer != p.cfg.Issuer {
		return Discovery{}, fmt.Errorf("%s describes issuer %q, and this service is configured for %q",
			endpoint, doc.Issuer, p.cfg.Issuer)
	}
	if doc.JWKSURI == "" || doc.TokenEndpoint == "" {
		return Discovery{}, fmt.Errorf("%s names no jwks_uri or token_endpoint", endpoint)
	}
	if err := requireHTTPS(endpoint, "jwks_uri", doc.JWKSURI); err != nil {
		return Discovery{}, err
	}
	if err := requireHTTPS(endpoint, "token_endpoint", doc.TokenEndpoint); err != nil {
		return Discovery{}, err
	}
	// Optional: a provider used only for client credentials or for
	// presented tokens has no interactive login, and StartAuth refuses
	// its absence. A plaintext one is refused here.
	if doc.AuthEndpoint != "" {
		if err := requireHTTPS(endpoint, "authorization_endpoint", doc.AuthEndpoint); err != nil {
			return Discovery{}, err
		}
	}
	return doc, nil
}

// AuthRequest is one interactive login in progress.
//
// The verifier and the nonce are held by the service between the
// redirect out and the callback back, and neither ever reaches the
// browser in a form that would let it be replayed: the browser carries
// the challenge, which is a digest, and the state, which is a handle.
type AuthRequest struct {
	// URL is where to send the operator.
	URL string
	// State is the handle that ties the callback to this request.
	State string
	// Nonce binds the eventual token to this request.
	Nonce string
	// Verifier is the PKCE secret, kept here and sent only at the
	// token exchange.
	Verifier string
	// Created is when this began, so a stale request can be swept.
	Created time.Time
}

// StartAuth builds the authorization request of SPEC 23.4's interactive
// path: Authorization Code with PKCE.
//
// PKCE even though this service holds a client secret, because the
// secret alone does not stop an authorization code intercepted at the
// redirect from being exchanged by whoever intercepted it.
func (p *Provider) StartAuth(ctx context.Context) (*AuthRequest, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	if doc.AuthEndpoint == "" {
		return nil, fmt.Errorf("%s names no authorization_endpoint", p.cfg.Issuer)
	}
	if p.cfg.RedirectURL == "" {
		return nil, fmt.Errorf("an interactive login needs a redirect URL")
	}

	verifier, err := randomString(64)
	if err != nil {
		return nil, err
	}
	state, err := randomString(32)
	if err != nil {
		return nil, err
	}
	nonce, err := randomString(32)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(digest[:])

	query := url.Values{}
	query.Set("response_type", "code")
	query.Set("client_id", p.cfg.ClientID)
	query.Set("redirect_uri", p.cfg.RedirectURL)
	query.Set("scope", strings.Join(p.scopes(), " "))
	query.Set("state", state)
	query.Set("nonce", nonce)
	query.Set("code_challenge", challenge)
	// S256 only. `plain` sends the verifier itself, which is the thing
	// PKCE exists to keep off the wire.
	query.Set("code_challenge_method", "S256")

	return &AuthRequest{
		URL:      doc.AuthEndpoint + "?" + query.Encode(),
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		Created:  p.now(),
	}, nil
}

func (p *Provider) scopes() []string {
	scopes := []string{"openid"}
	for _, s := range p.cfg.Scopes {
		if s != "openid" {
			scopes = append(scopes, s)
		}
	}
	return scopes
}

// tokenResponse is what a token endpoint answers with.
type tokenResponse struct {
	IDToken     string `json:"id_token"`
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

// Exchange trades an authorization code for a verified identity.
func (p *Provider) Exchange(ctx context.Context, code string, req *AuthRequest) (*Identity, error) {
	if req == nil {
		return nil, fmt.Errorf("there is no login in progress for this callback")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", p.cfg.RedirectURL)
	form.Set("client_id", p.cfg.ClientID)
	form.Set("code_verifier", req.Verifier)

	res, err := p.postToken(ctx, form)
	if err != nil {
		return nil, err
	}
	if res.IDToken == "" {
		return nil, fmt.Errorf("the provider returned no id_token")
	}
	return p.identityFrom(ctx, res.IDToken, req.Nonce)
}

// ClientCredentials is SPEC 23.4's automation path.
func (p *Provider) ClientCredentials(ctx context.Context) (*Identity, error) {
	if p.cfg.ClientSecret == "" {
		return nil, fmt.Errorf("client credentials need a client secret")
	}
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("scope", strings.Join(p.scopes(), " "))
	if p.cfg.Audience != "" {
		form.Set("audience", p.cfg.Audience)
	}
	res, err := p.postToken(ctx, form)
	if err != nil {
		return nil, err
	}
	token := res.IDToken
	if token == "" {
		// There is no browser and so no ID token; the access token is
		// the assertion, and it is verified the same way.
		token = res.AccessToken
	}
	if token == "" {
		return nil, fmt.Errorf("the provider returned no token")
	}
	// No nonce: there was no browser round trip to bind.
	return p.identityFrom(ctx, token, "")
}

// VerifyToken checks a token an operator presents directly, which is how
// a CI job that already holds one authenticates.
func (p *Provider) VerifyToken(ctx context.Context, token string) (*Identity, error) {
	return p.identityFrom(ctx, token, "")
}

func (p *Provider) postToken(ctx context.Context, form url.Values) (*tokenResponse, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if p.cfg.ClientSecret != "" {
		// In the header rather than the body: RFC 6749 prefers it, and
		// a secret in a form field is a secret in more logs.
		req.SetBasicAuth(url.QueryEscape(p.cfg.ClientID), url.QueryEscape(p.cfg.ClientSecret))
	}

	res, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching %s: %w", doc.TokenEndpoint, err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, MaxDiscoveryBody))
	if err != nil {
		return nil, err
	}
	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%s answered %d with something that is not a token response",
			doc.TokenEndpoint, res.StatusCode)
	}
	if parsed.Error != "" {
		// The provider's own words, which is what an operator needs;
		// this service cannot diagnose a misconfigured client for them.
		return nil, fmt.Errorf("the provider refused: %s%s", parsed.Error, describeError(parsed.Description))
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d", doc.TokenEndpoint, res.StatusCode)
	}
	return &parsed, nil
}

func describeError(description string) string {
	if description == "" {
		return ""
	}
	return " (" + description + ")"
}

// Identity is a verified operator.
type Identity struct {
	// Principal is `oidc:<claim value>`, the form the RBAC grammar
	// already accepts.
	Principal string
	// Roles come from the group claim through the mapping table.
	Roles []string
	// Groups is what the provider actually said, for the log: an
	// operator whose groups map to no roles needs to see which groups
	// they had.
	Groups []string
	// Claims is the whole verified token.
	Claims Claims
	// Expiry is the token's, which bounds the session this service
	// issues for it.
	Expiry time.Time
}

func (p *Provider) identityFrom(ctx context.Context, token, nonce string) (*Identity, error) {
	if _, err := p.discover(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	keys := p.keys
	p.mu.Unlock()

	claims, err := Verify(token, keys, VerifyOptions{
		Issuer:   p.cfg.Issuer,
		Audience: p.cfg.Audience,
		Nonce:    nonce,
		Skew:     p.cfg.Skew,
		Now:      p.cfg.Now,
	})
	if err != nil {
		return nil, err
	}

	name := claimString(claims.All, p.cfg.PrincipalClaim)
	if name == "" {
		return nil, fmt.Errorf("the token carries no %q claim to identify the operator by",
			p.cfg.PrincipalClaim)
	}
	groups := ClaimStrings(claims.All, p.cfg.GroupsClaim)
	identity := &Identity{
		Principal: "oidc:" + name,
		Roles:     MapRoles(groups, p.cfg.RoleMap),
		Groups:    groups,
		Claims:    claims,
	}
	if claims.Expiry != nil {
		identity.Expiry = time.Unix(*claims.Expiry, 0)
	}
	return identity, nil
}

// randomString is n bytes from crypto/rand, base64url encoded.
func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
