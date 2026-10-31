package policy

import (
	"strings"
	"testing"
)

// A role can require a keyword, and `test: true` makes it a dry-run role.
//
// Chapter 24 said a `reporter` certificate could be given "a role that may run
// state.apply only with --test-style safety by pairing the schedule with a
// policy that denies anything else". Nothing in the grammar could say it: the
// argument limits were allow_sls, deny_sls, allow_mods and deny_kwargs, and
// `deny_kwargs: [test]` forbids the one thing the job passes. Run against a
// throwaway hub, the reporter role's `state.apply` without `--test` was
// allowed and wrote a file (DIVERGENCE 5.487, issue 526).
//
// The dry-run flag is not in Kwarg. A job carries `Test` beside its keywords
// and job.WireKwargs puts it on the wire as `test`, over whatever Kwarg says,
// so the cases below include the ones where the two disagree and the node
// would see true.
const requireTest = `
roles:
  reporter:
    - target: '*'
      functions: ['state.apply']
      args:
        'state.apply':
          require_kwargs: {test: true}
bindings:
  - principal: 'cert:CN=reporter'
    roles: ['reporter']
`

func authorizeReporter(t *testing.T, src string, req Request) Decision {
	t.Helper()
	p, _, err := Load([]byte(src), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	req.Principal, req.Target, req.Fun = "cert:CN=reporter", "*", "state.apply"
	return p.Authorize(req)
}

func TestRequireKwargsHoldsAReporterToTheDryRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		req   Request
		allow bool
		why   string
	}{
		{"the flag", Request{Test: true}, true, ""},
		{"a keyword true", Request{Kwarg: map[string]any{"test": true}}, true, ""},
		// WireKwargs sets test=true over the keyword, so the node dry-runs.
		{"the flag over a keyword false", Request{Test: true, Kwarg: map[string]any{"test": false}}, true, ""},
		{"neither", Request{}, false, "requires test=true"},
		{"keyword false", Request{Kwarg: map[string]any{"test": false}}, false, "has test=false"},
		// A node refuses a non-boolean test (jobTestFlag); the policy must not
		// call the string the real thing.
		{"keyword the string true", Request{Kwarg: map[string]any{"test": "true"}}, false, "has test=true"},
		{"keyword 1", Request{Kwarg: map[string]any{"test": float64(1)}}, false, "has test=1"},
		{"another keyword only", Request{Kwarg: map[string]any{"pillar": "{}"}}, false, "requires test=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := authorizeReporter(t, requireTest, tc.req)
			if d.Allowed != tc.allow {
				t.Fatalf("allowed = %v, want %v (%s)", d.Allowed, tc.allow, d)
			}
			if !tc.allow && !strings.Contains(d.Reason, tc.why) {
				t.Errorf("reason %q does not say %q", d.Reason, tc.why)
			}
		})
	}
}

// Strings and numbers compare exactly, a number is a number whatever type
// the YAML file and the JSON body gave it, and a name is not a pattern.
func TestRequireKwargsComparesValuesExactly(t *testing.T) {
	const src = `
roles:
  r:
    - target: '*'
      functions: ['state.apply']
      args:
        'state.apply':
          require_kwargs: {saltenv: base, retries: 3, 'te*': true}
bindings:
  - {principal: 'cert:CN=reporter', roles: ['r']}
`
	ok := map[string]any{"saltenv": "base", "retries": float64(3), "te*": true}
	if d := authorizeReporter(t, src, Request{Kwarg: ok}); !d.Allowed {
		t.Errorf("the exact values were refused: %s", d)
	}
	for name, kw := range map[string]map[string]any{
		"wrong string":     {"saltenv": "prod", "retries": float64(3), "te*": true},
		"wrong number":     {"saltenv": "base", "retries": float64(4), "te*": true},
		"number as string": {"saltenv": "base", "retries": "3", "te*": true},
		// `te*` is a keyword's name, not a glob over the call's: `test` does
		// not satisfy it.
		"a pattern matching another name": {"saltenv": "base", "retries": float64(3), "test": true},
	} {
		if d := authorizeReporter(t, src, Request{Kwarg: kw}); d.Allowed {
			t.Errorf("%s: allowed", name)
		}
	}
}

// deny_kwargs judges the keywords the node receives, `test` included.
//
// It read Kwarg alone, so `deny_kwargs: ['*']` and `['t*']` let a dry-run
// flag through that the node reads as a keyword. Now they refuse it.
func TestDenyKwargsSeesTheDryRunFlag(t *testing.T) {
	const src = `
roles:
  r:
    - target: '*'
      functions: ['state.apply']
      args:
        'state.apply': {deny_kwargs: ['te*']}
bindings:
  - {principal: 'cert:CN=reporter', roles: ['r']}
`
	if d := authorizeReporter(t, src, Request{Test: true}); d.Allowed {
		t.Error("deny_kwargs: te* did not see the test flag")
	}
	if d := authorizeReporter(t, src, Request{}); !d.Allowed {
		t.Errorf("a call with no keywords was refused: %s", d)
	}
}

// A requirement that cannot be compared exactly is refused at load.
func TestRequireKwargsIsValidatedAtLoad(t *testing.T) {
	for name, body := range map[string]string{
		"a list":          `require_kwargs: [test]`,
		"a string":        `require_kwargs: test`,
		"a list value":    `require_kwargs: {test: [true]}`,
		"a null":          `require_kwargs: {test: null}`,
		"a mapping value": `require_kwargs: {test: {a: b}}`,
	} {
		src := "roles:\n  r:\n    - target: '*'\n      functions: ['state.apply']\n      args:\n        'state.apply': {" + body + "}\n"
		if _, _, err := Load([]byte(src), "policy.yaml"); err == nil {
			t.Errorf("%s: loaded", name)
		} else if !strings.Contains(err.Error(), "require_kwargs") {
			t.Errorf("%s: the error does not name the field: %v", name, err)
		}
	}
}
