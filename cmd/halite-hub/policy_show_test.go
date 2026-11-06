package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `policy show` says, for a rule with argument rules, which functions each
// one holds and which granted functions none of them holds. Issue 621's
// role -- `state.*` granted, its argument rules written for state.apply --
// left state.sls, state.highstate and state.show_sls unconstrained, and
// nothing an operator could run would have shown it; this is the command
// that now does.
func TestPolicyShowSaysWhatNoArgumentRuleHolds(t *testing.T) {
	root := t.TempDir()
	const src = `
roles:
  web:
    - target: '*'
      functions: ['state.*', 'pkg.installed']
      args:
        'state.apply':
          allow_sls: ['web.*']
    - runners: ['state.orchestrate', 'jobs.lookup_jid']
      args:
        'state.orchestrate':
          allow_mods: ['deploy.*']
    - target: '*'
      functions: ['state.sls']
      args:
        'state.sls':
          deny_kwargs: ['pillar']
    - target: '*'
      functions: ['test.ping']
bindings:
  - principal: 'cert:CN=web'
    roles: ['web']
`
	if err := os.WriteFile(filepath.Join(root, "policy.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	res := run(t, "policy", "show", "--root", root)
	if res.code != 0 {
		t.Fatalf("policy show exited %d: %s", res.code, res.stderr)
	}
	for _, want := range []string{
		"state.apply: holds state.apply, state.highstate, state.show_highstate, state.show_lowstate, " +
			"state.show_sls, state.show_states, state.show_top, state.sls\n",
		// state.* is held whole, so only the exact name is left.
		"     no argument rule holds: pkg.installed\n",
		"state.orchestrate: holds state.orch, state.orch_resume, state.orchestrate, state.orchestrate_show_sls\n",
		"     no argument rule holds: runner jobs.lookup_jid\n",
		"     every function this rule grants is held by an argument rule\n",
	} {
		if !strings.Contains(res.stdout, want) {
			t.Errorf("policy show does not say %q:\n%s", want, res.stdout)
		}
	}
	// A rule with no argument rules says nothing about them.
	if strings.Count(res.stdout, "no argument rule holds") != 2 {
		t.Errorf("want the unheld line for the two rules that have argument rules only:\n%s", res.stdout)
	}
}
