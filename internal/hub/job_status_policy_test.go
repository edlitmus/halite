package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/job"
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

// Stopping a runner call is authorized the way reading it is: as the runner.
//
// `kill` built its request from the job's target and function as a node job,
// and a runner call is recorded against the pseudo-target "hub", so the
// operator who held the runner and could read the call could not stop it, and
// one holding only node rules was judged by what a glob over "hub" said.
// DIVERGENCE 5.516.
func TestAnOperatorHoldingTheRunnerCanStopItsCall(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	loaded, _, err := policy.Load([]byte(`
roles:
  runnerholder:
    - target: '*'
      runners: ['pillar.show_pillar']
  everynode:
    - target: '*'
      functions: ['*']
bindings:
  - principal: 'cert:CN=rita'
    roles: ['runnerholder']
  - principal: 'cert:CN=nodey'
    roles: ['everynode']
`), "kill-runner-job.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded
	const jid = "20261101T000000000002"
	l.server.recordRunnerJob(job.ID(jid), RunnerCall{
		Principal: "cert:CN=rita", Fun: "pillar.show_pillar",
	}, l.server.now())

	if _, err := l.operator(t, "nodey").KillJob(context.Background(), jid); err == nil {
		t.Error("an operator with node rules and no runner grant stopped a runner call")
	}
	if _, err := l.operator(t, "rita").KillJob(context.Background(), jid); err != nil {
		t.Errorf("the operator holding the runner cannot stop its call: %v", err)
	}
}
