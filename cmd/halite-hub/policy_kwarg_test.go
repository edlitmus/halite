package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// `policy test --kwarg` kept the pairs with an `=` and dropped the rest,
// so with `deny_kwargs: [mods]` a typo or a bare flag read "allowed by
// role": the question asked was about a request with no keyword at all.
// A `key=value` written as an argument, the way `run` takes one, was
// dropped the same way. Each is a usage error now. DIVERGENCE 5.589.
func TestPolicyTestRefusesAKeywordItCannotRead(t *testing.T) {
	root := t.TempDir()
	policyYAML := `roles:
  webops:
    - target: '*'
      functions: ['state.apply']
      args:
        'state.apply':
          deny_kwargs: ['mods']
bindings:
  - principal: 'cert:CN=alice'
    roles: [webops]
`
	if err := os.WriteFile(filepath.Join(root, "policy.yaml"), []byte(policyYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	ask := func(extra ...string) result {
		return run(t, append([]string{"policy", "test", "--root", root,
			"cert:CN=alice", "web1", "state.apply"}, extra...)...)
	}

	// The controls: the policy reads, and the keyword it names is denied
	// when it is asked about in the form that works.
	if res := ask(); res.code != 0 {
		t.Fatalf("no keyword: exited %d: %s%s", res.code, res.stdout, res.stderr)
	}
	if res := ask("--kwarg", "mods=evil"); res.code != 1 || !strings.Contains(res.stdout, "mods") {
		t.Fatalf("--kwarg mods=evil: exited %d: %s%s; want it denied", res.code, res.stdout, res.stderr)
	}
	if res := ask("--kwarg", "mods="); res.code != 1 {
		t.Errorf("--kwarg mods= (an empty value): exited %d: %s%s; want it denied", res.code, res.stdout, res.stderr)
	}
	if res := ask("--kwarg", "other=1,more=2"); res.code != 0 {
		t.Errorf("two pairs the rule does not name: exited %d: %s%s", res.code, res.stdout, res.stderr)
	}

	for _, tc := range []struct {
		name  string
		extra []string
		says  string
	}{
		{"a colon for an equals sign", []string{"--kwarg", "mods:evil"}, "--kwarg"},
		{"a key with no value", []string{"--kwarg", "mods"}, "--kwarg"},
		{"a bare --kwarg", []string{"--kwarg"}, "--kwarg"},
		{"an empty --kwarg", []string{"--kwarg="}, "--kwarg"},
		{"an empty key", []string{"--kwarg", "=evil"}, "--kwarg"},
		{"a comma inside a value", []string{"--kwarg", "mods=a,b"}, "cannot hold one"},
		{"an empty pair in a list", []string{"--kwarg", "x=1,,mods=evil"}, "--kwarg"},
		{"a key given twice", []string{"--kwarg", "mods=a,mods=b"}, "twice"},
		{"a keyword as an argument, the way run takes one", []string{"mods=evil"}, "--kwarg key=value"},
	} {
		res := ask(tc.extra...)
		if res.code != cli.ExitUsage {
			t.Errorf("%s: exited %d, want %d: %s%s", tc.name, res.code, cli.ExitUsage, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stderr, tc.says) {
			t.Errorf("%s: the message does not say %q: %s", tc.name, tc.says, res.stderr)
		}
		if strings.Contains(res.stdout, "allowed") {
			t.Errorf("%s: answered as though it had been understood: %s", tc.name, res.stdout)
		}
	}
}
