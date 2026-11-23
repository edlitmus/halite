package hub

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/jobsign"
	"github.com/edlitmus/halite/internal/orchplan"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/treesign"
	"github.com/edlitmus/halite/internal/value"
)

// Issue 765: a plan of pre-signed jobs. These tests sign plans with the
// same code `halite-hub orch plan` uses (NewPlan), run them on the hub's
// real compiler and dispatcher, and read what reached the node. The node
// here is a stand-in that checks each signature the way a node does, from
// the message; the real node is cmd/halite-node's orch plan binary test.

const planDeploy = `
drain:
  salt.function:
    - name: lb.drain
    - tgt: 'web*'
    - arg: [pool-a]
    - kwarg:
        grace: 30
        modes: {soft: true, order: [b, a]}

deploy_web:
  salt.state:
    - tgt: 'web*'
    - sls: [webserver]
    - pillar:
        version: {{ pillar.get('version', '0') }}
        zeta: 1
        alpha: 2
    - require:
      - salt: drain

announce:
  salt.runner:
    - name: event.send
    - arg: [halite/audit/deployed]
    - require:
      - salt: deploy_web
`

// planSigner holds the key a test signs plans with.
type planSigner struct {
	key  *ecdsa.PrivateKey
	keys []jobsign.SignerKey
}

func newPlanSigner(t *testing.T) *planSigner {
	t.Helper()
	key, err := jobsign.GenerateKey("ecdsa-p256")
	if err != nil {
		t.Fatal(err)
	}
	line, err := jobsign.FormatSignerKey("ops", &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := jobsign.ParseSignerKey(line)
	if err != nil {
		t.Fatal(err)
	}
	return &planSigner{key: key, keys: []jobsign.SignerKey{trusted}}
}

// plan compiles and signs a plan from loader, encodes it and parses it
// back, as the file a hub is given.
func (p *planSigner) plan(t *testing.T, loader *fileserver.Roots, opts PlanOptions) (*orchplan.Plan, string) {
	t.Helper()
	if opts.Created.IsZero() {
		opts.Created = time.Now()
	}
	if opts.TTL == 0 {
		opts.TTL = time.Hour
	}
	plan, payloads, err := NewPlan(loader, opts)
	if err != nil {
		t.Fatal(err)
	}
	for i, payload := range payloads {
		sig, err := jobsign.Sign(p.key, payload)
		if err != nil {
			t.Fatal(err)
		}
		plan.Steps[i].Signature = sig
	}
	raw, err := orchplan.Encode(plan)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := orchplan.Parse(raw)
	if err != nil {
		t.Fatalf("the plan tool wrote a plan the hub cannot read: %v\n%s", err, raw)
	}
	return parsed, orchplan.Digest(raw)
}

// signingNode records every job that reaches it, and whether the job's
// signature verifies, rebuilt from the message the way a node rebuilds it
// (cmd/halite-node acceptJob).
type signingNode struct {
	mu       sync.Mutex
	keys     []jobsign.SignerKey
	received []transport.Message
	verified map[string]bool
}

func (n *signingNode) record(msg transport.Message) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.received = append(n.received, msg)
	if n.verified == nil {
		n.verified = map[string]bool{}
	}
	j := &job.Job{JID: job.ID(msg.JID), Fun: msg.Fun, Arg: msg.Arg, Kwarg: msg.Kwarg, Env: msg.Env,
		Target: msg.Target, TargetKind: msg.TargetKind, Signature: msg.Signature}
	if expires, err := time.Parse(time.RFC3339Nano, msg.Expires); err == nil {
		j.Expires = expires
	}
	_, err := jobsign.Verify(n.keys, job.SigningPayload(j), msg.Signature)
	n.verified[msg.JID] = err == nil
	return err == nil
}

func (n *signingNode) jobs() []transport.Message {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]transport.Message(nil), n.received...)
}

// connect puts the node on the hub. fail names functions it reports as
// failed; a job whose signature does not verify always fails, as a node
// that requires signatures refuses it.
func (n *signingNode) connect(t *testing.T, l *lab, nodeID string, fail func(fun string) bool) func() {
	t.Helper()
	client := l.enrolled(t, nodeID)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Subscribe(ctx, transport.SubscribeRequest{NodeID: nodeID},
			func(msg transport.Message) error {
				switch msg.T {
				case transport.MsgPing:
					select {
					case <-ready:
					default:
						close(ready)
					}
				case transport.MsgJob:
					good := n.record(msg) && (fail == nil || !fail(msg.Fun))
					client.Return(ctx, job.Return{
						JID: job.ID(msg.JID), NodeID: nodeID, Fun: msg.Fun, Success: good,
						RetCode: map[bool]int{true: 0, false: 1}[good],
						Return:  json.RawMessage(`{}`), Schema: job.ReturnSchema,
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
	return func() { cancel(); <-stopped }
}

func (l *lab) rewrite(t *testing.T, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(l.root, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func versionPillar() *value.Map { return value.MapOf("version", "1.4.2") }

// A planned run sends every fleet step as the plan's signed job: the
// plan's identifier, its signature, and a payload the signature verifies
// over once it has crossed the wire. The runner step runs on the hub and
// needs none.
func TestAPlannedOrchestrationSendsEachFleetStepAsItsSignedJob(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": planDeploy})
	signer := newPlanSigner(t)
	node := &signingNode{keys: signer.keys}
	defer node.connect(t, l, "web1.example", nil)()
	plan, digest := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"deploy"}, Pillar: versionPillar()})

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: versionPillar(),
		Plan: plan, PlanDigest: digest,
	})

	if err != nil {
		t.Fatal(err)
	}
	if run.State != OrchComplete {
		t.Fatalf("the run is %s: %+v", run.State, run.Steps)
	}
	if run.PlanDigest != digest {
		t.Errorf("the record holds plan %q, want %q", run.PlanDigest, digest)
	}
	got := node.jobs()
	if len(got) != 2 {
		t.Fatalf("the node received %d jobs, want the two fleet steps", len(got))
	}
	for _, msg := range got {
		entry := planEntryFor(t, plan, msg.JID)
		if msg.Signature != entry.Signature {
			t.Errorf("%s arrived with a signature other than the plan's", entry.ID)
		}
		if !node.verified[msg.JID] {
			t.Errorf("%s's signature does not verify over the job that arrived: %+v", entry.ID, msg)
		}
		if step := stepByID(t, run, entry.ID); !step.Signed || step.JID != msg.JID {
			t.Errorf("the record says %s signed=%v job %s, want signed and %s", entry.ID, step.Signed, step.JID, msg.JID)
		}
	}
	if s := stepByID(t, run, "announce"); s.Signed || !s.Succeeded() {
		t.Errorf("the runner step: signed=%v succeeded=%v %s", s.Signed, s.Succeeded(), s.Comment)
	}
}

func planEntryFor(t *testing.T, plan *orchplan.Plan, jid string) orchplan.Step {
	t.Helper()
	for _, s := range plan.Steps {
		if s.Job.JID == jid {
			return s
		}
	}
	t.Fatalf("job %s is not one the plan signed", jid)
	return orchplan.Step{}
}

// The two paths that must agree: the job the plan tool signs for a step
// and the job the hub's step module actually dispatches for it, unsigned,
// are the same payload once the identifier and expiry are set aside. This
// is what decides whether the design is usable at all; a difference here
// would refuse every planned run. It runs over this file's orchestration,
// whose keyword arguments are typed out of order, and over the handbook's
// chapter 21 rolling update.
func TestThePlanToolSignsTheJobTheHubWouldSendForEveryStep(t *testing.T) {
	cases := map[string]struct {
		files  map[string]string
		sls    string
		pillar *value.Map
	}{
		"this file's deploy": {map[string]string{"deploy.sls": planDeploy}, "deploy", versionPillar()},
		"the handbook's rolling update": {map[string]string{
			"rolling-update.sls": exampleFile(t, "orchestration/rolling-update.sls")}, "rolling-update", pillarOf(t, "1.2")},
		"a highstate and an sls step": {map[string]string{"hs.sls": `
everything:
  salt.highstate:
    - tgt: 'G@os:FreeBSD'
    - tgt_type: compound
named:
  salt.sls:
    - tgt: web1.example,web2.example
    - tgt_type: list
    - sls: [a, b]
    - pillarenv: prod
    - saltenv: base
`}, "hs", nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			l := orchLab(t, tc.files)
			signer := newPlanSigner(t)
			node := &signingNode{keys: signer.keys}
			defer node.connect(t, l, "web1.example", nil)()
			plan, _ := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{tc.sls}, Pillar: tc.pillar})

			run, err := l.server.Orchestrate(context.Background(), OrchRequest{
				Principal: "cert:CN=ed", SLS: []string{tc.sls}, Pillar: tc.pillar,
			})

			if err != nil {
				t.Fatal(err)
			}
			sent := 0
			for _, step := range run.Steps {
				if step.JID == "" || !isFleetStep(step.Fun) {
					continue
				}
				sent++
				entry, ok := plan.Step(step.ID)
				if !ok {
					t.Fatalf("the hub sent a job for %s and the plan signs none", step.ID)
				}
				dispatched, err := l.server.Jobs.Get(job.ID(step.JID))
				if err != nil {
					t.Fatal(err)
				}
				if field, planned, compiled, differ := differentField(dispatched, entry.Job); differ {
					t.Errorf("%s: the plan signs %s %s and the hub sent %s", step.ID, field, planned, compiled)
				}
			}
			// Every step the hub sent is held above; a rollback that
			// nothing triggered is signed and never sent.
			if sent == 0 {
				t.Error("the hub sent no fleet job, so nothing was compared")
			}
		})
	}
}

// Every way a plan can disagree with this hub's run refuses the whole run
// before anything is dispatched, naming what differs: a node that never
// receives a job is the assertion.
func TestAPlanThatDisagreesWithTheRunIsRefusedBeforeAnyStep(t *testing.T) {
	cases := []struct {
		name string
		// change alters the hub's side after the plan is made.
		change func(t *testing.T, l *lab, req *OrchRequest)
		want   string
	}{
		{"an argument changed on the hub", func(t *testing.T, l *lab, req *OrchRequest) {
			l.rewrite(t, "deploy.sls", strings.Replace(planDeploy, "pool-a", "pool-b", 1))
		}, `drain is signed for arg ["pool-a"]`},
		{"a target widened on the hub", func(t *testing.T, l *lab, req *OrchRequest) {
			l.rewrite(t, "deploy.sls", strings.Replace(planDeploy, "tgt: 'web*'\n    - sls", "tgt: '*'\n    - sls", 1))
		}, `deploy_web is signed for target "web*"`},
		{"a target kind changed on the hub", func(t *testing.T, l *lab, req *OrchRequest) {
			l.rewrite(t, "deploy.sls", strings.Replace(planDeploy, "tgt: 'web*'\n    - arg", "tgt: 'web*'\n    - tgt_type: pcre\n    - arg", 1))
		}, `drain is signed for target_kind "glob"`},
		{"a function changed on the hub", func(t *testing.T, l *lab, req *OrchRequest) {
			l.rewrite(t, "deploy.sls", strings.Replace(planDeploy, "lb.drain", "cmd.run", 1))
		}, `drain is signed for fun "lb.drain"`},
		{"a keyword argument changed on the hub", func(t *testing.T, l *lab, req *OrchRequest) {
			l.rewrite(t, "deploy.sls", strings.Replace(planDeploy, "grace: 30", "grace: 31", 1))
		}, "drain is signed for kwarg"},
		{"a step added on the hub", func(t *testing.T, l *lab, req *OrchRequest) {
			l.rewrite(t, "deploy.sls", planDeploy+"\nextra:\n  salt.function:\n    - name: test.ping\n    - tgt: '*'\n")
		}, "step extra sends a job and is not in the plan"},
		{"a step removed on the hub", func(t *testing.T, l *lab, req *OrchRequest) {
			l.rewrite(t, "deploy.sls", strings.Replace(planDeploy[strings.Index(planDeploy, "deploy_web:"):],
				"    - require:\n      - salt: drain\n", "", 1))
		}, "the plan signs step drain and this run does not send it"},
		{"a runner step changed on the hub", func(t *testing.T, l *lab, req *OrchRequest) {
			l.rewrite(t, "deploy.sls", strings.Replace(planDeploy, "event.send", "jobs.list_jobs", 1))
		}, "the plan lists hub steps"},
		{"another orchestration with the same steps", func(t *testing.T, l *lab, req *OrchRequest) {
			l.rewrite(t, "copy.sls", planDeploy)
			req.SLS = []string{"copy"}
		}, "the plan is for deploy and this run is copy"},
		{"a different pillar override", func(t *testing.T, l *lab, req *OrchRequest) {
			req.Pillar = value.MapOf("version", "1.4.3")
		}, "different pillar override"},
		{"a real plan run as a test", func(t *testing.T, l *lab, req *OrchRequest) {
			req.Test = true
		}, "made for a real run and this run is a test"},
		{"another environment", func(t *testing.T, l *lab, req *OrchRequest) {
			req.Env = "prod"
		}, ""},
		{"a resume given a plan that is not a resume plan", func(t *testing.T, l *lab, req *OrchRequest) {
			req.ResumeOf, req.ResumeFrom = "20261122T181000123456", "deploy_web"
		}, ""},
		{"an expired plan", func(t *testing.T, l *lab, req *OrchRequest) {
			l.server.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
		}, "the plan expired at"},
		{"a step whose signed job this hub already sent", func(t *testing.T, l *lab, req *OrchRequest) {
			entry, _ := req.Plan.Step("drain")
			if err := l.server.Jobs.Put(&job.Job{JID: job.ID(entry.Job.JID), Fun: "test.ping",
				Created: time.Now(), Expires: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
		}, "has already been sent by this hub"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := orchLab(t, map[string]string{"deploy.sls": planDeploy})
			signer := newPlanSigner(t)
			node := &signingNode{keys: signer.keys}
			defer node.connect(t, l, "web1.example", nil)()
			plan, digest := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"deploy"}, Pillar: versionPillar()})
			req := OrchRequest{Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: versionPillar(),
				Plan: plan, PlanDigest: digest}
			tc.change(t, l, &req)

			_, err := l.server.Orchestrate(context.Background(), req)

			if err == nil {
				t.Fatal("the run was not refused")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal does not say %q: %v", tc.want, err)
			}
			if got := node.jobs(); len(got) != 0 {
				t.Errorf("%d job(s) were dispatched before the refusal: %+v", len(got), got)
			}
		})
	}
}

// The environment and resume refusals name what differs; they have their
// own test because the run is refused by an earlier check when the
// environment has no tree.
func TestAPlanForAnotherResumeOrNoneIsRefusedSayingSo(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": planDeploy})
	signer := newPlanSigner(t)
	plan, digest := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"deploy"}, Pillar: versionPillar()})
	req := OrchRequest{SLS: []string{"deploy"}, Env: "base", Pillar: versionPillar(), Plan: plan, PlanDigest: digest,
		ResumeOf: "20261122T181000123456", ResumeFrom: "deploy_web"}

	err := l.server.checkPlan(req, "base", CompilePlanned(l.server.Files, req.SLS, "base", req.Pillar, false), nil)

	if err == nil || !strings.Contains(err.Error(), "a resume always needs a new plan") {
		t.Errorf("a resume given a fresh-run plan: %v", err)
	}
	req.ResumeOf, req.ResumeFrom = "", ""
	req.Env = "prod"
	if err := l.server.checkPlan(req, "prod", CompilePlanned(l.server.Files, req.SLS, "base", req.Pillar, false), nil); err == nil ||
		!strings.Contains(err.Error(), `the plan is for environment "base"`) {
		t.Errorf("a plan for another environment: %v", err)
	}
}

// A planned orchestration may not read the hub's configuration: the plan
// tool has none to give it, so the off-hub compile refuses a template that
// reads `opts`, saying why, and the hub's compile of the same plan does
// the same rather than reading its own.
func TestAPlannedOrchestrationMayNotReadHubConfiguration(t *testing.T) {
	for _, expr := range []string{"opts.file_roots", "opts['state_dir']", "__opts__.get('x')"} {
		t.Run(expr, func(t *testing.T) {
			l := orchLab(t, map[string]string{"cfg.sls": "step:\n  salt.function:\n    - name: test.echo\n" +
				"    - tgt: '*'\n    - arg: ['{{ " + expr + " }}']\n"})

			_, _, err := NewPlan(l.server.Files, PlanOptions{SLS: []string{"cfg"}, Created: time.Now(), TTL: time.Hour})

			if err == nil || !strings.Contains(err.Error(), "may not read the hub's configuration") {
				t.Errorf("the plan compiled, or failed without saying why: %v", err)
			}
		})
	}
}

// Things a node would refuse on every signed job are refused when the plan
// is made: a nodegroup, which a node cannot evaluate about itself; and
// `retry`, whose second attempt would be the same signed job again.
func TestAStepNoNodeCouldAcceptSignedIsRefusedAtPlanTime(t *testing.T) {
	cases := map[string]struct{ sls, want string }{
		"a nodegroup target": {"s:\n  salt.function:\n    - name: test.ping\n    - tgt: web\n    - tgt_type: nodegroup\n",
			"a node cannot check"},
		"a nodegroup inside a compound": {"s:\n  salt.function:\n    - name: test.ping\n    - tgt: 'N@web and G@os:FreeBSD'\n    - tgt_type: compound\n",
			"a node cannot check"},
		"retry on a fleet step": {"s:\n  salt.function:\n    - name: test.ping\n    - tgt: '*'\n    - retry: {attempts: 2}\n",
			"`retry` cannot be planned"},
		"a step with no target": {"s:\n  salt.function:\n    - name: test.ping\n    - tgt: ''\n", "needs `tgt`"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			l := orchLab(t, map[string]string{"o.sls": tc.sls})

			_, _, err := NewPlan(l.server.Files, PlanOptions{SLS: []string{"o"}, Created: time.Now(), TTL: time.Hour})

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want a refusal saying %q, got %v", tc.want, err)
			}
		})
	}
}

// `run_parallel` is outside the signature (SPEC 9.6, issue 765 question
// 4): a plan never carries it, the step's signed payload is the same with
// or without it, and the hub still sends the flag, which a node ignores on
// a signed job (cmd/halite-node TestAJobsSchedulingFollowsTheFunction...,
// "--parallel on a signed job is ignored").
func TestRunParallelIsOutsideAPlansSignature(t *testing.T) {
	without := "warm:\n  salt.function:\n    - name: test.ping\n    - tgt: '*'\n"
	with := without + "    - run_parallel: true\n"
	l := orchLab(t, map[string]string{"o.sls": without})
	signer := newPlanSigner(t)
	node := &signingNode{keys: signer.keys}
	defer node.connect(t, l, "web1.example", nil)()
	plan, digest := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"o"}})
	raw, _ := orchplan.Encode(plan)
	if strings.Contains(string(raw), "parallel") {
		t.Fatalf("the plan carries a parallel field:\n%s", raw)
	}
	// The hub's tree asks for it after the plan was signed without it.
	l.rewrite(t, "o.sls", with)

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"o"}, Plan: plan, PlanDigest: digest})

	if err != nil {
		t.Fatalf("a hub setting run_parallel on a signed step refused the plan: %v", err)
	}
	if run.State != OrchComplete {
		t.Fatalf("the run is %s: %+v", run.State, run.Steps)
	}
	got := node.jobs()
	if len(got) != 1 || !got[0].Parallel || got[0].Signature == "" || !node.verified[got[0].JID] {
		t.Fatalf("want one signed job carrying the flag, verifying regardless: %+v", got)
	}
}

// A test plan signs the state steps with `test` inside the signature and
// leaves the function steps out, as a test run dispatches none; it runs
// under --test and not for real.
func TestATestPlanRunsOnlyAsATestRun(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": planDeploy})
	signer := newPlanSigner(t)
	node := &signingNode{keys: signer.keys}
	defer node.connect(t, l, "web1.example", nil)()
	plan, digest := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"deploy"}, Pillar: versionPillar(), Test: true})
	if _, ok := plan.Step("drain"); ok || len(plan.Steps) != 1 {
		t.Fatalf("a test plan signed %d steps, want only the state step", len(plan.Steps))
	}
	if entry, _ := plan.Step("deploy_web"); entry.Job.Kwarg["test"] != true {
		t.Fatalf("the state step is not signed with test set: %+v", entry.Job.Kwarg)
	}
	real := OrchRequest{Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: versionPillar(), Plan: plan, PlanDigest: digest}

	if _, err := l.server.Orchestrate(context.Background(), real); err == nil ||
		!strings.Contains(err.Error(), "made for a test run and this run is real") {
		t.Fatalf("a test plan run for real: %v", err)
	}
	dry := real
	dry.Test = true
	run, err := l.server.Orchestrate(context.Background(), dry)

	if err != nil {
		t.Fatal(err)
	}
	got := node.jobs()
	if len(got) != 1 || got[0].Kwarg["test"] != true || !node.verified[got[0].JID] {
		t.Fatalf("want one signed dry state run: %+v", got)
	}
	if s := stepByID(t, run, "drain"); s.JID != "" {
		t.Errorf("the function step was dispatched in a test run: %+v", s)
	}
}

// A resume always needs a new plan. A step fails; the cause is fixed; a
// resume plan signs the step named and the ones after it, and the resumed
// run sends those and carries the earlier ones forward. The original plan
// cannot be used: its failed step's job is spent.
func TestAResumeRunsOnAFreshPlanAndNotOnTheOriginal(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": planDeploy})
	signer := newPlanSigner(t)
	node := &signingNode{keys: signer.keys}
	failing := true
	var mu sync.Mutex
	defer node.connect(t, l, "web1.example", func(fun string) bool {
		mu.Lock()
		defer mu.Unlock()
		return failing && fun == "state.apply"
	})()
	plan, digest := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"deploy"}, Pillar: versionPillar()})
	first, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: versionPillar(), Plan: plan, PlanDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != OrchFailed {
		t.Fatalf("the first run is %s, want failed", first.State)
	}
	mu.Lock()
	failing = false
	mu.Unlock()
	resumeOf := string(first.JID)

	_, err = l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: versionPillar(), Plan: plan, PlanDigest: digest,
		ResumeOf: first.JID, ResumeFrom: "deploy_web"})
	if err == nil || !strings.Contains(err.Error(), "a resume always needs a new plan") {
		t.Fatalf("a resume on the original plan: %v", err)
	}

	fresh, freshDigest := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"deploy"}, Pillar: versionPillar(),
		ResumeOf: resumeOf, ResumeFrom: "deploy_web"})
	if _, ok := fresh.Step("drain"); ok {
		t.Fatal("the resume plan signs a step before the one it resumes from")
	}
	before := len(node.jobs())
	resumed, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: versionPillar(), Plan: fresh, PlanDigest: freshDigest,
		ResumeOf: first.JID, ResumeFrom: "deploy_web"})

	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != OrchComplete {
		t.Fatalf("the resumed run is %s: %+v", resumed.State, resumed.Steps)
	}
	sent := node.jobs()[before:]
	if len(sent) != 1 || sent[0].Fun != "state.apply" || !node.verified[sent[0].JID] {
		t.Fatalf("the resume sent %+v, want only the signed state step", sent)
	}
	if s := stepByID(t, resumed, "drain"); s.JID != "" && s.JID == sent[0].JID {
		t.Errorf("the carried step was sent again")
	}
}

// A run whose record holds no steps -- what a hub stopped mid-run leaves
// today, issue 802 -- carries nothing forward, so a resume of it would send
// every earlier step again. A resume plan does not sign those, and the
// pre-flight refuses the run naming the first of them, rather than
// sending anything.
func TestAResumeOfARunThatRecordedNoStepsIsRefusedNamingThem(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": planDeploy})
	signer := newPlanSigner(t)
	node := &signingNode{keys: signer.keys}
	defer node.connect(t, l, "web1.example", nil)()
	interrupted := &OrchRun{JID: "20261122T181000123456", SLS: []string{"deploy"}, Env: "base",
		Principal: "cert:CN=ed", Started: time.Now(), State: OrchRunning, PillarDigest: pillarDigest(versionPillar())}
	if err := l.server.Orch.Put(interrupted); err != nil {
		t.Fatal(err)
	}
	fresh, digest := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"deploy"}, Pillar: versionPillar(),
		ResumeOf: string(interrupted.JID), ResumeFrom: "deploy_web"})

	_, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: versionPillar(), Plan: fresh, PlanDigest: digest,
		ResumeOf: interrupted.JID, ResumeFrom: "deploy_web"})

	if err == nil || !strings.Contains(err.Error(), "step drain runs in this resume and is not in the plan") {
		t.Fatalf("want a refusal naming drain: %v", err)
	}
	if got := node.jobs(); len(got) != 0 {
		t.Errorf("%d job(s) were dispatched: %+v", len(got), got)
	}
}

// A plan records the issue 764 revision of the checkout it was compiled
// from, and a hub serving another revision -- or none -- refuses it.
func TestAPlanBoundToATreeRevisionRefusesAnother(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": planDeploy, treesign.ManifestPath: `{"revision":"one"}`})
	signer := newPlanSigner(t)
	node := &signingNode{keys: signer.keys}
	defer node.connect(t, l, "web1.example", nil)()
	served, err := l.server.treeRevision("base")
	if err != nil || served == "" {
		t.Fatalf("the hub reads no revision: %q %v", served, err)
	}
	plan, digest := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"deploy"}, Pillar: versionPillar(),
		TreeRevision: served})
	req := OrchRequest{Principal: "cert:CN=ed", SLS: []string{"deploy"}, Pillar: versionPillar(), Plan: plan, PlanDigest: digest}
	l.rewrite(t, treesign.ManifestPath, `{"revision":"two"}`)

	_, err = l.server.Orchestrate(context.Background(), req)

	if err == nil || !strings.Contains(err.Error(), "signed tree revision "+served) {
		t.Fatalf("want a refusal naming the plan's revision: %v", err)
	}
	if err := os.Remove(filepath.Join(l.root, treesign.ManifestPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.server.Orchestrate(context.Background(), req); err == nil || !strings.Contains(err.Error(), "with no manifest") {
		t.Fatalf("want a refusal for a hub serving no manifest: %v", err)
	}
	if got := node.jobs(); len(got) != 0 {
		t.Errorf("%d job(s) were dispatched: %+v", len(got), got)
	}
}

// The check at dispatch, behind the pre-flight: a step whose job differs
// from its plan entry when it is about to be sent is not sent.
func TestASignedStepThatDiffersAtDispatchIsNotSent(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": planDeploy})
	signer := newPlanSigner(t)
	plan, _ := signer.plan(t, l.server.Files, PlanOptions{SLS: []string{"deploy"}, Pillar: versionPillar()})
	o := &orchRunner{server: l.server, plan: plan}
	entry, _ := plan.Step("drain")
	sub := Submission{Target: entry.Job.Target, Fun: entry.Job.Fun, Arg: entry.Job.Arg, Kwarg: entry.Job.Kwarg, Env: "base"}

	if _, err := o.signedStep("drain", sub); err != nil {
		t.Fatalf("the planned job itself was refused: %v", err)
	}
	sub.Target = "*"
	if _, err := o.signedStep("drain", sub); err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Errorf("a changed target at dispatch: %v", err)
	}
	if _, err := o.signedStep("nope", sub); err == nil {
		t.Error("a step the plan does not have was given a signature")
	}
}
