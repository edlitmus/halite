package api

import (
	"fmt"
	"net/http"
	"testing"
)

// The API authorizes a run as the kind it forwards to the hub.
//
// DIVERGENCE 5.296: the policy question carried no kind, so the target
// string was judged as a glob -- and, having no `*?[`, as a literal
// node name -- while the hub compiled it as tgt_type said. A role for
// `web*` could then reach db1 as a list, a compound or a pcre. Every
// "false" row below was a 200 that reached the hub.
func TestARunIsAuthorizedAsTheKindItIsForwardedAs(t *testing.T) {
	cases := []struct {
		tgt, kindField, kind string
		allowed              bool
	}{
		{"web1", "tgt_type", "", true},
		{"web1", "tgt_type", "glob", true},
		{"web1,web2", "tgt_type", "list", true},
		{"web1,db1", "tgt_type", "list", false},
		{"web1,db1", "expr_form", "list", false},
		{"web1,db1", "tgt_type", "L", false},
		{"web1 or db1", "tgt_type", "compound", false},
		{"web1 and not G@x:y or db1", "tgt_type", "compound", false},
		{"web1", "tgt_type", "compound", false},
		{"web1|db1", "tgt_type", "pcre", false},
		{"web1", "tgt_type", "pcre", false},
		{"role:web", "tgt_type", "grain", false},
		{"web1,db1", "tgt_type", "", false},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s/%s", c.kind, c.tgt), func(t *testing.T) {
			l, hub := executeLab(t, operatorPolicy)
			out := l.login(t, "ed", "hunter2")
			body := fmt.Sprintf(`{"tgt":%q,%q:%q,"fun":"test.ping"}`, c.tgt, c.kindField, c.kind)
			res, text := l.post(t, PathRun, body, out.Token)
			if c.allowed {
				if res.StatusCode != http.StatusOK {
					t.Fatalf("%s answered %d: %s", body, res.StatusCode, text)
				}
				if hub.submitted.Target != c.tgt || hub.submitted.TargetKind != c.kind {
					t.Errorf("forwarded %q as %q; authorized %q as %q",
						hub.submitted.Target, hub.submitted.TargetKind, c.tgt, c.kind)
				}
				return
			}
			if res.StatusCode != http.StatusForbidden {
				t.Errorf("%s answered %d, want 403: %s", body, res.StatusCode, text)
			}
			if len(hub.seen) != 0 {
				t.Errorf("a refused run reached the hub: %v", hub.seen)
			}
		})
	}
}

// A role whose target is `*` keeps every kind: the fix narrows scoped
// roles and nothing else.
func TestAnEstateWideRoleStillRunsEveryKind(t *testing.T) {
	const everywhere = `
roles:
  operator:
    - target: '*'
      functions: ['test.ping', 'state.apply']
bindings:
  - principal: 'local:ed'
    roles: ['operator']
`
	for _, c := range []struct{ tgt, kind string }{
		{"web1,db1", "list"},
		{"web1 or db1", "compound"},
		{"web1|db1", "pcre"},
		{"role:web", "grain"},
	} {
		l, hub := executeLab(t, everywhere)
		out := l.login(t, "ed", "hunter2")
		res, text := l.post(t, PathRun,
			fmt.Sprintf(`{"tgt":%q,"tgt_type":%q,"fun":"test.ping"}`, c.tgt, c.kind), out.Token)
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s %q answered %d: %s", c.kind, c.tgt, res.StatusCode, text)
			continue
		}
		if hub.submitted.TargetKind != c.kind {
			t.Errorf("forwarded as %q, want %q", hub.submitted.TargetKind, c.kind)
		}
	}
}

// The {id} of /v1/nodes/{id}/state is one node. It is submitted as a
// list, and a list splits on commas, so a segment with a comma was
// several nodes through an endpoint that names one. It is refused
// before the policy is asked -- under an estate-wide role too, where the
// policy would have said yes -- and nothing reaches the hub.
func TestANodePathSegmentIsOneNode(t *testing.T) {
	const everywhere = `
roles:
  operator:
    - target: '*'
      functions: ['state.apply']
    - runners: ['cache.*']
bindings:
  - principal: 'local:ed'
    roles: ['operator']
`
	for _, pol := range []struct{ name, src string }{
		{"scoped", operatorPolicy},
		{"estate-wide", everywhere},
	} {
		for _, seg := range []string{
			"web1.example,db1.example",
			"web1.example%2Cdb1.example",
			"web1.example%20db1.example",
			"web1.example,",
			"web*",
		} {
			l, hub := executeLab(t, pol.src)
			out := l.login(t, "ed", "hunter2")
			res, text := l.post(t, "/v1/nodes/"+seg+"/state", `{"sls":["web"]}`, out.Token)
			if res.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: /v1/nodes/%s/state answered %d, want 400: %s",
					pol.name, seg, res.StatusCode, text)
			}
			if len(hub.seen) != 0 {
				t.Errorf("%s: /v1/nodes/%s/state reached the hub: %v", pol.name, seg, hub.seen)
			}
		}
	}
}
