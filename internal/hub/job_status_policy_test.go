package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// Reading a job is authorized like running it.
//
// GET /v1/jobs/{jid} checked only that the caller held an operator
// certificate. An operator bound to `web*` was refused a submit against
// `db1` and could then read every return a `db1` job had made, which is
// state output and so may hold secrets. DIVERGENCE 5.513.
func TestJobStatusIsAuthorizedLikeTheJobWas(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	l.enrolled(t, "db1.example")
	loaded, _, err := policy.Load([]byte(`
roles:
  administrator:
    - target: '*'
      functions: ['*', 'cmd.run']
    - target: '*'
      runners: ['*']
  webonly:
    - target: 'web*'
      functions: ['test.ping']
  runnerreader:
    - target: '*'
      runners: ['pillar.show_pillar']
bindings:
  - principal: 'cert:CN=ed'
    roles: ['administrator']
  - principal: 'cert:CN=mallory'
    roles: ['webonly']
  - principal: 'cert:CN=rita'
    roles: ['runnerreader']
`), "job-status-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded
	admin := l.operator(t, "ed")
	sub, err := admin.Submit(context.Background(), transport.SubmitRequest{
		Target: "db1.example", TargetKind: "L", Fun: "test.ping", Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := admin.JobStatus(context.Background(), sub.JID); err != nil {
		t.Errorf("the operator who may run the job cannot read it: %v", err)
	}
	st, err := l.operator(t, "mallory").JobStatus(context.Background(), sub.JID)
	if err == nil {
		t.Errorf("an operator who may not run anything on db1 read its job: %+v", st)
	} else if !strings.Contains(err.Error(), "permits") {
		t.Errorf("refused, but not by the policy: %v", err)
	}

	// A runner call is a job against the pseudo-target "hub"; it is read by
	// whoever holds the runner, and not by one who holds only node rules.
	l.server.recordRunnerJob("20261101T000000000001", RunnerCall{
		Principal: "cert:CN=ed", Fun: "pillar.show_pillar",
	}, l.server.now())
	if _, err := l.operator(t, "rita").JobStatus(context.Background(), "20261101T000000000001"); err != nil {
		t.Errorf("an operator holding the runner cannot read its call: %v", err)
	}
	if _, err := l.operator(t, "mallory").JobStatus(context.Background(), "20261101T000000000001"); err == nil {
		t.Error("an operator with no runner grant read a runner call")
	}
}
