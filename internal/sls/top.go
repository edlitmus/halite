package sls

import (
	"fmt"

	"github.com/edlitmus/halite/internal/yamlite"
)

// MatchTop evaluates a parsed top.sls against grains and returns the SLS
// names to apply, in declaration order, deduplicated. The pillar tree uses
// the same top file semantics.
//
// Top file shape (environments -> target patterns -> sls names):
//
//	base:
//	  '*':
//	    - base
//	  'os_family:FreeBSD':
//	    - freebsd.tuning
//	  'web*':
//	    - webserver
//
// Target patterns are the language in target.go: globs, grain matches,
// G@/L@/E@/P@, and boolean combinations of them. All environments in the
// file are applied (masterless has no environment selection yet).
func MatchTop(root any, grains map[string]any) ([]string, error) {
	m, ok := root.(*yamlite.Map)
	if !ok {
		return nil, fmt.Errorf("top file must be a mapping of environments")
	}
	var names []string
	seen := map[string]bool{}
	for _, env := range m.Keys {
		envBody, ok := m.Vals[env].(*yamlite.Map)
		if !ok {
			return nil, fmt.Errorf("environment %q must be a mapping of targets", env)
		}
		for _, pat := range envBody.Keys {
			// A target that does not parse is the file's problem, not this
			// host's: reported, rather than quietly selecting nothing.
			matched, err := MatchTarget(pat, grains)
			if err != nil {
				return nil, fmt.Errorf("target %q: %w", pat, err)
			}
			if !matched {
				continue
			}
			list, ok := envBody.Vals[pat].([]any)
			if !ok {
				return nil, fmt.Errorf("target %q must map to a list of sls names", pat)
			}
			for _, item := range list {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("target %q: entries must be sls names, got %v", pat, item)
				}
				if !seen[s] {
					seen[s] = true
					names = append(names, s)
				}
			}
		}
	}
	return names, nil
}
