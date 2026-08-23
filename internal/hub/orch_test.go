package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// withOrch gives the lab somewhere to keep its orchestration records,
// as `serve` does.
func (l *lab) withOrch(t *testing.T) *lab {
	t.Helper()
	store, err := OpenOrchStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l.server.Orch = store
	return l
}

// answering connects a node that answers every job with the verdict the
// caller chooses, so a test can make one step fail.
func (l *lab) answering(t *testing.T, nodeID string, ok func(fun string) bool) func() {
	t.Helper()
	return l.answeringAs(t, l.enrolled(t, nodeID), nodeID, ok)
}

// answeringAs is the same for a node that has already enrolled, so a
// test can disconnect it and bring it back without a second key.
func (l *lab) answeringAs(t *testing.T, client *transport.Client, nodeID string, ok func(fun string) bool) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Subscribe(ctx, transport.SubscribeRequest{
			NodeID: nodeID,
			Grains: json.RawMessage(`{"os":"FreeBSD"}`),
		}, func(msg transport.Message) error {
			switch msg.T {
			case transport.MsgPing:
				select {
				case <-ready:
				default:
					close(ready)
				}
			case transport.MsgJob:
				good := ok == nil || ok(msg.Fun)
				client.Return(ctx, job.Return{
					JID:     job.ID(msg.JID),
					NodeID:  nodeID,
					Fun:     msg.Fun,
					Success: good,
					RetCode: map[bool]int{true: 0, false: 1}[good],
					Return:  json.RawMessage(`{"ran":"` + msg.Fun + `"}`),
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
	return func() { cancel(); <-stopped }
}

// orchLab is a hub that can compile and run an orchestration.
func orchLab(t *testing.T, files map[string]string) *lab {
	t.Helper()
	return newLab(t).withJobs(t).withEvents(t).withOrch(t).withFiles(t, files)
}

func stepByID(t *testing.T, run *OrchRun, id string) *OrchStep {
	t.Helper()
	for _, step := range run.Steps {
		if step.ID == id {
			return step
		}
	}
	t.Fatalf("the run has no step %q; it has %v", id, stepIDs(run))
	return nil
}

func stepIDs(run *OrchRun) []string {
	out := make([]string, len(run.Steps))
	for i, step := range run.Steps {
		out[i] = step.ID
	}
	return out
}

// An orchestration is a state run whose modules act on the fleet. The
// requisites are the state system's, so `require` means here exactly
// what it means in a highstate — which is the point of reusing the
// compiler rather than writing a second one.
func TestAnOrchestrationRunsItsStepsInRequisiteOrder(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'

deploy_web:
  salt.state:
    - tgt: '*'
    - sls:
      - webserver
    - require:
      - salt: drain
`})
	defer l.answering(t, "web1.example", nil)()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != OrchComplete {
		t.Fatalf("the run is %s: %+v", run.State, run.Steps)
	}
	if got := stepIDs(run); len(got) != 2 || got[0] != "drain" || got[1] != "deploy_web" {
		t.Fatalf("the steps ran as %v", got)
	}

	drain := stepByID(t, run, "drain")
	if drain.JID == "" || len(drain.Nodes) != 1 || drain.Nodes[0] != "web1.example" {
		t.Errorf("the first step reached %v under job %q", drain.Nodes, drain.JID)
	}
	if len(drain.Returns) != 1 {
		t.Errorf("the step recorded %d return(s)", len(drain.Returns))
	}

	// The record is on disk, which is what `orch show` reads and what
	// makes resuming possible after the terminal is closed.
	stored, err := l.server.Orch.Get(run.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Steps) != 2 || stored.State != OrchComplete {
		t.Errorf("the stored run is %s with %d step(s)", stored.State, len(stored.Steps))
	}
}

// `onfail` is what makes a rollback step expressible, and SPEC 19.1
// names it for exactly that.
func TestAFailedStepTriggersItsOnfailRollback(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
verify:
  salt.function:
    - name: http.query
    - tgt: '*'

rollback:
  salt.state:
    - tgt: '*'
    - sls:
      - webserver.rollback
    - onfail:
      - salt: verify
`})
	// The verification fails; the rollback must then run.
	defer l.answering(t, "web1.example", func(fun string) bool { return fun != "http.query" })()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != OrchFailed {
		t.Errorf("a run with a failed step is %s", run.State)
	}
	verify := stepByID(t, run, "verify")
	if verify.Succeeded() {
		t.Error("the verification step reported success")
	}
	if len(verify.Failed) != 1 || verify.Failed[0] != "web1.example" {
		t.Errorf("the failing node was recorded as %v", verify.Failed)
	}
	rollback := stepByID(t, run, "rollback")
	if rollback.Skipped || !rollback.Succeeded() {
		t.Errorf("the rollback did not run: skipped=%v result=%v comment=%q",
			rollback.Skipped, rollback.Succeeded(), rollback.Comment)
	}
}

// A node an operator has told the step it may lose does not fail it.
// SPEC 19.1 keeps Salt's option under a name that says what it does.
func TestToleratedFailuresDoNotFailTheStep(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
deploy_web:
  salt.state:
    - tgt: '*'
    - sls:
      - webserver
    - tolerate_failures:
      - broken*
`})
	defer l.answering(t, "web1.example", nil)()
	defer l.answering(t, "broken1.example", func(string) bool { return false })()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	step := stepByID(t, run, "deploy_web")
	if !step.Succeeded() {
		t.Errorf("a tolerated failure failed the step: %s", step.Comment)
	}
	// Tolerated is not hidden: the record still names who failed.
	if len(step.Failed) != 1 || step.Failed[0] != "broken1.example" {
		t.Errorf("the failure was not recorded: %v", step.Failed)
	}
}

// Resuming is what SPEC 19.1 says Salt cannot do and what makes a long
// deployment usable: the steps before the named one are carried
// forward as they finished, so the requisites pointing back at them are
// satisfied without running them again.
func TestResumingCarriesTheEarlierStepsForward(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'

deploy_web:
  salt.function:
    - name: pkg.install
    - tgt: '*'
    - require:
      - salt: drain
`})
	// The deploy fails the first time round.
	node := l.enrolled(t, "web1.example")
	stop := l.answeringAs(t, node, "web1.example", func(fun string) bool { return fun != "pkg.install" })
	first, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != OrchFailed {
		t.Fatalf("the first run is %s", first.State)
	}
	stop()

	// It succeeds on the second attempt, and only the named step runs.
	defer l.answeringAs(t, node, "web1.example", nil)()
	second, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
		ResumeOf: first.JID, ResumeFrom: "deploy_web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != OrchComplete {
		t.Fatalf("the resumed run is %s: %+v", second.State, second.Steps)
	}

	drain := stepByID(t, second, "drain")
	if !drain.Skipped {
		t.Error("the step before the resume point ran again")
	}
	if !strings.Contains(drain.Comment, "Carried forward") {
		t.Errorf("the carried step says %q", drain.Comment)
	}
	if drain.JID != "" {
		t.Errorf("the carried step dispatched a job: %s", drain.JID)
	}
	deploy := stepByID(t, second, "deploy_web")
	if deploy.Skipped || !deploy.Succeeded() {
		t.Errorf("the resumed step did not run: %+v", deploy)
	}
	if second.ResumedOf != first.JID || second.ResumedFrom != "deploy_web" {
		t.Errorf("the run does not record what it resumed: %+v", second)
	}
}

// Resuming at a step the compilation does not have would silently start
// from the beginning, which is the opposite of what was asked.
func TestResumingRefusesAStepThatIsNotThere(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'
`})
	defer l.answering(t, "web1.example", nil)()
	first, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
		ResumeOf: first.JID, ResumeFrom: "no_such_step",
	})
	if err == nil {
		t.Fatal("resuming at a step that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "no_such_step") {
		t.Errorf("the refusal says %q", err)
	}
}

// A state honours test mode by contract — SPEC 11.6, enforced for every
// state module by the conformance harness — so a test orchestration
// sends its state steps out with `test` set and the operator sees what
// each node would do.
func TestATestOrchestrationSendsStateStepsInTestMode(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
deploy_web:
  salt.state:
    - tgt: '*'
    - sls:
      - webserver
`})
	var sawTest, sawApply bool
	defer l.answeringTracking(t, "web1.example", func(msg transport.Message) {
		sawApply = true
		if test, ok := msg.Kwarg["test"]; ok && value.Truthy(test) {
			sawTest = true
		}
	})()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Test: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	step := stepByID(t, run, "deploy_web")
	if !step.Succeeded() {
		t.Errorf("the test step failed: %s", step.Comment)
	}
	// A state honours test mode by contract, so the job goes out with
	// `test` set and the operator sees what each node would do.
	if !sawApply || !sawTest {
		t.Errorf("the state step reached the node as apply=%v test=%v", sawApply, sawTest)
	}
}

// An execution function cannot be trusted to change nothing under test
// mode: `salt.function` runs whatever it names. A test run reports what
// it would call rather than calling it, or the test run is the change.
func TestATestOrchestrationDoesNotCallAnExecutionFunction(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'
`})
	defer l.answering(t, "web1.example", func(string) bool {
		t.Error("a test run dispatched an execution function to the fleet")
		return true
	})()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Test: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	step := stepByID(t, run, "drain")
	if step.JID != "" {
		t.Errorf("a test run recorded a job: %s", step.JID)
	}
	if !strings.Contains(step.Comment, "Would run") {
		t.Errorf("a test step says %q", step.Comment)
	}
}

// answeringTracking answers every job and reports what it was given, so
// a test can see the arguments the hub actually sent.
func (l *lab) answeringTracking(t *testing.T, nodeID string, saw func(transport.Message)) func() {
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
					saw(msg)
					client.Return(ctx, job.Return{
						JID: job.ID(msg.JID), NodeID: nodeID, Fun: msg.Fun,
						Success: true, Return: json.RawMessage(`{}`), Schema: job.ReturnSchema,
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

// A step is authorized twice: once as the orchestration, and again as
// the job it dispatches. Permission to run an orchestration is not
// permission to run whatever it happens to name.
func TestAnOrchestrationStepIsAuthorizedAsItsOwnJob(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
deploy_web:
  salt.function:
    - name: pkg.install
    - tgt: '*'
`})
	defer l.answering(t, "web1.example", nil)()

	loaded, _, err := policy.Load([]byte(`
roles:
  orchestrator:
    - runners: ['*']
    - target: '*'
      functions: ['test.ping']
bindings:
  - principal: 'cert:CN=ed'
    roles: ['orchestrator']
`), "orch-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	step := stepByID(t, run, "deploy_web")
	if step.Succeeded() {
		t.Fatal("a step ran a function its principal may not run")
	}
	if !strings.Contains(step.Comment, "pkg.install") {
		t.Errorf("the refusal should name the function: %q", step.Comment)
	}
}

// The runner surface is what an operator and a reaction both reach the
// orchestration through, so the timeline has to come back through it.
func TestTheOrchestrationRunnersRunAndReport(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'
`})
	defer l.answering(t, "web1.example", nil)()
	op := l.operator(t, "ed")

	res, err := op.Runner(context.Background(), transport.RunnerRequest{
		Fun: "state.orchestrate", Kwarg: map[string]any{"sls": "deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := mapOf(t, returned(t, res))
	jid, _ := out.Get("jid")
	if jid == nil || jid == "" {
		t.Fatalf("the run has no jid: %v", out.StringKeys())
	}

	shown := mapOf(t, returned(t, call(t, op, "state.orch_show", value.KeyString(jid))))
	steps, _ := shown.Get("steps")
	list, ok := steps.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("the timeline has %v", steps)
	}
	listed := mapOf(t, returned(t, call(t, op, "state.orch_list")))
	if !listed.Has(value.KeyString(jid)) {
		t.Errorf("the run is not in the listing: %v", listed.StringKeys())
	}

	// `orchestrate_show_sls` compiles and dispatches nothing.
	planned := mapOf(t, returned(t, res2(t, op, "state.orchestrate_show_sls", "deploy")))
	pSteps, _ := planned.Get("steps")
	if list, ok := pSteps.([]any); !ok || len(list) != 1 {
		t.Errorf("the plan has %v", pSteps)
	}
}

func res2(t *testing.T, client *transport.Client, fun, sls string) *transport.RunnerResponse {
	t.Helper()
	res, err := client.Runner(context.Background(), transport.RunnerRequest{
		Fun: fun, Kwarg: map[string]any{"sls": sls},
	})
	if err != nil {
		t.Fatalf("%s: %v", fun, err)
	}
	return res
}

// A resumed run is judged by the steps it ran, not by the failure that
// made someone resume it. Otherwise no resume could ever succeed.
func TestAResumedRunIsJudgedByWhatItRan(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
verify:
  salt.function:
    - name: http.query
    - tgt: '*'

rollback:
  salt.function:
    - name: pkg.remove
    - tgt: '*'
    - onfail:
      - salt: verify
`})
	node := l.enrolled(t, "web1.example")
	stop := l.answeringAs(t, node, "web1.example", func(fun string) bool { return fun != "http.query" })
	first, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != OrchFailed {
		t.Fatalf("the first run is %s", first.State)
	}
	stop()

	defer l.answeringAs(t, node, "web1.example", nil)()
	second, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
		ResumeOf: first.JID, ResumeFrom: "rollback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != OrchComplete {
		t.Errorf("a resumed run whose own steps all succeeded is %s: %+v", second.State, second.Steps)
	}
}

// A step's `timeout` is the per-state option of SPEC 11.7, which is
// what SPEC 19.1 means by a step's timeout. The state runner bounds the
// step's context with it and strips it before the module is called, so
// the module reads it back off the context — one timeout rather than
// two that can disagree.
//
// They did disagree: a step written with `timeout: 10s` waited its ten
// seconds and then reported that the run had been stopped, because the
// module was waiting on its own default of five minutes while the
// context expired underneath it.
func TestAStepTimeoutBoundsTheWaitAndSaysSo(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": `
drain:
  salt.function:
    - name: lb.drain
    - tgt: '*'
    - timeout: 1s
`})
	client := l.enrolled(t, "web1.example")
	defer l.connectSilent(t, client, "web1.example", `{"os":"FreeBSD"}`)()

	started := time.Now()
	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 30*time.Second {
		t.Fatalf("the step waited %s; its timeout was one second", elapsed)
	}
	step := stepByID(t, run, "drain")
	if step.Succeeded() {
		t.Error("a step whose node never answered reported success")
	}
	if len(step.Missing) != 1 || step.Missing[0] != "web1.example" {
		t.Errorf("the unanswered node was recorded as %v", step.Missing)
	}
	// "It said no" and "it said nothing" call for different things, and
	// the comment has to tell them apart.
	if !strings.Contains(step.Comment, "never answered") {
		t.Errorf("the step says %q", step.Comment)
	}
}
