package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// reporterPolicy lets one operator run state.apply only as a dry run.
func reporterPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	p, _, err := policy.Load([]byte(`
roles:
  reporter:
    - target: '*'
      functions: ['state.apply']
      args:
        'state.apply':
          require_kwargs: {test: true}
bindings:
  - principal: 'cert:CN=ed'
    roles: ['reporter']
  - principal: 'cert:CN=alice'
    roles: ['reporter']
`), "reporter-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// `require_kwargs: {test: true}` holds a reporter to the dry run on the hub's
// own submit path, which is the one the CLI's `run --test` takes.
//
// A job carries its dry-run flag beside its keywords, and the hub asked the
// policy about the keywords alone, so the flag a reporter passed was
// invisible to it. This is the end-to-end case for 5.487's scenario: the
// reporter's `state.apply` without `--test` was allowed. DIVERGENCE 5.493.
func TestAReporterRoleSubmitsOnlyDryRuns(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = reporterPolicy(t)
	web1 := l.enrolled(t, "web1.example")
	defer l.connect(t, web1, "web1.example", `{}`)()
	op := l.operator(t, "ed")

	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "state.apply", Test: true,
	}); err != nil {
		t.Errorf("a dry run was refused: %v", err)
	}
	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "state.apply", Kwarg: map[string]any{"test": true},
	}); err != nil {
		t.Errorf("a dry run asked for as a keyword was refused: %v", err)
	}
	_, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "state.apply"})
	if err == nil {
		t.Fatal("a real apply was allowed to a role that requires test=true")
	}
	if !strings.Contains(err.Error(), "requires test=true") {
		t.Errorf("the refusal should say what was required: %v", err)
	}
	if _, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "state.apply", Kwarg: map[string]any{"test": "true"},
	}); err == nil {
		t.Error("the string \"true\" was taken for the boolean, which a node refuses")
	}
}

// Killing and resuming a job are authorized as the original request, so they
// carry the original's dry-run flag too: a reporter who may run a dry run may
// also stop it.
func TestAReporterCanKillItsOwnDryRun(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = reporterPolicy(t)
	web1 := l.enrolled(t, "web1.example")
	// A node that does not answer, so the job is still running when it is
	// killed. This used a node that returns at once, and then raced: a job
	// that had already completed is refused a kill (DIVERGENCE 5.515), and
	// the kill that was meant to prove the reporter is authorized to stop a
	// dry run sometimes arrived after the return and failed for another
	// reason.
	defer l.connectSilent(t, web1, "web1.example", `{}`)()
	op := l.operator(t, "ed")

	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "state.apply", Test: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.KillJob(context.Background(), res.JID); err != nil {
		t.Errorf("the reporter could not kill its own dry run: %v", err)
	}
	if _, err := op.ResumeJob(context.Background(), res.JID); err != nil &&
		strings.Contains(err.Error(), "requires test=true") {
		t.Errorf("resuming a dry run was judged as a real apply: %v", err)
	}
}

// A job dispatched on a principal's behalf -- an orchestration step with
// `test: true` -- is judged with its flag, as the same role would be on the
// submit path.
func TestDispatchAsCarriesTheDryRunFlag(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = reporterPolicy(t)
	l.enrolled(t, "web1.example")

	if _, err := l.server.DispatchAs("cert:CN=alice", Submission{
		Target: "web1.example", TargetKind: "L", Fun: "state.apply", Test: true,
	}); err != nil {
		t.Errorf("a dry-run step was refused: %v", err)
	}
	if _, err := l.server.DispatchAs("cert:CN=alice", Submission{
		Target: "web1.example", TargetKind: "L", Fun: "state.apply",
	}); err == nil {
		t.Error("a real step was allowed to a role that requires test=true")
	}
}
