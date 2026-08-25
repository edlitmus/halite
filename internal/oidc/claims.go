package oidc

import (
	"fmt"
	"sort"
	"strings"
)

// ClaimStrings reads a colon-delimited path out of a token's claims and
// answers with the strings it finds.
//
// The path is configurable because providers disagree about where an
// operator's groups live. Keycloak puts them under
// `resource_access:halite:roles`, Okta and Entra use `groups`, and an
// estate with its own issuer uses whatever it chose. SPEC 23.4 makes
// this a setting rather than a guess.
//
// A single string and a list of strings both work: a provider with one
// group per operator often sends the first.
func ClaimStrings(claims map[string]any, path string) []string {
	if path == "" {
		return nil
	}
	value, ok := traverse(claims, path)
	if !ok {
		return nil
	}
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

// claimString reads one string claim by path.
func claimString(claims map[string]any, path string) string {
	value, ok := traverse(claims, path)
	if !ok {
		return ""
	}
	s, _ := value.(string)
	return s
}

// traverse walks a colon-delimited path through nested objects.
func traverse(claims map[string]any, path string) (any, bool) {
	var current any = claims
	for _, segment := range strings.Split(path, ":") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// MapRoles turns the groups a provider sent into roles in the RBAC
// policy.
//
// A group with no entry in the table grants nothing, and that is the
// point: the provider's directory is not this estate's authorization
// model, and an unmapped group appearing there must not become a role
// here. SPEC 23.5's deny-by-default reaches this far.
//
// The result is sorted and deduplicated, so two groups mapping to the
// same role produce one and the token records the same set however the
// provider ordered them.
func MapRoles(groups []string, table map[string][]string) []string {
	if len(table) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, group := range groups {
		for _, role := range table[group] {
			seen[role] = true
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
//
// For the log, and for the message an operator gets when they
// authenticate and are granted no roles: "you are in these groups and
// none of them is mapped" is actionable, and "access denied" is not.
func UnmappedGroups(groups []string, table map[string][]string) []string {
	var out []string
	for _, group := range groups {
		if len(table[group]) == 0 {
			out = append(out, group)
		}
	}
	sort.Strings(out)
	return out
}

// DescribeGroups renders a group list for a message, bounded so that a
// directory with a hundred groups does not produce a hundred-line log
// entry.
func DescribeGroups(groups []string) string {
	const most = 10
	if len(groups) == 0 {
		return "no groups"
	}
	if len(groups) <= most {
		return strings.Join(groups, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(groups[:most], ", "), len(groups)-most)
}
