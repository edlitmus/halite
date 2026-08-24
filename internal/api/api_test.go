package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/account"
	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/policy"
)

// lab is an API server with a local account and a token store.
type lab struct {
	server *Server
	http   *httptest.Server
}

func newLab(t *testing.T) *lab {
	t.Helper()
	hash, err := account.Hash("hunter2", account.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := apitoken.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := policy.Load([]byte(`
roles:
  administrator:
    - target: '*'
      functions: ['*']
bindings:
  - principal: 'local:ed'
    roles: ['administrator']
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		Accounts: &account.File{Accounts: map[string]*account.Account{
			"ed":  {Name: "ed", Hash: hash, Roles: []string{"operator"}},
			"off": {Name: "off", Hash: hash, Disabled: true},
		}},
		Tokens:        tokens,
		Policy:        loaded,
		TokenLifetime: time.Hour,
		TokenIdle:     30 * time.Minute,
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &lab{server: s, http: ts}
}

func (l *lab) post(t *testing.T, path, body, token string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, l.http.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(t, req)
}

func (l *lab) get(t *testing.T, path, token string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, l.http.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) (*http.Response, string) {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res, b.String()
}

// login is the whole point of the endpoint, so it gets a helper.
func (l *lab) login(t *testing.T, user, password string) LoginResponse {
	t.Helper()
	res, body := l.post(t, PathLogin,
		`{"username":"`+user+`","password":"`+password+`"}`, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login answered %d: %s", res.StatusCode, body)
	}
	var out LoginResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLoginIssuesAUsableToken(t *testing.T) {
	l := newLab(t)
	out := l.login(t, "ed", "hunter2")
	if out.Token == "" || out.TokenID == "" {
		t.Fatalf("login returned %+v", out)
	}
	if out.Principal != "local:ed" {
		t.Errorf("the principal is %q", out.Principal)
	}
	// The roles are the account's and the policy's together: an estate
	// may put the binding in either place.
	roles := strings.Join(out.Roles, ",")
	if !strings.Contains(roles, "operator") || !strings.Contains(roles, "administrator") {
		t.Errorf("the roles are %v", out.Roles)
	}

	// And it works.
	res, body := l.get(t, PathToken, out.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("introspection answered %d: %s", res.StatusCode, body)
	}
	var info TokenInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatal(err)
	}
	if info.TokenID != out.TokenID || info.Principal != "local:ed" {
		t.Errorf("introspection says %+v", info)
	}
	if info.IdleExpires == "" {
		t.Error("introspection does not say when the token goes idle")
	}
}

// One message for every failure. Which of the three it was is in the
// log, not in the answer: the difference between "no such account" and
// "wrong password" is the difference between a guess and a confirmed
// name.
func TestEveryLoginFailureLooksTheSame(t *testing.T) {
	l := newLab(t)
	var messages []string
	for _, body := range []string{
		`{"username":"ed","password":"wrong"}`,
		`{"username":"nosuchperson","password":"hunter2"}`,
		`{"username":"off","password":"hunter2"}`,
		`{"username":"","password":""}`,
	} {
		res, out := l.post(t, PathLogin, body, "")
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s answered %d", body, res.StatusCode)
		}
		messages = append(messages, out)
	}
	for i := 1; i < len(messages); i++ {
		if messages[i] != messages[0] {
			t.Errorf("the answers differ:\n%s\n%s", messages[0], messages[i])
		}
	}
	// And none of them says the password.
	if strings.Contains(messages[0], "hunter2") {
		t.Error("the answer contains a password")
	}
}

// A disabled account keeps its record for the audit and authenticates
// nobody.
func TestADisabledAccountCannotLogIn(t *testing.T) {
	l := newLab(t)
	res, _ := l.post(t, PathLogin, `{"username":"off","password":"hunter2"}`, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a disabled account answered %d", res.StatusCode)
	}
}

// An account with a second factor needs one.
func TestASecondFactorIsRequiredWhenConfigured(t *testing.T) {
	l := newLab(t)
	hash, err := account.Hash("hunter2", account.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	l.server.Accounts.Accounts["mfa"] = &account.Account{
		Name: "mfa", Hash: hash, TOTP: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ",
	}

	res, _ := l.post(t, PathLogin, `{"username":"mfa","password":"hunter2"}`, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a login with no code answered %d", res.StatusCode)
	}
	res, _ = l.post(t, PathLogin, `{"username":"mfa","password":"hunter2","code":"000000"}`, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a login with a wrong code answered %d", res.StatusCode)
	}
}

// Logout revokes the token that was presented, and it stops working.
func TestLogoutRevokesTheToken(t *testing.T) {
	l := newLab(t)
	out := l.login(t, "ed", "hunter2")

	res, _ := l.post(t, PathLogout, "", out.Token)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout answered %d", res.StatusCode)
	}
	res, body := l.get(t, PathToken, out.Token)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a revoked token answered %d", res.StatusCode)
	}
	if !strings.Contains(body, "revoked") {
		t.Errorf("the refusal says %q", body)
	}
}

// No token, a wrong token, and a token in the wrong place are all
// refused. SPEC 23.6 keeps token material out of a URL, so a query
// parameter is not a way in.
func TestAuthenticationRefusesWhatItShould(t *testing.T) {
	l := newLab(t)
	out := l.login(t, "ed", "hunter2")

	res, _ := l.get(t, PathToken, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token answered %d", res.StatusCode)
	}
	res, _ = l.get(t, PathToken, "not-a-token")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong token answered %d", res.StatusCode)
	}
	res, _ = l.get(t, PathToken+"?token="+out.Token, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a token in the query string answered %d", res.StatusCode)
	}

	// A malformed Authorization header is not a bearer token.
	req, _ := http.NewRequest(http.MethodGet, l.http.URL+PathToken, nil)
	req.Header.Set("Authorization", out.Token)
	res, _ = do(t, req)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a header with no scheme answered %d", res.StatusCode)
	}
}

// Health says nothing about the estate, and readiness says nothing
// about which upstream is unreachable: a probe that maps the estate is
// a probe anyone who can reach it can use.
func TestHealthDisclosesNothing(t *testing.T) {
	l := newLab(t)
	res, body := l.get(t, PathHealthz, "")
	if res.StatusCode != http.StatusOK || strings.TrimSpace(body) != "ok" {
		t.Errorf("healthz answered %d %q", res.StatusCode, body)
	}

	// No hub, so it is not ready — and does not say what a hub is.
	res, body = l.get(t, PathReadyz, "")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz answered %d", res.StatusCode)
	}
	if strings.Contains(body, "hub") || strings.Contains(body, "http") {
		t.Errorf("readyz disclosed %q", body)
	}
}

// The hardening of SPEC 22.3 is on every response, including the ones
// that fail.
func TestTheSecurityHeadersAreOnEveryResponse(t *testing.T) {
	l := newLab(t)
	for _, path := range []string{PathHealthz, PathToken, "/v1/nosuchthing"} {
		res, _ := l.get(t, path, "")
		for header, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "no-referrer",
			"Cache-Control":          "no-store",
		} {
			if got := res.Header.Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", path, header, got, want)
			}
		}
		if res.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s has no content security policy", path)
		}
	}
}

// An unrouted path says so without listing what does exist.
func TestAnUnknownPathSaysSoWithoutAMap(t *testing.T) {
	l := newLab(t)
	res, body := l.get(t, "/v1/nosuchthing", "")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown path answered %d", res.StatusCode)
	}
	if strings.Contains(body, PathLogin) {
		t.Errorf("the answer lists the endpoints: %q", body)
	}
}

// A body larger than the limit is refused before it is parsed.
func TestABodyIsLimitedBeforeItIsParsed(t *testing.T) {
	l := newLab(t)
	l.server.MaxBody = 64
	res, _ := l.post(t, PathLogin,
		`{"username":"ed","password":"`+strings.Repeat("x", 4096)+`"}`, "")
	if res.StatusCode == http.StatusOK {
		t.Error("an oversized body was accepted")
	}
}

// A request body that carries a second value is refused: one request is
// one thing being asked for.
func TestASmuggledSecondValueIsRefused(t *testing.T) {
	l := newLab(t)
	res, _ := l.post(t, PathLogin,
		`{"username":"ed","password":"hunter2"}{"username":"root","password":"x"}`, "")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("a smuggled second value answered %d", res.StatusCode)
	}
}

// This build has one authentication backend and says so, rather than
// quietly authenticating an LDAP request against local accounts.
func TestAnUnknownAuthBackendIsRefusedByName(t *testing.T) {
	l := newLab(t)
	res, body := l.post(t, PathLogin,
		`{"username":"ed","password":"hunter2","eauth":"ldap"}`, "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown backend answered %d", res.StatusCode)
	}
	if !strings.Contains(body, "local") {
		t.Errorf("the refusal says %q", body)
	}
}
