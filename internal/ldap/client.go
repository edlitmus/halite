package ldap

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Config is how an estate names its directory.
type Config struct {
	// Address is host:port. TLS is `ldaps` or `starttls`; there is no
	// plaintext mode.
	Address    string
	TLS        string
	CAFile     string
	ServerName string
	Timeout    time.Duration

	// BindDN and BindPassword are the service account this client
	// searches with, before the operator's own bind. A directory that
	// permits an anonymous search does not need one, but SPEC 23.3
	// refuses anonymous bind, so one is required.
	BindDN       string
	BindPassword string

	// UserBaseDN is where operators are looked for, and UserFilter is
	// how. `%s` in the filter is replaced with the escaped username.
	UserBaseDN string
	UserFilter string

	// GroupBaseDN and GroupFilter find the groups an operator is in by
	// searching, for a directory that does not publish `memberOf`.
	// `%s` is replaced with the operator's escaped DN.
	GroupBaseDN string
	GroupFilter string
	// GroupAttribute is the attribute on a group entry holding its
	// name. `cn` almost always.
	GroupAttribute string
	// MemberOfAttribute is the attribute on a user entry that lists
	// their groups directly, which is the cheaper path when the
	// directory publishes it.
	MemberOfAttribute string
	// NestedDepth is how far to follow a group's own memberships.
	// Active Directory needs this; a flat directory sets it to zero.
	NestedDepth int

	// RoleMap maps a group to roles in the RBAC policy. A group with no
	// entry grants nothing.
	RoleMap map[string][]string
	// PrincipalAttribute names the operator in the principal. Empty
	// uses the username they typed.
	PrincipalAttribute string
}

// Client authenticates against a directory.
type Client struct{ cfg Config }

// New checks a configuration and answers with a client.
//
// Everything is checked here rather than at the first login, because a
// directory misconfiguration discovered by the first operator to try it
// is discovered at the worst moment.
func New(cfg Config) (*Client, error) {
	if cfg.Address == "" {
		return nil, fmt.Errorf("an LDAP directory needs an address")
	}
	if cfg.TLS != TLSLDAPS && cfg.TLS != TLSStartTLS {
		return nil, fmt.Errorf("an LDAP directory needs `ldaps` or `starttls`; there is no plaintext mode")
	}
	if cfg.BindDN == "" || cfg.BindPassword == "" {
		return nil, fmt.Errorf("an LDAP directory needs a bind account; anonymous bind is refused (SPEC 23.3)")
	}
	if cfg.UserBaseDN == "" {
		return nil, fmt.Errorf("an LDAP directory needs a user base DN")
	}
	if cfg.UserFilter == "" {
		cfg.UserFilter = "(uid=%s)"
	}
	if !strings.Contains(cfg.UserFilter, "%s") {
		return nil, fmt.Errorf("the user filter has no `%%s` for the username: %q", cfg.UserFilter)
	}
	// Parsed once here so a filter that will not parse is a startup
	// failure rather than a login failure.
	if _, err := ParseFilter(fmt.Sprintf(cfg.UserFilter, "probe")); err != nil {
		return nil, fmt.Errorf("the user filter does not parse: %w", err)
	}
	if cfg.GroupFilter != "" {
		if !strings.Contains(cfg.GroupFilter, "%s") {
			return nil, fmt.Errorf("the group filter has no `%%s` for the user's DN: %q", cfg.GroupFilter)
		}
		if _, err := ParseFilter(fmt.Sprintf(cfg.GroupFilter, "probe")); err != nil {
			return nil, fmt.Errorf("the group filter does not parse: %w", err)
		}
		if cfg.GroupBaseDN == "" {
			return nil, fmt.Errorf("a group filter needs a group base DN")
		}
	}
	if cfg.GroupFilter == "" && cfg.MemberOfAttribute == "" {
		return nil, fmt.Errorf("an LDAP directory needs either a group filter or a memberOf attribute; " +
			"without one an operator is in no groups and can be granted nothing")
	}
	if cfg.GroupAttribute == "" {
		cfg.GroupAttribute = "cn"
	}
	if len(cfg.RoleMap) == 0 {
		return nil, fmt.Errorf("an LDAP directory needs a role map; " +
			"a group with no mapping grants nothing, so nobody could log in")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Client{cfg: cfg}, nil
}

// Identity is an authenticated operator.
type Identity struct {
	// Principal is `ldap:<name>`, the form the RBAC grammar accepts.
	Principal string
	DN        string
	Groups    []string
	Roles     []string
}

// Authenticate binds as the operator and resolves their groups.
//
// Three steps, in this order, and the order is the point:
//
//  1. Bind as the service account and find the operator's entry. The
//     username the operator typed never becomes part of a DN — it goes
//     into a filter, escaped — because a directory's DN syntax is not
//     something a login form should be able to write.
//  2. Bind as the operator's own DN with their password. This is the
//     authentication: the directory decides, not this client.
//  3. Resolve groups on the service account's connection, because an
//     operator often cannot read the group tree.
func (c *Client) Authenticate(username, password string) (*Identity, error) {
	if username == "" || password == "" {
		// Before the directory is touched. An empty password makes a
		// simple bind anonymous, which a directory answers success to.
		return nil, fmt.Errorf("a username and a password are both required: %w", ErrMalformedRequest)
	}

	conn, err := Dial(DialOptions{
		Address: c.cfg.Address, TLS: c.cfg.TLS, CAFile: c.cfg.CAFile,
		ServerName: c.cfg.ServerName, Timeout: c.cfg.Timeout,
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := conn.Bind(c.cfg.BindDN, c.cfg.BindPassword, c.cfg.Timeout); err != nil {
		return nil, fmt.Errorf("this service could not bind to the directory: %w", err)
	}

	entry, err := c.findUser(conn, username)
	if err != nil {
		return nil, err
	}

	// The operator's own bind, on its own connection: a failed bind
	// leaves the connection unauthenticated, and reusing it for the
	// group search would search as nobody.
	if err := c.bindAs(entry.DN, password); err != nil {
		return nil, err
	}

	groups, err := c.resolveGroups(conn, entry)
	if err != nil {
		return nil, err
	}
	name := username
	if c.cfg.PrincipalAttribute != "" {
		if values := entry.Values(c.cfg.PrincipalAttribute); len(values) > 0 {
			name = values[0]
		}
	}
	return &Identity{
		Principal: "ldap:" + name,
		DN:        entry.DN,
		Groups:    groups,
		Roles:     MapRoles(groups, c.cfg.RoleMap),
	}, nil
}

// findUser resolves a username to exactly one entry.
//
// Exactly one: a filter that matches two accounts is a
// misconfiguration, and binding as whichever the directory listed first
// would authenticate one operator as another.
func (c *Client) findUser(conn *Conn, username string) (Entry, error) {
	filter, err := ParseFilter(fmt.Sprintf(c.cfg.UserFilter, Escape(username)))
	if err != nil {
		return Entry{}, err
	}
	attributes := []string{"dn"}
	if c.cfg.MemberOfAttribute != "" {
		attributes = append(attributes, c.cfg.MemberOfAttribute)
	}
	if c.cfg.PrincipalAttribute != "" {
		attributes = append(attributes, c.cfg.PrincipalAttribute)
	}

	entries, err := conn.Search(SearchRequest{
		BaseDN: c.cfg.UserBaseDN, Scope: ScopeWholeSubtree, Filter: filter,
		Attributes: attributes, SizeLimit: 2, TimeLimit: int(c.cfg.Timeout.Seconds()),
	}, c.cfg.Timeout)
	if err != nil {
		return Entry{}, err
	}
	switch len(entries) {
	case 0:
		return Entry{}, ErrNoSuchUser
	case 1:
		return entries[0], nil
	}
	return Entry{}, fmt.Errorf("the user filter matched %d entries; it must match one", len(entries))
}

// ErrNoSuchUser is a username the directory does not have.
//
// Distinct from a wrong password inside this package and indistinguishable
// outside it: the caller reports one message for both, and the
// difference goes to the log.
var ErrNoSuchUser = fmt.Errorf("no such user in the directory")

// bindAs makes a second connection and binds as the operator.
func (c *Client) bindAs(dn, password string) error {
	conn, err := Dial(DialOptions{
		Address: c.cfg.Address, TLS: c.cfg.TLS, CAFile: c.cfg.CAFile,
		ServerName: c.cfg.ServerName, Timeout: c.cfg.Timeout,
	})
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Bind(dn, password, c.cfg.Timeout)
}

// resolveGroups collects an operator's groups by both routes SPEC 23.3
// names, and follows nested memberships to the configured depth.
func (c *Client) resolveGroups(conn *Conn, entry Entry) ([]string, error) {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}

	// `memberOf` holds DNs; the name is the first RDN's value.
	frontier := []string{}
	for _, dn := range entry.Values(c.cfg.MemberOfAttribute) {
		add(groupName(dn))
		frontier = append(frontier, dn)
	}

	// A group search finds what `memberOf` does not publish.
	if c.cfg.GroupFilter != "" {
		found, err := c.searchGroups(conn, entry.DN)
		if err != nil {
			return nil, err
		}
		for _, g := range found {
			add(g.name)
			frontier = append(frontier, g.dn)
		}
	}

	// Nested groups: a group is a member of a group. Active Directory
	// needs this and a flat directory sets the depth to zero.
	for depth := 0; depth < c.cfg.NestedDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, dn := range frontier {
			if c.cfg.GroupFilter == "" {
				break
			}
			found, err := c.searchGroups(conn, dn)
			if err != nil {
				return nil, err
			}
			for _, g := range found {
				if seen[g.name] {
					// A cycle. Directories have them, and following
					// one forever is how this becomes a hang rather
					// than a login.
					continue
				}
				add(g.name)
				next = append(next, g.dn)
			}
		}
		frontier = next
	}

	sort.Strings(names)
	return names, nil
}

type groupEntry struct{ dn, name string }

func (c *Client) searchGroups(conn *Conn, memberDN string) ([]groupEntry, error) {
	filter, err := ParseFilter(fmt.Sprintf(c.cfg.GroupFilter, Escape(memberDN)))
	if err != nil {
		return nil, err
	}
	entries, err := conn.Search(SearchRequest{
		BaseDN: c.cfg.GroupBaseDN, Scope: ScopeWholeSubtree, Filter: filter,
		Attributes: []string{c.cfg.GroupAttribute}, SizeLimit: 500,
		TimeLimit: int(c.cfg.Timeout.Seconds()),
	}, c.cfg.Timeout)
	if err != nil {
		return nil, err
	}
	out := make([]groupEntry, 0, len(entries))
	for _, e := range entries {
		name := groupName(e.DN)
		if values := e.Values(c.cfg.GroupAttribute); len(values) > 0 {
			name = values[0]
		}
		out = append(out, groupEntry{dn: e.DN, name: name})
	}
	return out, nil
}

// groupName reads the value of a DN's first RDN, which is a group's
// name in every directory layout this supports.
func groupName(dn string) string {
	first, _, _ := strings.Cut(dn, ",")
	_, value, ok := strings.Cut(first, "=")
	if !ok {
		return strings.TrimSpace(first)
	}
	return strings.TrimSpace(value)
}

// MapRoles turns directory groups into roles in the RBAC policy.
//
// A group with no entry grants nothing, for the reason it does in
// SPEC 23.4's mapping: the directory is not this estate's authorization
// model, and a group appearing there must not become a role here.
func MapRoles(groups []string, table map[string][]string) []string {
	if len(table) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, group := range groups {
		for _, role := range table[group] {
			seen[role] = true
		}
		// Group names are case-insensitive in Active Directory, and an
		// estate writing `Platform-Team` in its map should not be
		// defeated by a directory answering `platform-team`.
		for name, roles := range table {
			if !equalFold(name, group) {
				continue
			}
			for _, role := range roles {
				seen[role] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for role := range seen {
		out = append(out, role)
	}
	sort.Strings(out)
	return out
}

// UnmappedGroups is which of an operator's groups mapped to nothing.
func UnmappedGroups(groups []string, table map[string][]string) []string {
	var out []string
	for _, group := range groups {
		if len(MapRoles([]string{group}, table)) == 0 {
			out = append(out, group)
		}
	}
	return out
}
