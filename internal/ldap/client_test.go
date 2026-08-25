package ldap

import (
	"strings"
	"testing"
	"time"
)

// directoryWith sets up a fake holding a service account, an operator,
// and two groups.
func directoryWith(t *testing.T) *fakeDirectory {
	t.Helper()
	d := newFakeDirectory(t)
	d.user("cn=halite,ou=services,dc=example,dc=com", "service-pw")
	d.user("uid=ed,ou=people,dc=example,dc=com", "hunter2")
	d.entry(Entry{
		DN: "uid=ed,ou=people,dc=example,dc=com",
		Attributes: map[string][]string{
			"uid":  {"ed"},
			"mail": {"ed@example.com"},
			"memberOf": {
				"cn=platform,ou=groups,dc=example,dc=com",
				"cn=oncall,ou=groups,dc=example,dc=com",
			},
		},
	})
	return d
}

func clientFor(t *testing.T, d *fakeDirectory, adjust func(*Config)) *Client {
	t.Helper()
	cfg := Config{
		Address: d.address(), TLS: TLSLDAPS, CAFile: caFile(t),
		ServerName:        "127.0.0.1",
		BindDN:            "cn=halite,ou=services,dc=example,dc=com",
		BindPassword:      "service-pw",
		UserBaseDN:        "ou=people,dc=example,dc=com",
		UserFilter:        "(uid=%s)",
		MemberOfAttribute: "memberOf",
		RoleMap:           map[string][]string{"platform": {"operator"}, "oncall": {"responder"}},
		Timeout:           5 * time.Second,
	}
	if adjust != nil {
		adjust(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAnOperatorAuthenticatesAndTheirGroupsBecomeRoles(t *testing.T) {
	d := directoryWith(t)
	c := clientFor(t, d, nil)

	identity, err := c.Authenticate("ed", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Principal != "ldap:ed" {
		t.Errorf("the principal is %q", identity.Principal)
	}
	if identity.DN != "uid=ed,ou=people,dc=example,dc=com" {
		t.Errorf("the DN is %q", identity.DN)
	}
	if len(identity.Roles) != 2 || identity.Roles[0] != "operator" || identity.Roles[1] != "responder" {
		t.Errorf("the roles are %v", identity.Roles)
	}
}

func TestAWrongPasswordIsRefused(t *testing.T) {
	d := directoryWith(t)
	c := clientFor(t, d, nil)

	_, err := c.Authenticate("ed", "not-the-password")
	if err == nil {
		t.Fatal("a wrong password authenticated")
	}
	if !IsInvalidCredentials(err) {
		t.Errorf("the failure is not reported as invalid credentials: %v", err)
	}
}

// RFC 4513: an empty password makes a simple bind anonymous, and a
// directory answers success to it. A client that passes one through
// authenticates anybody who leaves the password field blank.
func TestAnEmptyPasswordIsRefusedBeforeTheDirectoryIsAsked(t *testing.T) {
	d := directoryWith(t)
	c := clientFor(t, d, nil)

	if _, err := c.Authenticate("ed", ""); err == nil {
		t.Fatal("an empty password authenticated")
	}
	// The directory was never asked, so no bind was attempted with it.
	if searches := len(d.asked()); searches != 0 {
		t.Errorf("the directory was searched %d times for an empty password", searches)
	}
}

// The LDAP injection boundary. A username of `*)(objectClass=*` in
// `(uid=%s)` would otherwise turn a lookup for one account into a match
// for every account.
func TestAUsernameCannotEscapeItsFilter(t *testing.T) {
	d := directoryWith(t)
	c := clientFor(t, d, nil)

	const injected = "*)(objectClass=*"
	_, err := c.Authenticate(injected, "hunter2")
	if err == nil {
		t.Fatal("an injected filter authenticated")
	}

	searches := d.asked()
	if len(searches) == 0 {
		t.Fatal("nothing was searched")
	}
	sent := searches[0]
	// The filter the server parsed is one equality match whose value is
	// the whole hostile string — not a compound filter. Rendered back
	// to RFC 4515 text, that is one `(`, with the reserved characters
	// escaped.
	if !strings.HasPrefix(sent, "(uid=") || strings.Count(sent, "(") != 1 {
		t.Errorf("the injected filter reached the server as %s", sent)
	}
	if !strings.Contains(sent, Escape(injected)) {
		t.Errorf("the value did not survive escaped: %s", sent)
	}
}

// A filter that matches two accounts is a misconfiguration, and binding
// as whichever the directory listed first would authenticate one
// operator as another.
func TestAFilterMatchingTwoEntriesIsRefused(t *testing.T) {
	d := directoryWith(t)
	d.entry(Entry{
		DN:         "uid=ed,ou=contractors,dc=example,dc=com",
		Attributes: map[string][]string{"uid": {"ed"}},
	})
	c := clientFor(t, d, func(cfg *Config) {
		cfg.UserBaseDN = "dc=example,dc=com"
	})

	_, err := c.Authenticate("ed", "hunter2")
	if err == nil {
		t.Fatal("an ambiguous username authenticated")
	}
	if !strings.Contains(err.Error(), "must match one") {
		t.Errorf("the refusal is %v", err)
	}
}

func TestAGroupSearchFindsWhatMemberOfDoesNot(t *testing.T) {
	d := directoryWith(t)
	// This directory publishes no memberOf.
	d.forget("uid=ed,ou=people,dc=example,dc=com", "memberOf")
	d.entry(Entry{
		DN: "cn=platform,ou=groups,dc=example,dc=com",
		Attributes: map[string][]string{
			"cn":     {"platform"},
			"member": {"uid=ed,ou=people,dc=example,dc=com"},
		},
	})
	c := clientFor(t, d, func(cfg *Config) {
		cfg.MemberOfAttribute = ""
		cfg.GroupBaseDN = "ou=groups,dc=example,dc=com"
		cfg.GroupFilter = "(member=%s)"
	})

	identity, err := c.Authenticate("ed", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if len(identity.Groups) != 1 || identity.Groups[0] != "platform" {
		t.Errorf("the groups are %v", identity.Groups)
	}
	if len(identity.Roles) != 1 || identity.Roles[0] != "operator" {
		t.Errorf("the roles are %v", identity.Roles)
	}
}

// Active Directory nests groups: a group is a member of a group.
func TestNestedGroupsAreFollowedToTheConfiguredDepth(t *testing.T) {
	d := directoryWith(t)
	d.forget("uid=ed,ou=people,dc=example,dc=com", "memberOf")
	d.entry(Entry{
		DN: "cn=sre,ou=groups,dc=example,dc=com",
		Attributes: map[string][]string{
			"cn": {"sre"}, "member": {"uid=ed,ou=people,dc=example,dc=com"},
		},
	})
	d.entry(Entry{
		DN: "cn=platform,ou=groups,dc=example,dc=com",
		Attributes: map[string][]string{
			"cn": {"platform"}, "member": {"cn=sre,ou=groups,dc=example,dc=com"},
		},
	})

	base := func(cfg *Config) {
		cfg.MemberOfAttribute = ""
		cfg.GroupBaseDN = "ou=groups,dc=example,dc=com"
		cfg.GroupFilter = "(member=%s)"
		cfg.RoleMap = map[string][]string{"platform": {"operator"}}
	}

	// Flat: only the direct group, so the nested role is not granted.
	flat, err := clientFor(t, d, base).Authenticate("ed", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if len(flat.Roles) != 0 {
		t.Errorf("a flat lookup granted %v", flat.Roles)
	}

	// One level of nesting reaches the group that grants.
	nested, err := clientFor(t, d, func(cfg *Config) {
		base(cfg)
		cfg.NestedDepth = 2
	}).Authenticate("ed", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if len(nested.Roles) != 1 || nested.Roles[0] != "operator" {
		t.Errorf("a nested lookup granted %v from groups %v", nested.Roles, nested.Groups)
	}
}

// Directories have membership cycles, and following one forever makes
// this a hang rather than a login.
func TestAGroupCycleTerminates(t *testing.T) {
	d := directoryWith(t)
	entry := d.entries["uid=ed,ou=people,dc=example,dc=com"]
	delete(entry.Attributes, "memberOf")
	d.entries["uid=ed,ou=people,dc=example,dc=com"] = entry

	d.entries["cn=a,ou=groups,dc=example,dc=com"] = Entry{
		DN: "cn=a,ou=groups,dc=example,dc=com",
		Attributes: map[string][]string{"cn": {"a"}, "member": {
			"uid=ed,ou=people,dc=example,dc=com", "cn=b,ou=groups,dc=example,dc=com"}},
	}
	d.entries["cn=b,ou=groups,dc=example,dc=com"] = Entry{
		DN:         "cn=b,ou=groups,dc=example,dc=com",
		Attributes: map[string][]string{"cn": {"b"}, "member": {"cn=a,ou=groups,dc=example,dc=com"}},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = clientFor(t, d, func(cfg *Config) {
			cfg.MemberOfAttribute = ""
			cfg.GroupBaseDN = "ou=groups,dc=example,dc=com"
			cfg.GroupFilter = "(member=%s)"
			cfg.NestedDepth = 20
			cfg.RoleMap = map[string][]string{"a": {"operator"}}
		}).Authenticate("ed", "hunter2")
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("a group cycle did not terminate")
	}
}

func TestStartTLSUpgradesAndARefusalIsNotIgnored(t *testing.T) {
	d := newFakeDirectory(t)
	d.configure(func(f *fakeDirectory) { f.plain = true })
	d.user("cn=halite,ou=services,dc=example,dc=com", "service-pw")
	d.user("uid=ed,ou=people,dc=example,dc=com", "hunter2")
	d.entry(Entry{
		DN:         "uid=ed,ou=people,dc=example,dc=com",
		Attributes: map[string][]string{"uid": {"ed"}, "memberOf": {"cn=platform,ou=groups,dc=example,dc=com"}},
	})

	c := clientFor(t, d, func(cfg *Config) { cfg.TLS = TLSStartTLS })
	if _, err := c.Authenticate("ed", "hunter2"); err != nil {
		t.Fatalf("StartTLS failed: %v", err)
	}

	// A server that refuses the upgrade must stop the login rather than
	// continuing in the clear.
	d.configure(func(f *fakeDirectory) { f.startTLSRefused = true })
	if _, err := c.Authenticate("ed", "hunter2"); err == nil {
		t.Fatal("a refused StartTLS continued in the clear")
	}
}

// A simple bind puts an operator's password on the wire.
func TestPlaintextIsRefused(t *testing.T) {
	d := directoryWith(t)
	_, err := New(Config{
		Address: d.address(), TLS: "", BindDN: "cn=x", BindPassword: "y",
		UserBaseDN: "dc=example,dc=com", MemberOfAttribute: "memberOf",
		RoleMap: map[string][]string{"a": {"b"}},
	})
	if err == nil {
		t.Fatal("a configuration with no TLS was accepted")
	}
	if !strings.Contains(err.Error(), "plaintext") {
		t.Errorf("the refusal is %v", err)
	}
}

// A directory misconfiguration found by the first operator to try it is
// found at the worst moment.
func TestAMisconfigurationIsRefusedAtStartup(t *testing.T) {
	base := func() Config {
		return Config{
			Address: "dir.example:636", TLS: TLSLDAPS,
			BindDN: "cn=x", BindPassword: "y",
			UserBaseDN: "dc=example,dc=com", UserFilter: "(uid=%s)",
			MemberOfAttribute: "memberOf",
			RoleMap:           map[string][]string{"a": {"b"}},
		}
	}
	cases := []struct {
		name   string
		adjust func(*Config)
		want   string
	}{
		{"no bind account", func(c *Config) { c.BindDN = "" }, "anonymous bind is refused"},
		{"a filter with no placeholder", func(c *Config) { c.UserFilter = "(uid=ed)" }, "%s"},
		{"a filter that does not parse", func(c *Config) { c.UserFilter = "(uid=%s" }, "does not parse"},
		{"no way to find groups", func(c *Config) { c.MemberOfAttribute = "" }, "group filter or a memberOf"},
		{"no role map", func(c *Config) { c.RoleMap = nil }, "role map"},
		{"a group filter with no base", func(c *Config) {
			c.GroupFilter = "(member=%s)"
			c.GroupBaseDN = ""
		}, "group base DN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.adjust(&cfg)
			_, err := New(cfg)
			if err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal is %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The directory is not this estate's authorization model.
func TestAnUnmappedGroupGrantsNothing(t *testing.T) {
	d := directoryWith(t)
	c := clientFor(t, d, func(cfg *Config) {
		cfg.RoleMap = map[string][]string{"some-other-group": {"administrator"}}
	})

	identity, err := c.Authenticate("ed", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if len(identity.Roles) != 0 {
		t.Errorf("unmapped groups granted %v", identity.Roles)
	}
	if got := UnmappedGroups(identity.Groups, c.cfg.RoleMap); len(got) != 2 {
		t.Errorf("the unmapped groups are %v", got)
	}
}

// Group names are case-insensitive in Active Directory, and an estate
// writing `Platform` should not be defeated by a directory answering
// `platform`.
func TestGroupNamesMatchCaseInsensitively(t *testing.T) {
	d := directoryWith(t)
	c := clientFor(t, d, func(cfg *Config) {
		cfg.RoleMap = map[string][]string{"PLATFORM": {"operator"}}
	})
	identity, err := c.Authenticate("ed", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if len(identity.Roles) != 1 {
		t.Errorf("case-insensitive matching granted %v from %v", identity.Roles, identity.Groups)
	}
}

func TestThePrincipalCanComeFromAnAttribute(t *testing.T) {
	d := directoryWith(t)
	c := clientFor(t, d, func(cfg *Config) { cfg.PrincipalAttribute = "mail" })
	identity, err := c.Authenticate("ed", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if identity.Principal != "ldap:ed@example.com" {
		t.Errorf("the principal is %q", identity.Principal)
	}
}
