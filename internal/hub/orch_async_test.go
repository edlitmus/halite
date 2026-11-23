package hub

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/value"
)

// `state.orchestrate async=true` answers once the run has started, with
// the run's jid, and files the outcome as the runner call's own job when
// the run ends (issue 815). The call's answer is not the outcome, and the
// job is: a caller polling it reads what the synchronous call would have
// returned.
func TestAnAsyncOrchestrationAnswersAtOnceAndFilesItsOutcomeLater(t *testing.T) {
	l := orchLab(t, map[string]string{"gate.sls": `
wait_for_go:
  salt.wait_for_event:
    - name: halite/test/go
    - timeout: 30
`})

	out, err := l.server.CallRunner(context.Background(), RunnerCall{
		Principal: "cert:CN=ed", Fun: "state.orchestrate",
		Kwarg: map[string]any{"sls": "gate", "async": true},
	})
	if err != nil || !out.Success {
		t.Fatalf("state.orchestrate async: %v %+v", err, out)
	}
	answer, ok := out.Return.(*value.Map)
	if !ok {
		t.Fatalf("the answer is %T, want the run", out.Return)
	}
	state, _ := answer.Get("state")
	jid, _ := answer.Get("jid")
	if state != string(OrchRunning) || jid == "" {
		t.Fatalf("the answer is not a running run: %v", answer)
	}
	if returns, _ := l.server.Jobs.Returns(out.JID); len(returns) != 0 {
		t.Fatalf("the call's job was answered while the run waits: %+v", returns[0])
	}

	// Sent until it is heard: the step reads the bus from the moment it
	// starts, which may be after the first one.
	var filed *job.Return
	eventually(t, "the call's job to be answered", func() bool {
		l.server.emit("halite/test/go", "", map[string]any{})
		returns, _ := l.server.Jobs.Returns(out.JID)
		if len(returns) == 0 {
			return false
		}
		filed = returns[0]
		return true
	})
	if !filed.Success {
		t.Fatalf("the run succeeded and its job says it failed: %s", filed.Return)
	}
	var run struct {
		JID   string `json:"jid"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(filed.Return, &run); err != nil {
		t.Fatal(err)
	}
	if run.JID != jid || run.State != string(OrchComplete) {
		t.Fatalf("the job's return is not the finished run %s: %s", jid, filed.Return)
	}
}

// A run that is refused before its first step is the call's answer, as it
// is without `async`, and leaves no run behind.
func TestAnAsyncOrchestrationThatCannotStartIsRefusedAtOnce(t *testing.T) {
	l := orchLab(t, map[string]string{"gate.sls": "x: {}\n"})
	out, err := l.server.CallRunner(context.Background(), RunnerCall{
		Principal: "cert:CN=ed", Fun: "state.orchestrate",
		Kwarg: map[string]any{"sls": "nosuch", "async": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Success || !strings.Contains(out.Err, "nosuch") {
		t.Fatalf("a missing orchestration was not the answer: %+v", out)
	}
	if returns, _ := l.server.Jobs.Returns(out.JID); len(returns) != 1 || returns[0].Success {
		t.Fatalf("the call's job does not hold the refusal: %+v", returns)
	}
}
