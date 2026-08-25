package main

import (
	"strings"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/oidc"
	"github.com/edlitmus/halite/internal/value"
)

// oidcProvider builds the identity provider of SPEC 23.4, or nil when
// none is configured.
//
// A misconfiguration stops the service. An estate that wrote an issuer
// meant its operators to log in through it, and starting with OIDC
// quietly disabled would send every one of them to a password prompt
// they may not have an account for.
func (s *service) oidcProvider() *oidc.Provider {
	issuer := s.cfg.String("oidc_issuer", "")
	if issuer == "" {
		return nil
	}
	provider, err := oidc.New(oidc.Config{
		Issuer:         issuer,
		ClientID:       s.cfg.String("oidc_client_id", ""),
		ClientSecret:   oidcSecret(s),
		Audience:       s.cfg.String("oidc_audience", ""),
		RedirectURL:    s.cfg.String("oidc_redirect_url", ""),
		Scopes:         splitList(s.cfg.String("oidc_scopes", "")),
		GroupsClaim:    s.cfg.String("oidc_groups_claim", "groups"),
		PrincipalClaim: s.cfg.String("oidc_principal_claim", "sub"),
		RoleMap:        oidcRoleMap(s),
		Skew:           s.cfg.Duration("oidc_skew", 60*time.Second),
		CAFile:         s.cfg.String("oidc_ca_file", ""),
	})
	if err != nil {
		cli.Fatalf("oidc: %v", err)
	}
	return provider
}

// oidcSecret reads the client secret, preferring the file form for the
// reason every other secret here does: a secret in the configuration is
// a secret in whatever distributes the configuration.
func oidcSecret(s *service) string {
	if path := s.cfg.String("oidc_client_secret_file", ""); path != "" {
		secret, err := config.ReadSecretFile(path)
		if err != nil {
			cli.Fatalf("oidc: %v", err)
		}
		return secret
	}
	return s.cfg.String("oidc_client_secret", "")
}

// oidcRoleMap reads `oidc_role_map`, a mapping of provider group to
// roles in the policy.
//
// A group that is not here grants nothing, so an empty map with an
// issuer configured means nobody can log in — which is refused at
// startup rather than discovered by the first operator who tries.
func oidcRoleMap(s *service) map[string][]string {
	raw, ok := s.cfg.Get("oidc_role_map")
	if !ok || raw == nil {
		cli.Fatalf("oidc: `oidc_issuer` is set and `oidc_role_map` is not; " +
			"a group with no mapping grants nothing, so nobody could log in")
	}
	m, ok := raw.(*value.Map)
	if !ok {
		cli.Fatalf("oidc: `oidc_role_map` is a mapping of group to roles, not %s", value.TypeName(raw))
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
			cli.Fatalf("oidc: `oidc_role_map`: %s maps to %s, and a group maps to a role or a list of them",
				group, value.TypeName(e.Val))
		}
	}
	if len(out) == 0 {
		cli.Fatalf("oidc: `oidc_role_map` is empty; a group with no mapping grants nothing")
	}
	return out
}

// splitList reads a comma-separated setting.
func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
