package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// scopedPolicy gives alice the web nodes and ed the estate, and lets
// lb1 read the web nodes' mine.
func scopedPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	loaded, _, err := policy.Load([]byte(`
roles:
  webops:
    - target: 'web*.example'
      functions: ['test.ping']
  administrator:
    - target: '*'
      functions: ['*']
    - target: '*'
      runners: ['*']
  backends-may-be-read:
    - target: 'web*.example'
      functions: ['backend']
bindings:
  - principal: 'cert:CN=alice'
    roles: ['webops']
  - principal: 'cert:CN=ed'
    roles: ['administrator']
  - principal: 'node:lb1.example'
    roles: ['backends-may-be-read']
`), "scoped-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

// An operator submitting straight to the hub is authorized as the kind
// the job is dispatched under. DIVERGENCE 5.296: the hub's own check
// carried no kind either, so a scoped operator did not need the API to
// reach db1 -- every "refused" row here was dispatched to it.
func TestTheHubAuthorizesAJobAsItsKind(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = scopedPolicy(t)
	l.enrolled(t, "web1.example")
	l.enrolled(t, "web2.example")
	l.enrolled(t, "db1.example")
	alice := l.operator(t, "alice")
	ed := l.operator(t, "ed")

	refused := []transport.SubmitRequest{
		{Target: "web1.example,db1.example", TargetKind: "L"},
		{Target: "web1.example, db1.example", TargetKind: "list"},
		{Target: "web1.example or db1.example", TargetKind: "C"},
		{Target: "web1.example and not G@x:y or db1.example", TargetKind: "compound"},
		{Target: "web1.example|db1.example", TargetKind: "E"},
		{Target: "web1.example", TargetKind: "pcre"},
		{Target: "os:FreeBSD", TargetKind: "G"},
	}
	for _, req := range refused {
		req.Fun = "test.ping"
		res, err := alice.Submit(context.Background(), req)
		if err == nil {
			t.Errorf("alice ran %q as %q against %v", req.Target, req.TargetKind, res.Nodes)
		}
	}

	res, err := alice.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example,web2.example", TargetKind: "L", Fun: "test.ping",
	})
	if err != nil {
		t.Fatalf("a list wholly inside alice's grant was refused: %v", err)
	}
	if len(res.Nodes) != 2 {
		t.Errorf("the list reached %v", res.Nodes)
	}

	// An estate-wide role keeps every kind.
	res, err = ed.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example or db1.example", TargetKind: "C", Fun: "test.ping",
	})
	if err != nil {
		t.Fatalf("a `*` role was refused a compound target: %v", err)
	}
	if len(res.Nodes) != 2 {
		t.Errorf("the compound reached %v", res.Nodes)
	}

	// Killing a job is authorized as the job, kind included: alice may
	// not kill ed's list job against db1 by way of the glob reading of
	// its target string.
	listJob, err := ed.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example,db1.example", TargetKind: "L", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := alice.KillJob(context.Background(), listJob.JID); err == nil {
		t.Error("alice killed a list job that reaches db1.example")
	}
}

// A job a runner, an orchestration or a reaction dispatches on a
// principal's behalf goes through DispatchAs, which is authorized as
// the submission's kind.
func TestDispatchAsAuthorizesTheSubmissionsKind(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = scopedPolicy(t)
	l.enrolled(t, "web1.example")
	l.enrolled(t, "db1.example")

	for _, sub := range []Submission{
		{Target: "web1.example,db1.example", TargetKind: "L"},
		{Target: "web1.example or db1.example", TargetKind: "C"},
		// Node-shaped, so a check that dropped the kind would read it
		// as a glob and allow it.
		{Target: "web1.example", TargetKind: "C"},
	} {
		sub.Fun = "test.ping"
		if j, err := l.server.DispatchAs("cert:CN=alice", sub); err == nil {
			t.Errorf("DispatchAs ran %q as %q against %v", sub.Target, sub.TargetKind, j.Nodes)
		}
	}
	if _, err := l.server.DispatchAs("cert:CN=alice", Submission{
		Target: "web1.example", TargetKind: "L", Fun: "test.ping",
	}); err != nil {
		t.Errorf("a one-node list inside the grant was refused: %v", err)
	}
}

// A mine read is authorized as the kind it is resolved under, so a
// node allowed the web nodes' data cannot read db1's by asking with a
// list or a compound.
func TestAMineReadIsAuthorizedAsItsKind(t *testing.T) {
	l := newLab(t).withJobs(t).withMine(t)
	web1 := l.enrolled(t, "web1.example")
	db1 := l.enrolled(t, "db1.example")
	lb := l.enrolled(t, "lb1.example")
	defer l.connect(t, web1, "web1.example", `{"os":"FreeBSD"}`)()
	defer l.connect(t, db1, "db1.example", `{"os":"FreeBSD"}`)()
	defer l.connect(t, lb, "lb1.example", `{"os":"FreeBSD"}`)()
	publish(t, web1, "backend", `{"address":"10.0.0.1"}`, "")
	publish(t, db1, "backend", `{"address":"10.0.0.9"}`, "")
	l.server.Policy = scopedPolicy(t)

	for _, req := range []transport.MineGetRequest{
		{Target: "web1.example,db1.example", TargetKind: "L"},
		{Target: "web1.example or db1.example", TargetKind: "C"},
		{Target: "web1.example|db1.example", TargetKind: "E"},
		// `.` is any character, so this is not one node; and a check
		// that dropped the kind would read it as a glob and allow it.
		{Target: "web1.example", TargetKind: "E"},
	} {
		req.Function = "backend"
		got, err := lb.FetchMine(context.Background(), req)
		if err == nil {
			t.Errorf("lb1 read %q as %q: %v", req.Target, req.TargetKind, got.Data)
			continue
		}
		if !strings.Contains(err.Error(), "no rule") {
			t.Errorf("the refusal should be the policy's: %v", err)
		}
	}

	got, err := lb.FetchMine(context.Background(), transport.MineGetRequest{
		Target: "web1.example", TargetKind: "L", Function: "backend",
	})
	if err != nil {
		t.Fatalf("a list inside the grant was refused: %v", err)
	}
	if _, ok := got.Data["web1.example"]; !ok || len(got.Data) != 1 {
		t.Errorf("lb1 read %v", got.Data)
	}
}
