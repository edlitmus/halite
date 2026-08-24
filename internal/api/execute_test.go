package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// stubHub stands in for the control plane, recording what the API
// forwarded and answering with what a hub would.
type stubHub struct {
	t *testing.T
	// seen records each request path and body.
	seen []stubCall
	// submitted is the last job submission, decoded.
	submitted transport.SubmitRequest
	// runnerReturn is what a runner call answers with.
	runnerReturn string
	// runnerFails makes the hub refuse, as it would for a grant this
	// service does not have.
	runnerFails string
	server      *httptest.Server
}

type stubCall struct {
	path string
	body string
}

func newStubHub(t *testing.T) *stubHub {
	t.Helper()
	h := &stubHub{t: t, runnerReturn: `{"ok":true}`}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "halite-hub test ok")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body := readAll(r)
		h.seen = append(h.seen, stubCall{path: r.URL.Path, body: body})

		switch r.URL.Path {
		case transport.PathJobs:
			_ = json.Unmarshal([]byte(body), &h.submitted)
			writeJSON(w, http.StatusAccepted, transport.SubmitResponse{
				JID: "20260824T101500.000000", Nodes: []string{"web1.example"},
			})
		case transport.PathRunners:
			if h.runnerFails != "" {
				writeJSON(w, http.StatusForbidden,
					transport.Error{Error: h.runnerFails, Code: transport.CodeRefused})
				return
			}
			writeJSON(w, http.StatusOK, transport.RunnerResponse{
				JID: "20260824T101500.000001", Fun: "stub",
				Success: true, Return: json.RawMessage(h.runnerReturn),
			})
		default:
			if strings.HasPrefix(r.URL.Path, transport.PathJob) {
				writeJSON(w, http.StatusOK, transport.JobStatus{
					JID: "20260824T101500.000000", Fun: "test.ping",
					Nodes: []string{"web1.example"}, State: "complete",
					Returns: []json.RawMessage{
						json.RawMessage(`{"id":"web1.example","return":"pong"}`),
					},
				})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}
	})
	h.server = httptest.NewTLSServer(mux)
	t.Cleanup(h.server.Close)
	return h
}

// client is a transport client pointed at the stub.
//
// The stub's own certificate is pinned as the CA, which is what a
// self-signed one is. Nothing is skipped: the client verifies exactly
// as it does against a real hub, so the test exercises the path
// production uses rather than one that shrugs.
func (h *stubHub) client() *transport.Client {
	return &transport.Client{
		HubURL:     h.server.URL,
		CA:         h.server.Certificate(),
		ServerName: "127.0.0.1",
		Timeout:    10 * time.Second,
	}
}

func readAll(r *http.Request) string {
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String()
}

// executeLab is an API with a stub hub behind it.
func executeLab(t *testing.T, policySrc string) (*lab, *stubHub) {
	t.Helper()
	l := newLab(t)
	hub := newStubHub(t)
	l.server.Hub = hub.client()
	if policySrc != "" {
		loaded, _, err := policy.Load([]byte(policySrc), "policy.yaml")
		if err != nil {
			t.Fatal(err)
		}
		l.server.Policy = loaded
	}
	return l, hub
}

const operatorPolicy = `
roles:
  operator:
    - target: 'web*'
      functions: ['test.ping', 'state.apply']
    - runners: ['jobs.*', 'manage.*', 'cache.*']
bindings:
  - principal: 'local:ed'
    roles: ['operator']
`

// The operator behind the token is authorized here, before anything
// reaches the hub. Without this check, logging in would hand out this
// service's whole authority.
func TestTheOperatorIsAuthorizedBeforeTheHubIsAsked(t *testing.T) {
	l, hub := executeLab(t, operatorPolicy)
	out := l.login(t, "ed", "hunter2")

	// Permitted: it reaches the hub.
	res, body := l.post(t, PathRun, `{"tgt":"web1","fun":"test.ping"}`, out.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("a permitted run answered %d: %s", res.StatusCode, body)
	}
	if len(hub.seen) == 0 {
		t.Fatal("nothing reached the hub")
	}

	// Refused: it does not.
	before := len(hub.seen)
	res, body = l.post(t, PathRun, `{"tgt":"db1","fun":"test.ping"}`, out.Token)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("a run outside the grant answered %d: %s", res.StatusCode, body)
	}
	res, _ = l.post(t, PathRun, `{"tgt":"web1","fun":"cmd.run"}`, out.Token)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("a function outside the grant answered %d", res.StatusCode)
	}
	if len(hub.seen) != before {
		t.Error("a refused request still reached the hub")
	}
}

// The job records who really asked, so the audit names the person and
// not the service.
func TestAJobRecordsWhoItWasSubmittedFor(t *testing.T) {
	l, hub := executeLab(t, operatorPolicy)
	out := l.login(t, "ed", "hunter2")

	res, _ := l.post(t, PathRun, `{"tgt":"web1","fun":"test.ping"}`, out.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the run answered %d", res.StatusCode)
	}
	if hub.submitted.OnBehalfOf != "local:ed" {
		t.Errorf("the job was submitted on behalf of %q", hub.submitted.OnBehalfOf)
	}
	// And the body does not claim to be the submitter: the hub reads
	// that from the certificate.
	if strings.Contains(hub.seen[0].body, `"submitter"`) {
		t.Errorf("the submission names a submitter: %s", hub.seen[0].body)
	}
}

// The token's frozen roles decide, not whatever the policy binds to the
// principal now.
func TestATokensRolesAreWhatDecides(t *testing.T) {
	l, _ := executeLab(t, operatorPolicy)
	out := l.login(t, "ed", "hunter2")

	// The policy is rewritten to give the principal much more. The
	// token in the operator's hand does not widen.
	wider, _, err := policy.Load([]byte(`
roles:
  operator:
    - target: 'web*'
      functions: ['test.ping', 'state.apply']
  superuser:
    - target: '*'
      functions: ['*']
bindings:
  - principal: 'local:ed'
    roles: ['operator', 'superuser']
`), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l.server.Policy = wider

	res, _ := l.post(t, PathRun, `{"tgt":"db1","fun":"test.ping"}`, out.Token)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("a role added after the token was issued widened it: %d", res.StatusCode)
	}
	// A token issued now does carry it.
	fresh := l.login(t, "ed", "hunter2")
	res, body := l.post(t, PathRun, `{"tgt":"db1","fun":"test.ping"}`, fresh.Token)
	if res.StatusCode != http.StatusOK {
		t.Errorf("a fresh token did not carry the new role: %d %s", res.StatusCode, body)
	}
}

// A synchronous run answers with what each node said.
func TestASynchronousRunReturnsWhatTheNodesSaid(t *testing.T) {
	l, _ := executeLab(t, operatorPolicy)
	out := l.login(t, "ed", "hunter2")

	res, body := l.post(t, PathRun, `{"tgt":"web1","fun":"test.ping"}`, out.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the run answered %d: %s", res.StatusCode, body)
	}
	var got RunResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.JID == "" {
		t.Error("the answer has no jid")
	}
	if got.Return["web1.example"] != "pong" {
		t.Errorf("the answer is %v", got.Return)
	}
}

// An asynchronous submission answers with the jid and does not wait.
func TestAnAsynchronousSubmissionReturnsTheJid(t *testing.T) {
	l, _ := executeLab(t, operatorPolicy)
	out := l.login(t, "ed", "hunter2")

	res, body := l.post(t, PathJobs, `{"tgt":"web1","fun":"test.ping"}`, out.Token)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("the submission answered %d: %s", res.StatusCode, body)
	}
	var got JobAccepted
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.JID == "" {
		t.Errorf("the answer is %+v", got)
	}
}

// Salt's netapi client types are preserved, and the one this build does
// not serve is refused by name rather than run as something else.
func TestTheClientTypesArePreserved(t *testing.T) {
	l, _ := executeLab(t, operatorPolicy)
	out := l.login(t, "ed", "hunter2")

	// `wheel` reaches the runner registry: this build has one hub
	// namespace, and an existing client that says wheel means the
	// same thing.
	res, body := l.post(t, PathRun,
		`{"client":"wheel","fun":"manage.status"}`, out.Token)
	if res.StatusCode != http.StatusOK {
		t.Errorf("a wheel client answered %d: %s", res.StatusCode, body)
	}

	res, body = l.post(t, PathRun, `{"client":"ssh","tgt":"web1","fun":"test.ping"}`, out.Token)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("the ssh client answered %d", res.StatusCode)
	}
	if !strings.Contains(body, "phase 5") {
		t.Errorf("the refusal should name the phase: %s", body)
	}
	res, _ = l.post(t, PathRun, `{"client":"nonsense","fun":"test.ping"}`, out.Token)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("an unknown client answered %d", res.StatusCode)
	}
}

// Reading a node's pillar is reading its secrets, so a role has to name
// it: a wildcard never carries it. SPEC 22.1.
func TestReadingPillarIsNeverGrantedByAWildcard(t *testing.T) {
	l, _ := executeLab(t, `
roles:
  wildcard:
    - runners: ['*']
  named:
    - runners: ['pillar.show_pillar']
bindings:
  - principal: 'local:ed'
    roles: ['wildcard']
  - principal: 'local:named'
    roles: ['named']
`)
	out := l.login(t, "ed", "hunter2")
	res, body := l.get(t, "/v1/pillar/web1.example", out.Token)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("a wildcard read the pillar: %d %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "wildcard") {
		t.Errorf("the refusal should say why: %s", body)
	}

	// The same wildcard grant does reach an ordinary runner, so the
	// refusal is about this endpoint and not about the role.
	res, _ = l.get(t, PathJobs, out.Token)
	if res.StatusCode != http.StatusOK {
		t.Errorf("the wildcard grant did not reach jobs.list_jobs: %d", res.StatusCode)
	}
}

// A refusal from the hub is this service's own grant being too narrow,
// and saying so is the only way an operator can act on it.
func TestAHubRefusalIsReportedAsOne(t *testing.T) {
	l, hub := executeLab(t, operatorPolicy)
	hub.runnerFails = "cert:CN=api is bound to no role"
	out := l.login(t, "ed", "hunter2")

	res, body := l.get(t, PathJobs, out.Token)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("a hub refusal answered %d: %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "cert:CN=api") {
		t.Errorf("the refusal should name whose grant it was: %s", body)
	}
}

// Applying state to one node targets it by list, not by glob: a path
// read as a pattern would let /v1/nodes/*/state reach the estate
// through an endpoint that says it reaches one machine.
func TestApplyingStateToOneNodeTargetsItByList(t *testing.T) {
	l, hub := executeLab(t, operatorPolicy)
	out := l.login(t, "ed", "hunter2")

	res, body := l.post(t, "/v1/nodes/web1.example/state", `{"sls":["web"]}`, out.Token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("applying state answered %d: %s", res.StatusCode, body)
	}
	if hub.submitted.TargetKind != "L" || hub.submitted.Target != "web1.example" {
		t.Errorf("the state run targeted %q as %q",
			hub.submitted.Target, hub.submitted.TargetKind)
	}
	if hub.submitted.Fun != "state.apply" {
		t.Errorf("it ran %q", hub.submitted.Fun)
	}
}
