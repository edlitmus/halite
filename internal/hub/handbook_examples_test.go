package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/value"
)

// The orchestrations and reactions in the handbook's chapters on automation
// are files under contrib/examples/handbook/automation, quoted into the pages
// byte for byte by the handbook's own check. These tests hold the files to
// what the pages say they do, on the hub's real compiler and dispatcher, with
// a node that answers.

const automationExamples = "../../contrib/examples/handbook/automation"

func exampleFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(automationExamples, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func pillarOf(t *testing.T, version string) *value.Map {
	t.Helper()
	return value.MapOf("version", version)
}

// Chapter 20's rolling update: every step runs in order and the rollback,
// which waits on `onfail`, is skipped when nothing failed.
func TestTheRollingUpdateRunsAndDoesNotRollBackWhenNothingFailed(t *testing.T) {
	l := orchLab(t, map[string]string{"rolling-update.sls": exampleFile(t, "orchestration/rolling-update.sls")})
	defer l.answering(t, "web1.example", nil)()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"rolling-update"}, Pillar: pillarOf(t, "1.2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != OrchComplete {
		t.Fatalf("the run is %s: %+v", run.State, run.Steps)
	}
	for _, id := range []string{"announce_start", "rehearse_web", "apply_web", "verify_web", "announce_done"} {
		if s := stepByID(t, run, id); s.Skipped || !s.Succeeded() {
			t.Errorf("%s: skipped=%v succeeded=%v %s", id, s.Skipped, s.Succeeded(), s.Comment)
		}
	}
	if s := stepByID(t, run, "roll_back"); !s.Skipped {
		t.Errorf("the rollback ran although nothing failed: %s", s.Comment)
	}
	for _, tag := range []string{"halite/audit/rollout_started", "halite/audit/rollout_finished"} {
		if !busHas(t, l, tag) {
			t.Errorf("the run never put %s on the bus", tag)
		}
	}
}

// And when the application fails, the rollback runs and the run fails: a
// rollback is a repair, not an absolution.
func TestTheRollingUpdateRollsBackWhenAStepFails(t *testing.T) {
	l := orchLab(t, map[string]string{"rolling-update.sls": exampleFile(t, "orchestration/rolling-update.sls")})
	// Every job fails except the rollback's own command.
	defer l.answering(t, "web1.example", func(fun string) bool { return fun == "cmd.run" || fun == "test.ping" })()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"rolling-update"}, Pillar: pillarOf(t, "1.2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != OrchFailed {
		t.Errorf("a run whose apply failed is %s", run.State)
	}
	if s := stepByID(t, run, "apply_web"); s.Succeeded() {
		t.Error("apply_web succeeded although its node refused")
	}
	if s := stepByID(t, run, "roll_back"); s.Skipped || !s.Succeeded() {
		t.Errorf("the rollback did not run: skipped=%v %s", s.Skipped, s.Comment)
	}
}

// Chapter 20's approval gate times out, with the message the chapter shows,
// when nobody approves.
func TestTheApprovalGateFailsWhenNobodyApproves(t *testing.T) {
	src := strings.Replace(exampleFile(t, "orchestration/approval-gate.sls"), "timeout: 120", "timeout: 1", 1)
	l := orchLab(t, map[string]string{"approval-gate.sls": src})
	defer l.answering(t, "web1.example", nil)()

	run, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"approval-gate"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != OrchFailed {
		t.Fatalf("the run is %s", run.State)
	}
	if s := stepByID(t, run, "wait_for_approval"); s.Succeeded() {
		t.Error("the gate opened with no approval")
	}
	if s := stepByID(t, run, "proceed"); !s.Skipped {
		t.Error("the step behind a closed gate ran")
	}
}

// The three reactions of chapter 21 render and dispatch what the chapter
// says, for the event shapes the chapter names.
func TestTheHandbookReactionsDispatchWhatChapterTwentyOneSays(t *testing.T) {
	files := map[string]string{}
	for _, n := range []string{"node-started.sls", "self-heal.sls", "config-changed.sls"} {
		files[n] = exampleFile(t, "reactor/"+n)
	}
	l, r, _ := reactorLab(t, files, `
reactor:
  - tag: 'halite/node/*/start'
    sls: ['$DIR/node-started.sls']
    principal: 'cert:CN=ed'
  - tag: 'halite/beacon/*/filechanges/**'
    sls: ['$DIR/self-heal.sls', '$DIR/config-changed.sls']
    principal: 'cert:CN=ed'
`)
	l.server.Policy = labPolicy(t)
	l.enrolled(t, "web1.example")

	done := make(chan []ReactionResult, 2)
	r.Handled = func(tag string, res []ReactionResult) { done <- res }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.start(ctx)

	wait := func() []ReactionResult {
		t.Helper()
		select {
		case res := <-done:
			return res
		case <-time.After(5 * time.Second):
			t.Fatal("the reaction never ran")
			return nil
		}
	}

	r.Offer(&eventbus.Event{
		Tag: "halite/node/web1.example/start", Node: "web1.example",
		Data: map[string]any{"id": "web1.example"},
	})
	res := wait()
	if len(res) != 1 || !res[0].Succeeded() || res[0].Reaction.Fun != "state.apply" {
		t.Fatalf("node-started: %+v", res)
	}
	if res[0].Reaction.Kwarg["test"] != true {
		t.Errorf("node-started does not rehearse: kwarg %v", res[0].Reaction.Kwarg)
	}

	r.Offer(&eventbus.Event{
		Tag: "halite/beacon/web1.example/filechanges/etc/motd", Node: "web1.example",
		Data: map[string]any{"id": "web1.example", "path": "/etc/motd"},
	})
	res = wait()
	var kinds []string
	for _, x := range res {
		if x.Error != "" {
			t.Errorf("%s failed: %s", x.Reaction.Fun, x.Error)
		}
		kinds = append(kinds, x.Reaction.Fun)
	}
	joined := strings.Join(kinds, ",")
	for _, want := range []string{"state.apply", "cmd.run", "event.send"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the file-change reactions ran %v; want %s among them", kinds, want)
		}
	}
	if !busHas(t, l, "halite/audit/config_changed") {
		t.Error("config-changed never announced itself")
	}
}
