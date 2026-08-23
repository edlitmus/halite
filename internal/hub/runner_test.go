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
	"github.com/edlitmus/halite/internal/version"
)

// call runs a runner over the wire and fails the test if the request
// itself was refused. The runner's own verdict is left to the caller.
func call(t *testing.T, client *transport.Client, fun string, args ...string) *transport.RunnerResponse {
	t.Helper()
	res, err := client.Runner(context.Background(), transport.RunnerRequest{Fun: fun, Arg: args})
	if err != nil {
		t.Fatalf("%s: %v", fun, err)
	}
	return res
}

// returned decodes a runner's answer into the nine-type model.
func returned(t *testing.T, res *transport.RunnerResponse) any {
	t.Helper()
	if !res.Success {
		t.Fatalf("%s failed: %s", res.Fun, res.Error)
	}
	v, err := value.DecodeJSON(res.Return)
	if err != nil {
		t.Fatalf("%s returned something unreadable: %v", res.Fun, err)
	}
	return v
}

func mapOf(t *testing.T, v any) *value.Map {
	t.Helper()
	m, ok := v.(*value.Map)
	if !ok {
		t.Fatalf("expected a mapping, got %T", v)
	}
	return m
}

func listOf(t *testing.T, m *value.Map, key string) []string {
	t.Helper()
	raw, ok := m.Get(key)
	if !ok {
		t.Fatalf("no %q in %v", key, m.StringKeys())
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("%q is %T, not a list", key, raw)
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = value.KeyString(item)
	}
	return out
}

// A runner grant and a fleet grant are different grants. SPEC 23.5 puts
// runners under `runners:` for exactly this reason, and a principal that
// may run anything on every node still may not ask the hub to accept a
// key unless someone said so.
func TestARunnerNeedsARunnerGrant(t *testing.T) {
	l := newLab(t).withJobs(t)
	loaded, _, err := policy.Load([]byte(`
roles:
  fleet-only:
    - target: '*'
      functions: ['*']
  reader:
    - runners: ['manage.*']
bindings:
  - principal: 'cert:CN=fleet'
    roles: ['fleet-only']
  - principal: 'cert:CN=reader'
    roles: ['reader']
`), "runner-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded

	fleet := l.operator(t, "fleet")
	if _, err := fleet.Runner(context.Background(), transport.RunnerRequest{Fun: "manage.status"}); err == nil {
		t.Fatal("a grant over every function on every node authorized a runner call")
	}

	reader := l.operator(t, "reader")
	if res := call(t, reader, "manage.status"); !res.Success {
		t.Fatalf("manage.status: %s", res.Error)
	}
	if _, err := reader.Runner(context.Background(), transport.RunnerRequest{Fun: "jobs.prune"}); err == nil {
		t.Fatal("a grant of manage.* authorized jobs.prune")
	}
}

// A runner that reaches the fleet is authorized twice: once to call the
// runner, and again as the job it dispatches. Without the second check a
// narrow `runners:` grant becomes fleet-wide execution, which is the
// escalation SPEC 18.3 describes one layer up.
func TestARunnerCannotDispatchWhatItsPrincipalMayNotRun(t *testing.T) {
	l := newLab(t).withJobs(t)
	loaded, _, err := policy.Load([]byte(`
roles:
  runners-only:
    - runners: ['*']
bindings:
  - principal: 'cert:CN=ed'
    roles: ['runners-only']
`), "runner-policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = loaded

	op := l.operator(t, "ed")
	res := call(t, op, "saltutil.refresh_pillar")
	if res.Success {
		t.Fatal("a runners-only grant dispatched a job to the whole fleet")
	}
	if !strings.Contains(res.Error, "pillar.refresh") {
		t.Errorf("the refusal should name the job it would have run: %s", res.Error)
	}
}

// manage.status is the question an operator asks first, and it has to
// separate "enrolled" from "here right now".
func TestManageStatusSplitsTheFleet(t *testing.T) {
	l := newLab(t).withJobs(t)
	here := l.enrolled(t, "web1.example")
	l.enrolled(t, "web2.example")
	defer l.connect(t, here, "web1.example", `{"os":"FreeBSD"}`)()

	op := l.operator(t, "ed")
	status := mapOf(t, returned(t, call(t, op, "manage.status")))
	if up := listOf(t, status, "up"); len(up) != 1 || up[0] != "web1.example" {
		t.Errorf("up = %v", up)
	}
	if down := listOf(t, status, "down"); len(down) != 1 || down[0] != "web2.example" {
		t.Errorf("down = %v", down)
	}

	// The presence names Salt separates are one fact here, and the
	// aliases have to agree with the fact rather than drift from it.
	for _, fun := range []string{"manage.up", "manage.present", "manage.alived"} {
		got := returned(t, call(t, op, fun))
		items, ok := got.([]any)
		if !ok || len(items) != 1 || value.KeyString(items[0]) != "web1.example" {
			t.Errorf("%s = %v", fun, got)
		}
	}
	notAlived := returned(t, call(t, op, "manage.not_alived"))
	if items, ok := notAlived.([]any); !ok || len(items) != 1 {
		t.Errorf("manage.not_alived = %v", notAlived)
	}
	listed := returned(t, call(t, op, "manage.list_not_state", "up"))
	if items, ok := listed.([]any); !ok || len(items) != 1 || value.KeyString(items[0]) != "web2.example" {
		t.Errorf("manage.list_not_state up = %v", listed)
	}
}

// connectSilent attaches a node that never answers, so that a test can
// see a job before its returns rather than only after them.
func (l *lab) connectSilent(t *testing.T, client *transport.Client, nodeID, grains string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		client.Subscribe(ctx, transport.SubscribeRequest{
			NodeID: nodeID,
			Grains: json.RawMessage(grains),
		}, func(msg transport.Message) error {
			if msg.T == transport.MsgPing {
				select {
				case <-ready:
				default:
					close(ready)
				}
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

// The jobs runners read the same cache `halite-hub jobs` reads, so a
// reaction and an operator see one account of what happened.
func TestJobsRunnersReadTheJobCache(t *testing.T) {
	l := newLab(t).withJobs(t)
	client := l.enrolled(t, "web1.example")
	defer l.connectSilent(t, client, "web1.example", `{"os":"FreeBSD"}`)()

	op := l.operator(t, "ed")
	sub, err := op.Submit(context.Background(), transport.SubmitRequest{Target: "*", Fun: "test.ping"})
	if err != nil {
		t.Fatal(err)
	}

	// Nothing has answered yet: the job is active, one node is missing,
	// and it has not succeeded.
	active := mapOf(t, returned(t, call(t, op, "jobs.active")))
	if !active.Has(sub.JID) {
		t.Errorf("jobs.active did not list %s: %v", sub.JID, active.StringKeys())
	}
	if missing := returned(t, call(t, op, "jobs.missing", sub.JID)); len(missing.([]any)) != 1 {
		t.Errorf("jobs.missing = %v", missing)
	}
	if ok := returned(t, call(t, op, "jobs.exit_success", sub.JID)); ok != false {
		t.Errorf("jobs.exit_success on an unanswered job = %v", ok)
	}

	// And once it has.
	err = client.Return(context.Background(), job.Return{
		JID:     job.ID(sub.JID),
		NodeID:  "web1.example",
		Fun:     "test.ping",
		Success: true,
		Return:  json.RawMessage(`"pong"`),
		Schema:  job.ReturnSchema,
	})
	if err != nil {
		t.Fatal(err)
	}
	looked := mapOf(t, returned(t, call(t, op, "jobs.lookup_jid", sub.JID)))
	if got, _ := looked.Get("web1.example"); got != "pong" {
		t.Errorf("jobs.lookup_jid = %v", got)
	}
	if ok := returned(t, call(t, op, "jobs.exit_success", sub.JID)); ok != true {
		t.Errorf("jobs.exit_success after a successful return = %v", ok)
	}
	detail := mapOf(t, returned(t, call(t, op, "jobs.print_job", sub.JID)))
	if fun, _ := detail.Get("fun"); fun != "test.ping" {
		t.Errorf("jobs.print_job fun = %v", fun)
	}
}

// A runner call is a job. Without a record, "who asked the hub to accept
// that key, and when" has no answer on disk, which is the question an
// audit asks and the one Salt's runner history cannot answer.
func TestARunnerCallIsRecordedInTheJobCache(t *testing.T) {
	l := newLab(t).withJobs(t)
	op := l.operator(t, "ed")

	res := call(t, op, "manage.status")
	if res.JID == "" {
		t.Fatal("a runner call was given no job identifier")
	}
	listed := mapOf(t, returned(t, call(t, op, "jobs.list_jobs")))
	entry, ok := listed.Get(res.JID)
	if !ok {
		t.Fatalf("the runner call is not in the job cache: %v", listed.StringKeys())
	}
	record := mapOf(t, entry)
	if fun, _ := record.Get("fun"); fun != "manage.status" {
		t.Errorf("the recorded function is %v", fun)
	}
	if who, _ := record.Get("submitter"); who != "cert:CN=ed" {
		t.Errorf("the recorded submitter is %v", who)
	}
	looked := mapOf(t, returned(t, call(t, op, "jobs.lookup_jid", res.JID)))
	if !looked.Has(RunnerTarget) {
		t.Errorf("the runner's own return was not recorded: %v", looked.StringKeys())
	}
}

// A runner declared in SPEC 19.2 and not built has to say so. The
// alternative makes "not written yet" and "you have mistyped it" the
// same message.
func TestAPendingRunnerSaysWhenItArrives(t *testing.T) {
	l := newLab(t).withJobs(t)
	op := l.operator(t, "ed")

	res := call(t, op, "mine.get")
	if res.Success {
		t.Fatal("the mine is not built and reported success")
	}
	if !strings.Contains(res.Error, "phase 3") {
		t.Errorf("mine.get should name its phase: %s", res.Error)
	}
}

// An unknown runner names what does exist, rather than leaving an
// operator to guess whether they mistyped the module or the function.
func TestAnUnknownRunnerNamesTheOnesThatExist(t *testing.T) {
	l := newLab(t).withJobs(t)
	op := l.operator(t, "ed")

	_, err := op.Runner(context.Background(), transport.RunnerRequest{Fun: "manage.upp"})
	if err == nil {
		t.Fatal("manage.upp was accepted")
	}
	if !strings.Contains(err.Error(), "manage.up") {
		t.Errorf("the refusal should name the near miss: %v", err)
	}
}

// event.send and event.replay are the two halves a reaction needs: one
// to record that something happened, the other to find out that it did.
func TestEventRunnerSendsAndReplays(t *testing.T) {
	l := newLab(t).withJobs(t).withEvents(t)
	op := l.operator(t, "ed")

	sent := mapOf(t, returned(t, call(t, op, "event.send", "halite/audit/deployed")))
	if tag, _ := sent.Get("tag"); tag != "halite/audit/deployed" {
		t.Errorf("event.send returned %v", sent)
	}

	res, err := op.Runner(context.Background(), transport.RunnerRequest{
		Fun:   "event.replay",
		Kwarg: map[string]any{"tag": "halite/audit/**", "from": "earliest"},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := mapOf(t, returned(t, res))
	events, _ := page.Get("events")
	items, ok := events.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("event.replay returned %v", events)
	}
	record := mapOf(t, items[0])
	if tag, _ := record.Get("_tag"); tag != "halite/audit/deployed" {
		t.Errorf("the replayed event is %v", record.StringKeys())
	}
	// The principal is the certificate's, so a reaction firing on this
	// tag can be told who caused it without trusting the payload.
	data := mapOf(t, mustGet(t, record, "data"))
	if who, _ := data.Get("_principal"); who != "cert:CN=ed" {
		t.Errorf("the event does not record who sent it: %v", data.StringKeys())
	}
}

func mustGet(t *testing.T, m *value.Map, key string) any {
	t.Helper()
	v, ok := m.Get(key)
	if !ok {
		t.Fatalf("no %q in %v", key, m.StringKeys())
	}
	return v
}

// A runner that needs a subsystem this hub does not have says which
// subsystem, rather than returning an empty answer that reads like a
// fact about the fleet.
func TestARunnerWithoutItsSubsystemSaysSo(t *testing.T) {
	l := newLab(t).withJobs(t)
	op := l.operator(t, "ed")

	res := call(t, op, "fileserver.envs")
	if res.Success {
		t.Fatal("a hub serving no tree answered fileserver.envs")
	}
	if !strings.Contains(res.Error, "no state tree") {
		t.Errorf("fileserver.envs = %s", res.Error)
	}
}

// The registry is the inventory. A runner registered twice, or one
// registered with no implementation and no phase, is a build-time
// mistake, and NewRunners panics on both -- so building it here is the
// check.
func TestTheRunnerRegistryIsWellFormed(t *testing.T) {
	reg := NewRunners()
	names := reg.Names()
	if len(names) < 40 {
		t.Fatalf("the registry holds %d runners; SPEC 19.2 names many more", len(names))
	}
	sigs := reg.Signatures()
	for _, name := range names {
		sig, ok := sigs.Lookup(name)
		if !ok {
			t.Errorf("%s has no signature", name)
			continue
		}
		if sig.Doc == "" {
			t.Errorf("%s has no documentation, so `runner list` cannot describe it", name)
		}
		if sig.Section == "" {
			t.Errorf("%s does not say which part of SPEC defines it", name)
		}
	}
}

// The wire carries the runner's verdict inside a 200. A caller that read
// the status code alone would report a failed runner as a success.
func TestAFailedRunnerIsA200WithSuccessFalse(t *testing.T) {
	l := newLab(t).withJobs(t)
	op := l.operator(t, "ed")

	res, err := op.Runner(context.Background(), transport.RunnerRequest{
		Fun: "error.error",
		Arg: []string{"the deploy was refused"},
	})
	if err != nil {
		t.Fatalf("error.error should answer, not fail the request: %v", err)
	}
	if res.Success || !strings.Contains(res.Error, "the deploy was refused") {
		t.Errorf("error.error = %+v", res)
	}
	if res.JID == "" {
		t.Error("a failed runner still has to be identifiable afterwards")
	}
	var payload map[string]any
	if err := json.Unmarshal(res.Return, &payload); err == nil && len(payload) > 0 {
		t.Errorf("a failed runner returned a payload as well as an error: %s", res.Return)
	}
}

// A node running the hub's own build is not behind it.
//
// The hub compared `version.Version` against what a node reports, which
// is `version.String()` — the same thing with the commit appended — so
// a matched fleet came back entirely mismatched. Found by running a hub
// and a node rather than by a test.
func TestManageVersionsDoesNotReportAMatchedFleetAsBehind(t *testing.T) {
	// A commit, so that version.String and version.Version differ. A
	// `go test` build has neither set, and without this the test agrees
	// with the defect.
	was := version.Commit
	version.Commit = "0123456789abcdef"
	t.Cleanup(func() { version.Commit = was })

	l := newLab(t).withJobs(t)
	l.enrolled(t, "web1.example")
	err := l.server.nodes().Put(&NodeData{
		NodeID:   "web1.example",
		Version:  version.String(),
		LastSeen: l.server.now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	op := l.operator(t, "ed")
	got := mapOf(t, returned(t, call(t, op, "manage.versions")))
	if hub, _ := got.Get("hub"); hub != version.String() {
		t.Errorf("the hub reports its version as %v, and a node reports %q", hub, version.String())
	}
	if behind := listOf(t, got, "mismatched"); len(behind) != 0 {
		t.Errorf("a node on the hub's own build was reported as %v", behind)
	}
}
