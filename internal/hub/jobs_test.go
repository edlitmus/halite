package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// withJobs gives the lab a job cache and a node cache, as `serve` does.
func (l *lab) withJobs(t *testing.T) *lab {
	t.Helper()
	dir := t.TempDir()
	jobs, err := job.OpenCache(dir + "/jobs")
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := OpenNodeCache(dir + "/nodes")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Jobs = jobs
	l.server.Nodes = nodes
	l.server.Policy = labPolicy(t)
	return l
}

// labPolicy grants the lab's operator everything, including the
// functions a wildcard never covers. It is written out rather than
// assumed, because the tests below check that the absence of one
// authorizes nothing.
func labPolicy(t *testing.T) *policy.Policy {
	t.Helper()
	loaded, _, err := policy.Load([]byte(`
roles:
  administrator:
    - target: '*'
      functions: ['*', 'cmd.run']
    - runners: ['*']
bindings:
  - principal: 'cert:CN=ed'
    roles: ['administrator']
`), "lab-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	loaded.ArbitraryCode = map[string]bool{"cmd.run": true, "cmd.script": true, "module.run": true}
	return loaded
}

// operator issues an operator certificate and returns a client holding
// it.
func (l *lab) operator(t *testing.T, name string) *transport.Client {
	t.Helper()
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	der, err := l.ca.IssueOperator(key, name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pki.EncodeCert(der), keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &transport.Client{HubURL: l.url, CA: l.ca.Cert, Cert: &pair, Timeout: 5 * time.Second}
}

// connect opens a node's subscribe stream and answers jobs with a
// canned return, which is what a node does minus the executing.
func (l *lab) connect(t *testing.T, client *transport.Client, nodeID string, grains string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	// stopped closes when the goroutine has finished, including any
	// return it was in the middle of posting. Without waiting for it, a
	// test's temporary directory is removed while a return is being
	// written into it, which fails the test that has already passed.
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Subscribe(ctx, transport.SubscribeRequest{
			NodeID: nodeID,
			Grains: json.RawMessage(grains),
		}, func(msg transport.Message) error {
			switch msg.T {
			case transport.MsgPing:
				select {
				case <-ready:
				default:
					close(ready)
				}
			case transport.MsgJob:
				// ctx, not Background: a return in flight when the
				// test ends must not outlive it.
				client.Return(ctx, job.Return{
					JID:     job.ID(msg.JID),
					NodeID:  nodeID,
					Fun:     msg.Fun,
					Success: true,
					Return:  json.RawMessage(`{"answered":"` + nodeID + `"}`),
					Schema:  job.ReturnSchema,
				})
			}
			return nil
		})
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatalf("%s never connected", nodeID)
	}
	return func() {
		cancel()
		<-stopped
	}
}

func TestAJobReachesTheFleetAndTheReturnsComeBack(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	web2 := l.enrolled(t, "web2.example")
	defer l.connect(t, web1, "web1.example", `{"os":"FreeBSD"}`)()
	defer l.connect(t, web2, "web2.example", `{"os":"Linux"}`)()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 2 {
		t.Fatalf("matched %v", res.Nodes)
	}
	if len(res.Absent) != 0 {
		t.Errorf("both nodes are connected, and %v were reported absent", res.Absent)
	}

	status := waitForReturns(t, op, res.JID, 2)
	if len(status.Missing) != 0 {
		t.Errorf("missing = %v", status.Missing)
	}

	// The job was recorded with its expected respondents before it was
	// delivered, which is what makes a missing return detectable.
	j, err := l.server.Jobs.Get(job.ID(res.JID))
	if err != nil {
		t.Fatal(err)
	}
	if j.Submitter != "cert:CN=ed" {
		t.Errorf("the job records its submitter as %q", j.Submitter)
	}
	if j.Nonce == "" || j.Expires.IsZero() {
		t.Error("a job needs a nonce and an expiry, per SPEC 6.3")
	}
	if len(j.Nodes) != 2 {
		t.Errorf("the recorded node set is %v", j.Nodes)
	}
}

// Targeting on a grain reads what the node reported when it connected.
func TestAGrainTargetSelectsPartOfTheFleet(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	web2 := l.enrolled(t, "web2.example")
	defer l.connect(t, web1, "web1.example", `{"os":"FreeBSD"}`)()
	defer l.connect(t, web2, "web2.example", `{"os":"Linux"}`)()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "os:FreeBSD", TargetKind: "G", Fun: "test.ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || res.Nodes[0] != "web1.example" {
		t.Fatalf("a grain target matched %v", res.Nodes)
	}
	status := waitForReturns(t, op, res.JID, 1)
	if len(status.Returns) != 1 {
		t.Errorf("%d returns for a one-node job", len(status.Returns))
	}
}

// A node's certificate authenticates a node. If it could submit a job,
// one compromised host would drive the fleet.
func TestANodeCannotSubmitAJob(t *testing.T) {
	l := newLab(t).withJobs(t)
	node := l.enrolled(t, "web1.example")

	_, err := node.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"})
	if err == nil {
		t.Fatal("a node submitted a job")
	}
	if !strings.Contains(err.Error(), "operator") {
		t.Errorf("the refusal should say what was wrong: %v", err)
	}
}

// A node may not file a return on another node's behalf, however it
// fills in the body.
func TestANodeCannotReturnForAnother(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	l.enrolled(t, "web2.example")
	defer l.connect(t, web1, "web1.example", `{}`)()

	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"})
	if err != nil {
		t.Fatal(err)
	}
	err = web1.Return(context.Background(), job.Return{
		JID: job.ID(res.JID), NodeID: "web2.example", Fun: "test.ping", Success: true,
	})
	if err == nil {
		t.Fatal("web1 filed a return as web2")
	}
	if !strings.Contains(err.Error(), "web1.example") {
		t.Errorf("the refusal should name the certificate's identity: %v", err)
	}
}

// SPEC 9.5: `require` fails outright rather than reporting success for
// a fleet that was not all there.
func TestTheRequireOfflinePolicyRefusesAnAbsentNode(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	l.enrolled(t, "web2.example") // enrolled and never connected
	defer l.connect(t, web1, "web1.example", `{}`)()

	op := l.operator(t, "ed")
	_, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*", Fun: "test.ping", Offline: "require",
	})
	if err == nil {
		t.Fatal("require accepted a job with an absent node")
	}
	if !strings.Contains(err.Error(), "web2.example") {
		t.Errorf("the refusal should name who was missing: %v", err)
	}

	// The default reports the absence and dispatches to the rest.
	res, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Absent) != 1 || res.Absent[0] != "web2.example" {
		t.Errorf("absent = %v", res.Absent)
	}
	status := waitForReturns(t, op, res.JID, 1)
	if len(status.Missing) != 1 || status.Missing[0] != "web2.example" {
		t.Errorf("missing = %v", status.Missing)
	}
}

// A revoked node is not part of the estate, so a job does not go to it
// and it is not counted as missing either.
func TestARevokedNodeIsNotATarget(t *testing.T) {
	l := newLab(t).withJobs(t)
	web1 := l.enrolled(t, "web1.example")
	l.enrolled(t, "web2.example")
	defer l.connect(t, web1, "web1.example", `{}`)()

	if err := l.server.Revoke("web2.example", "decommissioned"); err != nil {
		t.Fatal(err)
	}
	op := l.operator(t, "ed")
	res, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Nodes) != 1 || res.Nodes[0] != "web1.example" {
		t.Errorf("a revoked node was targeted: %v", res.Nodes)
	}
}

// SPEC 23.5: an empty policy grants nothing, and a missing one is the
// same. A hub that authorized everything when its policy file was
// absent would be a hub whose security depends on a file existing.
func TestAHubWithNoPolicyAuthorizesNothing(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.server.Policy = nil
	l.enrolled(t, "web1.example")

	op := l.operator(t, "ed")
	_, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"})
	if err == nil {
		t.Fatal("a hub with no policy ran a job")
	}
	if !strings.Contains(err.Error(), "policy") {
		t.Errorf("the refusal should say what is missing: %v", err)
	}
}

// An operator the policy does not bind is authenticated and not
// authorized, and the difference is the point of having a policy.
func TestAnUnboundOperatorIsRefused(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.enrolled(t, "web1.example")

	stranger := l.operator(t, "mallory")
	_, err := stranger.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"})
	if err == nil {
		t.Fatal("an unbound operator ran a job")
	}
	if !strings.Contains(err.Error(), "mallory") {
		t.Errorf("the refusal should name the principal: %v", err)
	}
}

// Salt's `.*` grants everything, and everybody's Salt ACL grants `.*`.
func TestAWildcardDoesNotGrantAShell(t *testing.T) {
	l := newLab(t).withJobs(t)
	l.enrolled(t, "web1.example")
	// The lab's policy names cmd.run, so take that away and leave the
	// wildcard.
	l.server.Policy.Roles["administrator"][0].Functions = []string{"*"}

	op := l.operator(t, "ed")
	_, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "cmd.run"})
	if err == nil {
		t.Fatal("a wildcard granted cmd.run")
	}
	if !strings.Contains(err.Error(), "arbitrary code") {
		t.Errorf("the refusal should say why: %v", err)
	}
	// And the wildcard still grants an ordinary function.
	if _, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"}); err != nil {
		t.Errorf("a wildcard should grant test.ping: %v", err)
	}
}

func waitForReturns(t *testing.T, op *transport.Client, jid string, want int) *transport.JobStatus {
	t.Helper()
	// Generous, because `go test ./...` runs packages in parallel and a
	// deadline tight enough to pass on an idle machine is a test that
	// fails when the machine is busy. Nothing here waits for the
	// deadline in the ordinary case.
	deadline := time.Now().Add(30 * time.Second)
	for {
		status, err := op.JobStatus(context.Background(), jid)
		if err != nil {
			t.Fatal(err)
		}
		if len(status.Returns) >= want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d returns arrived for %s", len(status.Returns), want, jid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A structured argument reaches the node as the structure it was.
//
// It did not. The operator's command line parses `data='{"a":1}'` into
// the ordered model, and the transport marshals its request bodies with
// `encoding/json`, which wrote the model's position record instead of
// its contents: every mapping argument arrived at the hub as
// `{"Pos":{"File":"","Line":0,"Col":0}}`. The 64-bit integer is the
// other half of SPEC 6.4's promise, and the node's decoder was turning
// every number in a job's arguments into a float64.
func TestAStructuredArgumentReachesTheNodeIntact(t *testing.T) {
	l := newLab(t).withJobs(t)
	client := l.enrolled(t, "web1.example")

	const exact int64 = 9007199254740993
	got := make(chan transport.Message, 1)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Subscribe(ctx, transport.SubscribeRequest{NodeID: "web1.example"},
			func(msg transport.Message) error {
				switch msg.T {
				case transport.MsgPing:
					select {
					case <-ready:
					default:
						close(ready)
					}
				case transport.MsgJob:
					select {
					case got <- msg:
					default:
					}
				}
				return nil
			})
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("the node never connected")
	}
	defer func() { cancel(); <-stopped }()

	op := l.operator(t, "ed")
	_, err := op.Submit(context.Background(), transport.SubmitRequest{
		Target: "*",
		Fun:    "event.send",
		Kwarg: map[string]any{
			"tag":  "deploy/done",
			"data": value.MapOf("version", "1.2", "build", exact),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var msg transport.Message
	select {
	case msg = <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("the job never reached the node")
	}

	data, ok := value.FromJSON(msg.Kwarg["data"]).(*value.Map)
	if !ok {
		t.Fatalf("the mapping argument arrived as %T: %v", msg.Kwarg["data"], msg.Kwarg["data"])
	}
	if v, _ := data.Get("version"); v != "1.2" {
		t.Errorf("version arrived as %v", v)
	}
	if b, _ := data.Get("build"); b != exact {
		t.Errorf("the 64-bit integer arrived as %v (%T)", b, b)
	}
}
