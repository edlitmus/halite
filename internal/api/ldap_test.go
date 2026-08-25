package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/ldap"
)

// ldapLab is an API with a directory behind it.
//
// The directory is `internal/ldap`'s own test server, which speaks the
// protocol, so this exercises the whole path an operator takes: a
// password posted to `/v1/login`, a bind against a directory, groups
// resolved, roles mapped, and a token issued.
func ldapLab(t *testing.T, adjust func(*ldap.Config)) *lab {
	t.Helper()
	l, _ := executeLab(t, `
roles:
  operator:
    - target: '*'
      functions: ['*']
      runners: ['*']
bindings:
  - principal: 'ldap:ed'
    roles: ['operator']
`)
	dir, err := ldap.NewTestDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dir.Close)
	cfg := ldap.Config{
		Address: dir.Address(), TLS: ldap.TLSLDAPS, CAFile: dir.CAFile(),
		ServerName:        "127.0.0.1",
		BindDN:            "cn=halite,ou=services,dc=example,dc=com",
		BindPassword:      "service-pw",
		UserBaseDN:        "ou=people,dc=example,dc=com",
		UserFilter:        "(uid=%s)",
		MemberOfAttribute: "memberOf",
		RoleMap:           map[string][]string{"platform": {"operator"}},
		Timeout:           5 * time.Second,
	}
	if adjust != nil {
		adjust(&cfg)
	}
	client, err := ldap.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	l.server.LDAP = client
	return l
}

func TestAnLDAPLoginIssuesATokenThatWorks(t *testing.T) {
	l := ldapLab(t, nil)

	res, body := l.post(t, PathLogin,
		`{"username":"ed","password":"hunter2","eauth":"ldap"}`, "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an LDAP login answered %d: %s", res.StatusCode, body)
	}
	var login LoginResponse
	if err := json.Unmarshal([]byte(body), &login); err != nil {
		t.Fatal(err)
	}
	if login.Principal != "ldap:ed" {
		t.Errorf("the principal is %q", login.Principal)
	}
	if len(login.Roles) != 1 || login.Roles[0] != "operator" {
		t.Errorf("the roles are %v", login.Roles)
	}

	res, body = l.get(t, PathJobs, login.Token)
	if res.StatusCode != http.StatusOK {
		t.Errorf("the issued token was refused: %d %s", res.StatusCode, body)
	}
}

// One message for every failure, as the local path gives. The
// difference between "no such user", "wrong password", and "the
// directory is down" goes to the log.
func TestEveryLDAPFailureGivesOneMessage(t *testing.T) {
	l := ldapLab(t, nil)

	cases := []string{
		`{"username":"ed","password":"wrong","eauth":"ldap"}`,
		`{"username":"nobody","password":"hunter2","eauth":"ldap"}`,
		`{"username":"ed","password":"","eauth":"ldap"}`,
	}
	seen := map[string]bool{}
	for _, request := range cases {
		res, body := l.post(t, PathLogin, request, "")
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s answered %d: %s", request, res.StatusCode, body)
		}
		seen[body] = true
	}
	if len(seen) != 1 {
		t.Errorf("three failures gave %d different answers: %v", len(seen), seen)
	}
}

// The directory is not this estate's authorization model.
func TestAnLDAPOperatorWithNoMappedGroupIsToldWhichGroupsTheyHave(t *testing.T) {
	l := ldapLab(t, func(cfg *ldap.Config) {
		cfg.RoleMap = map[string][]string{"some-other-group": {"operator"}}
	})

	res, body := l.post(t, PathLogin,
		`{"username":"ed","password":"hunter2","eauth":"ldap"}`, "")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("an unmapped operator answered %d: %s", res.StatusCode, body)
	}
	for _, want := range []string{"platform", "mapped"} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer does not mention %q: %s", want, body)
		}
	}
}

func TestLDAPAttemptsAreCounted(t *testing.T) {
	l := ldapLab(t, nil)
	l.post(t, PathLogin, `{"username":"ed","password":"hunter2","eauth":"ldap"}`, "")
	l.post(t, PathLogin, `{"username":"ed","password":"wrong","eauth":"ldap"}`, "")

	token := l.login(t, "ed", "hunter2").Token
	_, body := l.get(t, PathMetrics, token)
	for _, want := range []string{
		`halite_auth_attempts_total{method="ldap",result="accepted"} 1`,
		`halite_auth_attempts_total{method="ldap",result="refused"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the exposition is missing %q:\n%s", want, section(body, "auth_attempts"))
		}
	}
}
