package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

// **The API tells the hub whom it acts for** (issue 618), on every runner
// call and on a kill: the token's principal and the roles frozen into it.
// Without it the hub judged this service's certificate alone, and an
// operator narrowed to web* dispatched to, read the runs of, and killed
// jobs on db1 through the API. What the hub then does with it is tested
// against the hub itself, in internal/hub (api_delegation_test.go).
func TestTheAPITellsTheHubWhomItActsFor(t *testing.T) {
	l, hub := executeLab(t, operatorPolicy)
	out := l.login(t, "ed", "hunter2")

	wantFor := func(t *testing.T, what, body string, raw json.RawMessage) {
		t.Helper()
		var got transport.Delegation
		if len(raw) == 0 || json.Unmarshal(raw, &got) != nil {
			t.Fatalf("%s reached the hub without saying whom it is for: %s", what, body)
		}
		if got.Principal != "local:ed" || strings.Join(got.Roles, ",") != strings.Join(out.Roles, ",") || len(got.Roles) == 0 {
			t.Errorf("%s says it is for %+v, want local:ed with the token's roles %v", what, got, out.Roles)
		}
	}

	res, body := l.post(t, PathRun, `{"client":"runner","fun":"jobs.list_jobs"}`, out.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the runner call answered %d: %s", res.StatusCode, body)
	}
	last := hub.seen[len(hub.seen)-1]
	var sent struct {
		OnBehalfOf json.RawMessage `json:"on_behalf_of"`
	}
	_ = json.Unmarshal([]byte(last.body), &sent)
	wantFor(t, "a runner call", last.body, sent.OnBehalfOf)

	// The routes that call a runner on the operator's behalf, not only
	// the `runner` client.
	res, body = l.get(t, PathJobs, out.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/jobs answered %d: %s", res.StatusCode, body)
	}
	last = hub.seen[len(hub.seen)-1]
	sent.OnBehalfOf = nil
	_ = json.Unmarshal([]byte(last.body), &sent)
	wantFor(t, "GET /v1/jobs", last.body, sent.OnBehalfOf)

	req, err := http.NewRequest(http.MethodDelete, l.http.URL+PathJob+"20260824T101500.000000", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+out.Token)
	res, body = do(t, req)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the kill answered %d: %s", res.StatusCode, body)
	}
	last = hub.seen[len(hub.seen)-1]
	if !strings.HasSuffix(last.path, "/kill") {
		t.Fatalf("the last call to the hub was %s, not a kill", last.path)
	}
	sent.OnBehalfOf = nil
	_ = json.Unmarshal([]byte(last.body), &sent)
	wantFor(t, "a kill", last.body, sent.OnBehalfOf)
}
