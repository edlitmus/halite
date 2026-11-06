package policy

import (
	"reflect"
	"strings"
	"testing"
)

// A key a rule or an argument rule is not read for is refused at load,
// naming it. It used to be ignored: `allow_envv: [base]` loaded, and the
// role ran state.apply in any environment (DIVERGENCE 5.590).
func TestAnUnknownKeyInARuleIsRefusedByName(t *testing.T) {
	cases := []struct {
		name string
		rule string
		say  []string
	}{
		{
			"a misspelt allow_env",
			`
    - target: '*'
      functions: ['state.apply']
      args:
        'state.apply':
          allow_envv: ['base']`,
			[]string{`role "ops" rule 0`, `args for "state.apply"`, "`allow_envv` is not a key of an argument rule",
				"allow_env, allow_mods, allow_sls, deny_env, deny_kwargs, deny_sls, require_kwargs",
				"did you mean `allow_env`?"},
		},
		{
			"a misspelt deny_kwargs, beside a good key",
			`
    - target: '*'
      functions: ['test.ping']
    - target: '*'
      functions: ['state.sls']
      args:
        'state.sls':
          allow_sls: ['web.*']
          deny_kwarg: ['pillar']`,
			[]string{`role "ops" rule 1`, "`deny_kwarg`", "did you mean `deny_kwargs`?"},
		},
		{
			"a key nothing is close to",
			`
    - target: '*'
      functions: ['state.apply']
      args:
        'state.apply':
          only_on_tuesdays: true`,
			[]string{"`only_on_tuesdays` is not a key of an argument rule"},
		},
		{
			// `arg:` drops every argument rule the role was written with.
			"a misspelt args",
			`
    - target: '*'
      functions: ['state.apply']
      arg:
        'state.apply':
          allow_sls: ['web.*']`,
			[]string{`role "ops" rule 0`, "`arg` is not a key of a rule",
				"act_for, args, functions, runners, target", "did you mean `args`?"},
		},
		{
			// Granted functions, so the "grants nothing" check never fired,
			// and the runners were silently not granted.
			"a misspelt runners beside functions",
			`
    - target: '*'
      functions: ['test.ping']
      runner: ['jobs.lookup_jid']`,
			[]string{"`runner` is not a key of a rule", "did you mean `runners`?"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "roles:\n  ops:" + c.rule + "\nbindings:\n  - principal: 'cert:CN=ops'\n    roles: ['ops']\n"
			p, _, err := Load([]byte(src), "policy.yaml")
			if err == nil {
				t.Fatalf("loaded a policy with an unknown key: %+v", p.Roles["ops"])
			}
			for _, want := range c.say {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not say %q:\n%v", want, err)
				}
			}
		})
	}
}

// Every key in argRuleKeys loads, and is read into the ArgRule: a key in
// the list that nothing reads would be accepted and enforce nothing, which
// is the defect this list exists to end. A key read and missing from the
// list is the other half, and is refused by Load before it is read, which
// the tests that use each key (env_test, require_kwargs_test, ...) would
// show.
func TestEveryArgRuleKeyLoadsAndIsRead(t *testing.T) {
	for _, key := range argRuleKeys {
		t.Run(key, func(t *testing.T) {
			val := "['x']"
			if key == "require_kwargs" {
				val = "{test: true}"
			}
			src := "roles:\n  ops:\n    - target: '*'\n      functions: ['state.apply']\n      args:\n" +
				"        'state.apply':\n          " + key + ": " + val + "\n"
			p, _, err := Load([]byte(src), "policy.yaml")
			if err != nil {
				t.Fatal(err)
			}
			ar := p.Roles["ops"][0].Args["state.apply"]
			if reflect.ValueOf(ar).IsZero() {
				t.Errorf("%s loaded and was not read into the argument rule", key)
			}
		})
	}
}

// The suggestion is only made when something is close; a long unrelated
// key gets the list alone.
func TestClosestKey(t *testing.T) {
	for k, want := range map[string]string{
		"allow_envv":  "allow_env",
		"alow_sls":    "allow_sls",
		"deny_kwargs": "deny_kwargs",
		"tag":         "",
		"comment":     "",
	} {
		if got := closestKey(k, argRuleKeys); got != want {
			t.Errorf("closestKey(%q) = %q, want %q", k, got, want)
		}
	}
}
