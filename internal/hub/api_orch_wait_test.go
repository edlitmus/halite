package hub

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/api"
	"github.com/edlitmus/halite/internal/job"
)

// Issue 822: halite-api calls the hub through its own client, whose
// per-request timeout is thirty seconds (cmd/halite-api/serve.go), and it
// waited for an orchestration's outcome on that one request. A run longer
// than that -- `POST /v1/orch`, or `state.orchestrate` through `/v1/run`'s
// runner client -- answered the API caller with a 502 while the run went
// on on the hub, which is issue 815 one service further out.
//
// A real api.Server in front of this package's hub, with the API's hub
// client cut to one second, and an orchestration held for three by an
// event the test sends. The caller's own request has no timeout here, so
// what bounds the wait is the caller, as it is in an estate.
const apiGateSLS = `
wait_for_go:
  salt.wait_for_event:
    - name: halite/test/go
    - timeout: 10
`

const apiTimesoutSLS = `
never:
  salt.wait_for_event:
    - name: halite/test/never
    - timeout: 3
`

func newAPIOrchWaitLab(t *testing.T) *apiLab {
	t.Helper()
	a := newAPILab(t, map[string]string{"gate.sls": apiGateSLS, "timesout.sls": apiTimesoutSLS})
	a.hub.Timeout = time.Second
	return a
}

// releaseAfter sends the event the gate waits for, from the delay on and
// until stopped: the step reads the bus from the moment it starts, so one
// event sent before then would be missed.
func (a *apiLab) releaseAfter(t *testing.T, delay time.Duration) (stop func()) {
	t.Helper()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-done:
			return
		case <-time.After(delay):
		}
		for {
			a.server.emit("halite/test/go", "", map[string]any{})
			select {
			case <-done:
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func TestTheAPIWaitsForAnOrchestrationLongerThanItsHubClientsTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("waits several seconds for an orchestration")
	}
	for _, tc := range []struct {
		name, path, body string
	}{
		{"POST /v1/orch", api.PathOrch, `{"sls":"gate"}`},
		{"/v1/run with the runner client", api.PathRun, `{"client":"runner","fun":"state.orchestrate","kwarg":{"sls":"gate"}}`},
		{"/v1/run with the runner client and state.orch", api.PathRun, `{"client":"runner","fun":"state.orch","kwarg":{"sls":"gate"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newAPIOrchWaitLab(t)
			token := a.login(t, "bob")

			stop := a.releaseAfter(t, 3*time.Second)
			started := time.Now()
			code, body := a.call(t, http.MethodPost, tc.path, token, tc.body)
			took := time.Since(started)
			stop()
			t.Logf("%s answered %d after %s: %s", tc.name, code, took.Round(time.Millisecond), body)

			if code != http.StatusOK {
				t.Fatalf("%s answered %d for a run that succeeded after %s; the API's hub client has 1s:\n%s",
					tc.name, code, took.Round(time.Millisecond), body)
			}
			if took < 2500*time.Millisecond {
				t.Fatalf("%s answered after %s, before the run could have finished", tc.name, took)
			}
			if !strings.Contains(body, `"state":"complete"`) {
				t.Errorf("the answer is not the finished run:\n%s", body)
			}
		})
	}
}

// A long run that fails is answered as a run that failed, 422 with the
// run named, not as a hub that failed.
func TestTheAPIReportsALongOrchestrationsFailure(t *testing.T) {
	if testing.Short() {
		t.Skip("waits several seconds for an orchestration")
	}
	a := newAPIOrchWaitLab(t)
	token := a.login(t, "bob")

	code, body := a.call(t, http.MethodPost, api.PathRun, token,
		`{"client":"runner","fun":"state.orchestrate","kwarg":{"sls":"timesout"}}`)
	t.Logf("answered %d: %s", code, body)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a run whose step failed after 3s answered %d, want 422:\n%s", code, body)
	}
	var out struct {
		Success bool `json:"success"`
		Return  struct {
			JID   string `json:"jid"`
			State string `json:"state"`
		} `json:"return"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Success || out.Return.State != string(OrchFailed) ||
		!strings.Contains(out.Error, "orchestration "+out.Return.JID+" failed") {
		t.Errorf("the answer is not the failed run, named:\n%s", body)
	}
}

// A caller that names `async` gets what it named, as it already did: the
// run's jid at once, and no wait.
func TestTheAPIPassesAnExplicitAsyncThrough(t *testing.T) {
	a := newAPIOrchWaitLab(t)
	token := a.login(t, "bob")
	stop := a.releaseAfter(t, 0)
	defer stop()

	code, body := a.call(t, http.MethodPost, api.PathRun, token,
		`{"client":"runner","fun":"state.orchestrate","kwarg":{"sls":"gate","async":true}}`)
	t.Logf("answered %d: %s", code, body)
	if code != http.StatusOK || !strings.Contains(body, `"state":"running"`) {
		t.Fatalf("an explicit async was not answered at once with the running run:\n%s", body)
	}
	// Released before the lab is torn down, which would otherwise wait for
	// the run's own timeout.
	var out struct {
		JID string `json:"jid"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the run to finish once released", func() bool {
		returns, _ := a.server.Jobs.Returns(job.ID(out.JID))
		return len(returns) > 0
	})
}
