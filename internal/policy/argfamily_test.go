package policy

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/state"
)

// The role issue 621 ran through the policy: `state.*` granted, with the
// argument rules written under `state.apply`, and every runner granted, with
// allow_mods written under `state.orchestrate`. On main every request below
// marked refused was allowed, because Rule.Args was read by the exact name of
// the function asked for, and only state.apply and state.orchestrate had one.
const siblingRole = `
roles:
  web:
    - target: '*'
      functions: ['state.*']
      args:
        'state.apply':
          allow_sls: ['web.*']
          deny_kwargs: ['pillar']
    - runners: ['state.*']
      args:
        'state.orchestrate':
          allow_mods: ['deploy.*']
bindings:
  - principal: 'cert:CN=web'
    roles: ['web']
`

func TestAnArgumentRuleHoldsEveryFunctionThatTakesTheSameNames(t *testing.T) {
	p, _, err := Load([]byte(siblingRole), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	catalogued(p)
	for _, tc := range []struct {
		name   string
		req    Request
		allow  bool
		reason string
	}{
		// The five cases of issue 621.
		{"state.sls of a name outside allow_sls",
			Request{Fun: "state.sls", Arg: []string{"db.secret"}},
			false, "db.secret is not in the SLS names this role may apply"},
		{"state.sls of an allowed name with pillar",
			Request{Fun: "state.sls", Arg: []string{"web.x"}, Kwarg: map[string]any{"pillar": "{}"}},
			false, `may not be called with "pillar"`},
		{"state.highstate",
			Request{Fun: "state.highstate"},
			false, "runs the whole highstate"},
		{"state.show_sls of a name outside allow_sls",
			Request{Fun: "state.show_sls", Arg: []string{"db.secret"}},
			false, "db.secret is not in the SLS names"},
		{"state.orch under allow_mods written on state.orchestrate",
			Request{Fun: "state.orch", Runner: true, Kwarg: map[string]any{"sls": "teardown"}},
			false, "teardown is not in the modules this role may orchestrate"},

		// The reason says where the rule was written, since the operator
		// reading it wrote it under another name.
		{"the reason names the rule's own function",
			Request{Fun: "state.sls", Arg: []string{"db.secret"}},
			false, "written for state.apply"},

		// The rest of each family, and the same rules still allowing what
		// they allow.
		{"state.show_top of a name outside allow_sls",
			Request{Fun: "state.show_top", Arg: []string{"db.secret"}},
			false, "db.secret is not in the SLS names"},
		{"state.show_lowstate with no names renders the highstate",
			Request{Fun: "state.show_lowstate"},
			false, "runs the highstate"},
		{"state.orchestrate_show_sls of a name outside allow_mods",
			Request{Fun: "state.orchestrate_show_sls", Runner: true, Arg: []string{"teardown"}},
			false, "teardown is not in the modules"},
		{"state.sls of an allowed name", Request{Fun: "state.sls", Arg: []string{"web.x"}}, true, ""},
		{"state.apply of an allowed name", Request{Fun: "state.apply", Arg: []string{"web.x"}}, true, ""},
		{"state.show_sls of an allowed name", Request{Fun: "state.show_sls", Arg: []string{"web.x"}}, true, ""},
		{"state.orch of an allowed name",
			Request{Fun: "state.orch", Runner: true, Kwarg: map[string]any{"sls": "deploy.web"}}, true, ""},
		// A runner outside the family is not held to a family's rule.
		{"state.orch_list", Request{Fun: "state.orch_list", Runner: true}, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.req.Principal = "cert:CN=web"
			if !tc.req.Runner {
				tc.req.Target = "web1"
			}
			d := p.Authorize(tc.req)
			if d.Allowed != tc.allow {
				t.Fatalf("allowed=%v, want %v: %s", d.Allowed, tc.allow, d)
			}
			if !strings.Contains(d.Reason, tc.reason) {
				t.Errorf("reason %q does not say %q", d.Reason, tc.reason)
			}
		})
	}
}

// Two argument rules in one family both hold: a request must satisfy each.
// Letting the exact name's rule replace the others would make writing a
// narrow rule for one function a way to lift a wider one from it.
func TestEveryArgumentRuleInTheFamilyHolds(t *testing.T) {
	src := `
roles:
  r:
    - target: '*'
      functions: ['state.apply', 'state.sls']
      args:
        'state.apply':
          allow_sls: ['web.*']
        'state.sls':
          deny_kwargs: ['pillar']
bindings:
  - principal: 'cert:CN=r'
    roles: ['r']
`
	p, _, err := Load([]byte(src), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	catalogued(p)
	for _, tc := range []struct {
		req    Request
		reason string
	}{
		{Request{Fun: "state.sls", Arg: []string{"db.secret"}}, "db.secret is not in the SLS names"},
		{Request{Fun: "state.apply", Arg: []string{"web.x"}, Kwarg: map[string]any{"pillar": "{}"}}, `"pillar"`},
	} {
		tc.req.Principal, tc.req.Target = "cert:CN=r", "*"
		if d := p.Authorize(tc.req); d.Allowed || !strings.Contains(d.Reason, tc.reason) {
			t.Errorf("%s %v: %s, want refused saying %q", tc.req.Fun, tc.req.Arg, d, tc.reason)
		}
	}
}

// The job family is the node's own list of the functions it answers through
// the state compiler, every one of which takes SLS names positionally
// (runStateJob: CompileSLS(j.Arg), or the highstate). It is read from
// state.JobFunctions rather than copied, so a function the node starts
// answering joins it. This holds that, and holds that nothing in the
// execution registry takes SLS names outside it: a `state.*` function there,
// or one with a parameter named for SLS names, would be a member the family
// does not know about.
func TestTheSLSFamilyIsEveryFunctionThatTakesSLSNames(t *testing.T) {
	got := ArgumentFamily("state.apply", false)
	want := state.JobFunctions()
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the SLS family is %v, and the node answers %v through its compiler", got, want)
	}
	exec := builtin.New().Exec.Signatures()
	for _, name := range exec.Names() {
		sig, _ := exec.Lookup(name)
		if sig.Module == "state" {
			t.Errorf("%s is a state function in the execution registry, which the SLS family does not hold", name)
		}
		for _, param := range sig.Params {
			switch param.Name {
			case "sls", "mods", "highstate":
				t.Errorf("%s takes %q, which looks like SLS names; put it in a family or say here why not", name, param.Name)
			}
		}
	}
}
