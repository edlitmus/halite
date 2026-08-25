package api

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/oidc"
)

// fakeIDP is an identity provider: discovery, keys, and a token
// endpoint that mints what the test asks for.
type fakeIDP struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	// groups is what the next token will claim.
	groups []string
	// nonce is echoed into the token, so a test can mint one for
	// another login.
	nonce string
	// subject names the operator.
	subject string
	// expiry overrides the token's, for the session-bounding test.
	expiry time.Time
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{key: key, groups: []string{"platform"}, subject: "ed"}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeIDPJSON(w, map[string]any{
			"issuer":                 f.server.URL,
			"authorization_endpoint": f.server.URL + "/authorize",
			"token_endpoint":         f.server.URL + "/token",
			"jwks_uri":               f.server.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		pub := &f.key.PublicKey
		writeIDPJSON(w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "kid": "k1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		writeIDPJSON(w, map[string]any{"id_token": f.mint(t), "token_type": "Bearer"})
	})
	f.server = httptest.NewTLSServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

// mint signs an ID token with whatever the fake is currently set to say.
func (f *fakeIDP) mint(t *testing.T) string {
	t.Helper()
	expiry := f.expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(time.Hour)
	}
	claims := map[string]any{
		"iss": f.server.URL, "sub": f.subject, "aud": "halite",
		"exp": expiry.Unix(), "iat": time.Now().Unix(),
		"groups": f.groups,
	}
	if f.nonce != "" {
		claims["nonce"] = f.nonce
	}
	header := map[string]any{"alg": "RS256", "kid": "k1", "typ": "JWT"}
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signed := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func writeIDPJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// oidcLab is an API with a provider behind it.
func oidcLab(t *testing.T, roleMap map[string][]string) (*lab, *fakeIDP) {
	t.Helper()
	l, _ := executeLab(t, `
roles:
  operator:
    - target: '*'
      functions: ['*']
      runners: ['*']
bindings:
  - principal: 'oidc:ed'
    roles: ['operator']
`)
	idp := newFakeIDP(t)
	if roleMap == nil {
		roleMap = map[string][]string{"platform": {"operator"}}
	}
	provider, err := oidc.New(oidc.Config{
		Issuer:      idp.server.URL,
		ClientID:    "halite",
		Audience:    "halite",
		RedirectURL: "https://api.example/callback",
		GroupsClaim: "groups",
		RoleMap:     roleMap,
		Client:      idp.server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	l.server.OIDC = provider
	return l, idp
}

func TestAnInteractiveOIDCLoginIssuesAToken(t *testing.T) {
	l, idp := oidcLab(t, nil)

	res, body := l.post(t, PathLoginOIDC, `{}`, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("starting a login answered %d: %s", res.StatusCode, body)
	}
	var start AuthStartResponse
	if err := json.Unmarshal([]byte(body), &start); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(start.URL, "code_challenge_method=S256") {
		t.Errorf("the authorization URL is %s", start.URL)
	}

	// The provider mints a token for this login.
	idp.nonce = nonceFrom(t, start.URL)
	res, body = l.post(t, PathLoginOIDCBack,
		`{"code":"the-code","state":"`+start.State+`"}`, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the callback answered %d: %s", res.StatusCode, body)
	}
	var login LoginResponse
	if err := json.Unmarshal([]byte(body), &login); err != nil {
		t.Fatal(err)
	}
	if login.Principal != "oidc:ed" {
		t.Errorf("the principal is %q", login.Principal)
	}
	if len(login.Roles) != 1 || login.Roles[0] != "operator" {
		t.Errorf("the roles are %v", login.Roles)
	}

	// And the token works against the estate.
	res, body = l.get(t, PathJobs, login.Token)
	if res.StatusCode != http.StatusOK {
		t.Errorf("the issued token was refused: %d %s", res.StatusCode, body)
	}
}

// A state is good once, so an authorization response replayed a second
// time finds nothing waiting.
func TestAnAuthorizationResponseCannotBeReplayed(t *testing.T) {
	l, idp := oidcLab(t, nil)

	res, body := l.post(t, PathLoginOIDC, `{}`, "")
	var start AuthStartResponse
	_ = json.Unmarshal([]byte(body), &start)
	idp.nonce = nonceFrom(t, start.URL)

	callback := `{"code":"the-code","state":"` + start.State + `"}`
	if res, _ = l.post(t, PathLoginOIDCBack, callback, ""); res.StatusCode != http.StatusOK {
		t.Fatal("the first callback failed")
	}
	res, body = l.post(t, PathLoginOIDCBack, callback, "")
	if res.StatusCode == http.StatusOK {
		t.Fatalf("the same authorization response was accepted twice: %s", body)
	}
}

// An unknown state is somebody guessing, and the answer is the same one
// every other login failure gets.
func TestAnUnknownStateIsRefused(t *testing.T) {
	l, _ := oidcLab(t, nil)
	res, _ := l.post(t, PathLoginOIDCBack, `{"code":"c","state":"never-issued"}`, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unknown state answered %d", res.StatusCode)
	}
}

// A CI job has no browser: it presents a token it already holds.
func TestATokenFromTheProviderIsAccepted(t *testing.T) {
	l, idp := oidcLab(t, nil)
	token := idp.mint(t)

	res, body := l.post(t, PathLoginOIDCToken, `{"token":"`+token+`"}`, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a provider token answered %d: %s", res.StatusCode, body)
	}
	var login LoginResponse
	_ = json.Unmarshal([]byte(body), &login)
	if login.Principal != "oidc:ed" {
		t.Errorf("the principal is %q", login.Principal)
	}
}

// "You authenticated, and none of your groups is mapped" is actionable
// where "access denied" is not.
func TestAnOperatorWithNoMappedGroupIsToldWhichGroupsTheyHave(t *testing.T) {
	l, idp := oidcLab(t, map[string][]string{"some-other-group": {"operator"}})
	idp.groups = []string{"platform", "oncall"}
	token := idp.mint(t)

	res, body := l.post(t, PathLoginOIDCToken, `{"token":"`+token+`"}`, "")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("an unmapped operator answered %d: %s", res.StatusCode, body)
	}
	for _, want := range []string{"platform", "oncall", "mapped"} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer does not mention %q: %s", want, body)
		}
	}
}

// A provider that expires a token in ten minutes has said something
// about how long it trusts the operator, and this service does not
// extend it.
func TestASessionDoesNotOutliveTheAssertion(t *testing.T) {
	l, idp := oidcLab(t, nil)
	l.server.TokenLifetime = 12 * time.Hour
	idp.expiry = time.Now().Add(10 * time.Minute)

	res, body := l.post(t, PathLoginOIDCToken, `{"token":"`+idp.mint(t)+`"}`, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the login answered %d: %s", res.StatusCode, body)
	}
	var login LoginResponse
	_ = json.Unmarshal([]byte(body), &login)
	expires, err := time.Parse(time.RFC3339, login.Expires)
	if err != nil {
		t.Fatal(err)
	}
	if expires.After(time.Now().Add(30 * time.Minute)) {
		t.Errorf("the session expires at %s, past the assertion it was made on", login.Expires)
	}
}

// A service with no provider says so, rather than 500ing or accepting.
func TestOIDCEndpointsSayWhenThereIsNoProvider(t *testing.T) {
	l := newLab(t)
	for _, path := range []string{PathLoginOIDC, PathLoginOIDCBack, PathLoginOIDCToken} {
		res, body := l.post(t, path, `{}`, "")
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("%s answered %d: %s", path, res.StatusCode, body)
		}
	}
}

// `eauth: oidc` on the password endpoint names where to go instead of
// being quietly authenticated against local accounts.
func TestEauthOIDCIsSentToTheRightEndpoint(t *testing.T) {
	l := newLab(t)
	res, body := l.post(t, PathLogin, `{"username":"ed","password":"hunter2","eauth":"oidc"}`, "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("eauth: oidc answered %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(body, PathLoginOIDC) {
		t.Errorf("the answer does not say where to go: %s", body)
	}
	res, body = l.post(t, PathLogin, `{"username":"ed","password":"hunter2","eauth":"ldap"}`, "")
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "LDAP is not built") {
		t.Errorf("eauth: ldap answered %d: %s", res.StatusCode, body)
	}
}

func nonceFrom(t *testing.T, authURL string) string {
	t.Helper()
	_, query, ok := strings.Cut(authURL, "?")
	if !ok {
		t.Fatalf("no query in %s", authURL)
	}
	for _, pair := range strings.Split(query, "&") {
		if value, ok := strings.CutPrefix(pair, "nonce="); ok {
			return value
		}
	}
	t.Fatalf("no nonce in %s", authURL)
	return ""
}
