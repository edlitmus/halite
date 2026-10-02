package hub

import (
	"context"
	"strings"
	"sync"
	"testing"
)

const resumeTwoSteps = `
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
`

// A dry run's steps did not happen, so they cannot be carried forward
// into a real one. `orch resume` used to take whatever a step recorded
// and present it to the requisites as done: resume a --test run for
// real and `drain` -- predicted, never run -- satisfied `deploy_web`'s
// requirement, and `deploy_web` was dispatched for real against nodes
// that had never been drained. The record did not say the run was a
// test, so nothing could tell (DIVERGENCE 5.197).
func TestAResumeOfADryRunIsRefusedForReal(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": resumeTwoSteps})
	node := l.enrolled(t, "web1.example")

	var mu sync.Mutex
	var dispatched []string
	defer l.answeringAs(t, node, "web1.example", func(fun string) bool {
		mu.Lock()
		dispatched = append(dispatched, fun)
		mu.Unlock()
		return true
	})()

	dry, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Test: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dry.Test {
		t.Error("the dry run's record does not say it was a test")
	}
	mu.Lock()
	before := len(dispatched)
	mu.Unlock()

	_, err = l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
		ResumeOf: dry.JID, ResumeFrom: "deploy_web",
	})
	if err == nil {
		t.Fatal("a real resume of a dry run was accepted")
	}
	// The record's own `test`, not the fallback below that reads the
	// steps: a dry run whose steps all predicted no change records only
	// successes, and only this catches it.
	if !strings.Contains(err.Error(), "was a test run") {
		t.Errorf("the refusal should come from the record saying it was a test: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if after := dispatched[before:]; len(after) != 0 {
		t.Errorf("the refused resume dispatched %v for real", after)
	}
}

// A dry resume of a dry run is a dry run, and is allowed: nothing is
// claimed to have happened that did not.
func TestADryResumeOfADryRunIsAllowed(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": resumeTwoSteps})
	defer l.answering(t, "web1.example", nil)()
	dry, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Test: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Test: true,
		ResumeOf: dry.JID, ResumeFrom: "deploy_web",
	})
	if err != nil {
		t.Fatalf("a dry resume of a dry run was refused: %v", err)
	}
	if !again.Test {
		t.Error("the dry resume's record does not say it was a test")
	}
}

// A record written before runs carried `test` cannot say what it was.
// Its steps can: a predicted change -- neither success nor failure -- is
// recorded only by a test run.
func TestAResumeOfAnOldDryRecordIsRefusedByItsPredictions(t *testing.T) {
	l := orchLab(t, map[string]string{"deploy.sls": resumeTwoSteps})
	defer l.answering(t, "web1.example", nil)()
	dry, err := l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"}, Test: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if drain := stepByID(t, dry, "drain"); drain.Result != nil {
		t.Fatalf("the dry run recorded drain as %v; this test needs a predicted change", *drain.Result)
	}
	// What a hub before this change wrote: the same record, no `test`.
	stored, err := l.server.orchStore().Get(dry.JID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Test = false
	if err := l.server.orchStore().Put(stored); err != nil {
		t.Fatal(err)
	}
	_, err = l.server.Orchestrate(context.Background(), OrchRequest{
		Principal: "cert:CN=ed", SLS: []string{"deploy"},
		ResumeOf: dry.JID, ResumeFrom: "deploy_web",
	})
	if err == nil || !strings.Contains(err.Error(), "predicted change") {
		t.Fatalf("an old dry record was resumed for real: %v", err)
	}
}
