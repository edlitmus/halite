package main

import (
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/ldap"
	"github.com/edlitmus/halite/internal/value"
)

// ldapClient builds the directory of SPEC 23.3, or nil when none is
// configured.
//
// A misconfiguration stops the service, for the reason the OIDC one
// does: an estate that named a directory meant its operators to log in
// through it, and finding out otherwise one operator at a time is the
// worst way to find out.
func (s *service) ldapClient() *ldap.Client {
	address := s.cfg.String("ldap_address", "")
	if address == "" {
		return nil
	}
	client, err := ldap.New(ldap.Config{
		Address:            address,
		TLS:                s.cfg.String("ldap_tls", ldap.TLSLDAPS),
		CAFile:             s.cfg.String("ldap_ca_file", ""),
		ServerName:         s.cfg.String("ldap_server_name", ""),
		Timeout:            s.cfg.Duration("ldap_timeout", 10*time.Second),
		BindDN:             s.cfg.String("ldap_bind_dn", ""),
		BindPassword:       ldapBindPassword(s),
		UserBaseDN:         s.cfg.String("ldap_user_base_dn", ""),
		UserFilter:         s.cfg.String("ldap_user_filter", "(uid=%s)"),
		MemberOfAttribute:  s.cfg.String("ldap_member_of_attribute", "memberOf"),
		GroupBaseDN:        s.cfg.String("ldap_group_base_dn", ""),
		GroupFilter:        s.cfg.String("ldap_group_filter", ""),
		GroupAttribute:     s.cfg.String("ldap_group_attribute", "cn"),
		NestedDepth:        int(s.cfg.Int("ldap_nested_depth", 0)),
		PrincipalAttribute: s.cfg.String("ldap_principal_attribute", ""),
		RoleMap:            roleMap(s.cfg.Get("ldap_role_map")),
	})
	if err != nil {
		cli.Fatalf("ldap: %v", err)
	}
	return client
}

// ldapBindPassword reads the service account's password, preferring the
// file: a password in the configuration is a password in whatever
// distributes the configuration.
func ldapBindPassword(s *service) string {
	if path := s.cfg.String("ldap_bind_password_file", ""); path != "" {
		secret, err := config.ReadSecretFile(path)
		if err != nil {
			cli.Fatalf("ldap: %v", err)
		}
		return secret
	}
	return s.cfg.String("ldap_bind_password", "")
}

// roleMap reads a group-to-roles mapping out of a configuration value.
//
// Shared by `ldap_role_map` and `oidc_role_map`, which have the same
// shape and the same rule: a group with no entry grants nothing. The
// caller does the lookup, with the key spelled out, so the audit that
// finds settings nothing reads can still see it.
func roleMap(raw any, present bool) map[string][]string {
	if !present || raw == nil {
		return nil
	}
	m, ok := raw.(*value.Map)
	if !ok {
		cli.Fatalf("a role map is a mapping of group to roles, not %s", value.TypeName(raw))
	}
	out := map[string][]string{}
	for _, e := range m.Entries() {
		group := value.KeyString(e.Key)
		switch v := e.Val.(type) {
		case string:
			out[group] = []string{v}
		case []any:
			for _, item := range v {
				if role := value.KeyString(item); role != "" {
					out[group] = append(out[group], role)
				}
			}
		default:
			cli.Fatalf("a role map maps a group to a role or a list of them; %s maps to %s",
				group, value.TypeName(e.Val))
		}
	}
	return out
}
