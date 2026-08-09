package sls

import (
	"fmt"
	"path"

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
// Target patterns: '*' matches every host; 'grain:valueglob' matches a
// grain (glob on the value, e.g. 'os_family:FreeBSD' or 'osrelease:14.*');
// any other pattern globs against the host grain. All environments in the
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
			if !TargetMatch(pat, grains) {
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

// TargetMatch reports whether a target pattern selects a host with the
// given grains. It is the one targeting language halite has: top files use
// it to pick SLS names, and the control plane uses it to pick agents.
func TargetMatch(pat string, grains map[string]any) bool {
	if pat == "*" {
		return true
	}
	for i := 0; i < len(pat); i++ {
		if pat[i] == ':' {
			key, valPat := pat[:i], pat[i+1:]
			gv := fmt.Sprintf("%v", grains[key])
			ok, err := path.Match(valPat, gv)
			return err == nil && ok
		}
	}
	// A bare pattern globs the host's identity: the `id` grain, which is the
	// enrolled name under a control plane, falling back to the hostname.
	name, _ := grains["id"].(string)
	if name == "" {
		name, _ = grains["host"].(string)
	}
	ok, err := path.Match(pat, name)
	return err == nil && ok
}
