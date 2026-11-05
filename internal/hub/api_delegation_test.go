package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/account"
	"github.com/edlitmus/halite/internal/api"
	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/transport"
)

// Issue 618: halite-api calls every runner, and kill, with its own
// certificate, so the hub judged the API's role and not the operator's.
// The four tests below are the four things the 2026-11-03 review showed an
// operator narrowed to `web*` doing through the API, each driven through a
// real api.Server in front of this package's hub, with one policy file
// read by both, as an estate runs them.
//
// They are written to compile against the code before the fix as well as
// after, so that "fails before, passes after" was run rather than argued:
// the policy's `act_for` sits on a rule that also grants runners, which
// a policy without the key reads as an unknown field and ignores.

// delegationPolicySrc is the one policy both services read.
//
// alice may do anything to web*, by function and by runner, and nothing
// else. bob may do anything anywhere. The API may do anything anywhere
// too -- the estate the review found, where the API's grant is the sum
// of its operators' -- and may act for any local account.
const delegationPolicySrc = `
roles:
  api-service:
    - target: '*'
      functions: ['*']
    - target: '*'
      runners: ['*', 'pillar.show_pillar']
      act_for: ['local:*']
  webops:
    - target: 'web*.example'
      functions: ['*']
    - target: 'web*.example'
      runners: ['saltutil.*', 'mine.*', 'state.orchestrate', 'state.orch_*', 'jobs.*', 'pillar.show_pillar']
  dbops:
    - target: '*'
      functions: ['*']
    - target: '*'
      runners: ['*', 'pillar.show_pillar']
bindings:
  - principal: 'cert:CN=api'
    roles: ['api-service']
  - principal: 'cert:CN=alice'
    roles: ['webops']
  - principal: 'local:alice'
    roles: ['webops']
  - principal: 'local:bob'
    roles: ['dbops']
`

// apiLab is a hub with web1 and db1 connected and answering, db2 enrolled
// and never connected (so a job to it stays open to be killed), an
// orchestration tree, and an API in front of it holding cert:CN=api.
type apiLab struct {
	*lab
	api *httptest.Server
}

func newAPILab(t *testing.T, files map[string]string) *apiLab {
	t.Helper()
	l := orchLab(t, files)
	p, _, err := policy.Load([]byte(delegationPolicySrc), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	catalogued(p)
	l.server.Policy = p
	for _, node := range []string{"web1.example", "db1.example"} {
		t.Cleanup(l.answering(t, node, func(string) bool { return true }))
	}
	l.enrolled(t, "db2.example")

	// The API reads the same file, loaded again as halite-api would.
	ap, _, err := policy.Load([]byte(delegationPolicySrc), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	catalogued(ap)
	hash, err := account.Hash("hunter2", account.MinIterations)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := apitoken.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s := &api.Server{
		Accounts: &account.File{Accounts: map[string]*account.Account{
			"alice": {Name: "alice", Hash: hash},
			"bob":   {Name: "bob", Hash: hash},
		}},
		Tokens:        tokens,
		Policy:        ap,
		Hub:           l.operator(t, "api"),
		Metrics:       metrics.NewRegistry(),
		TokenLifetime: time.Hour,
		TokenIdle:     30 * time.Minute,
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &apiLab{lab: l, api: ts}
}

func (a *apiLab) login(t *testing.T, user string) string {
	t.Helper()
	code, body := a.call(t, http.MethodPost, api.PathLogin, "", `{"username":"`+user+`","password":"hunter2"}`)
	if code != http.StatusOK {
		t.Fatalf("logging in as %s answered %d: %s", user, code, body)
	}
	var out api.LoginResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out.Token
}

func (a *apiLab) call(t *testing.T, method, path, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, a.api.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res.StatusCode, b.String()
}

// runner calls one runner through the API's `runner` client and returns
// the status and the decoded answer.
func (a *apiLab) runner(t *testing.T, token, fun string, arg []string, kwarg map[string]any) (int, map[string]any, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"client": "runner", "fun": fun, "arg": arg, "kwarg": kwarg})
	if err != nil {
		t.Fatal(err)
	}
	code, raw := a.call(t, http.MethodPost, api.PathRun, token, string(body))
	var out map[string]any
	_ = json.Unmarshal([]byte(raw), &out)
	return code, out, raw
}

// jobsReaching lists the fleet jobs the hub has sent to a node.
func (a *apiLab) jobsReaching(t *testing.T, node string) []string {
	t.Helper()
	all, err := a.server.Jobs.List(0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, j := range all {
		for _, n := range j.Nodes {
			if n == node {
				out = append(out, j.Fun+" "+string(j.JID))
			}
		}
	}
	return out
}

// **Dispatch.** A runner that reaches nodes by sending jobs sent them as
// the API. alice, whose every rule names web*, refreshed db1's pillar and
// grains, updated its mine, and ran an orchestration step on it.
func TestAnOperatorThroughTheAPIDispatchesOnlyWhereTheyMay(t *testing.T) {
	a := newAPILab(t, map[string]string{
		"reach/db.sls":  "ping_db:\n  salt.function:\n    - name: test.ping\n    - tgt: 'db1.example'\n",
		"reach/web.sls": "ping_web:\n  salt.function:\n    - name: test.ping\n    - tgt: 'web1.example'\n",
	})
	alice := a.login(t, "alice")

	for _, c := range []struct {
		fun   string
		kwarg map[string]any
	}{
		{"saltutil.refresh_pillar", map[string]any{"target": "db1.example"}},
		{"saltutil.refresh_grains", map[string]any{"target": "db1.example"}},
		{"mine.update", map[string]any{"tgt": "db1.example"}},
		{"state.orchestrate", map[string]any{"sls": "reach.db"}},
	} {
		code, _, raw := a.runner(t, alice, c.fun, nil, c.kwarg)
		if code == http.StatusOK {
			t.Errorf("alice, narrowed to web*, ran %s %v through the API: %s", c.fun, c.kwarg, raw)
		}
	}
	if got := a.jobsReaching(t, "db1.example"); len(got) > 0 {
		t.Errorf("jobs reached db1 for an operator narrowed to web*: %v", got)
	}

	// Inside her scope she is not stopped.
	for _, c := range []struct {
		fun   string
		kwarg map[string]any
	}{
		{"saltutil.refresh_pillar", map[string]any{"target": "web1.example"}},
		{"mine.update", map[string]any{"tgt": "web1.example"}},
		{"state.orchestrate", map[string]any{"sls": "reach.web"}},
	} {
		if code, _, raw := a.runner(t, alice, c.fun, nil, c.kwarg); code != http.StatusOK {
			t.Errorf("alice could not run %s %v on web1 through the API: %d %s", c.fun, c.kwarg, code, raw)
		}
	}
	if got := a.jobsReaching(t, "web1.example"); len(got) < 3 {
		t.Errorf("alice's own jobs did not all reach web1: %v", got)
	}
}

// **Visibility.** A runner call's job is shown to its caller, and through
// the API every caller was the API: alice read bob's `pillar.show_pillar
// db1` and its return with jobs.lookup_jid, list_job and print_job, and
// listed it.
func TestAnOperatorThroughTheAPISeesOnlyTheirOwnRunnerCalls(t *testing.T) {
	a := newAPILab(t, nil)
	alice, bob := a.login(t, "alice"), a.login(t, "bob")

	// The lab's hub compiles no pillar, so the runner fails; it is the
	// job record, its arguments and its return, that is at issue, and a
	// failed call has all three.
	_, out, raw := a.runner(t, bob, "pillar.show_pillar", []string{"db1.example"}, nil)
	jid, _ := out["jid"].(string)
	if jid == "" {
		t.Fatalf("bob's call has no jid: %s", raw)
	}

	for _, fun := range []string{"jobs.lookup_jid", "jobs.list_job", "jobs.print_job"} {
		if code, _, raw := a.runner(t, alice, fun, []string{jid}, nil); code == http.StatusOK {
			t.Errorf("alice read bob's pillar.show_pillar db1 with %s: %s", fun, raw)
		}
	}
	if code, body := a.call(t, http.MethodGet, api.PathJob+jid, alice, ""); code == http.StatusOK {
		t.Errorf("alice read bob's runner call at GET /v1/jobs/%s: %s", jid, body)
	}
	// By key: her own lookups name the jid in their arguments.
	code, body := a.call(t, http.MethodGet, api.PathJobs, alice, "")
	var listed map[string]any
	if err := json.Unmarshal([]byte(body), &listed); code != http.StatusOK || err != nil {
		t.Fatalf("alice's job list answered %d: %s", code, body)
	}
	if _, ok := listed[jid]; ok {
		t.Errorf("alice's job list names bob's runner call %s: %s", jid, body)
	}

	// bob, who made it, still reads it.
	if code, _, raw := a.runner(t, bob, "jobs.lookup_jid", []string{jid}, nil); code != http.StatusOK {
		t.Errorf("bob could not read his own runner call: %d %s", code, raw)
	}
	// And alice reads her own.
	_, out, raw = a.runner(t, alice, "pillar.show_pillar", []string{"web1.example"}, nil)
	mine, _ := out["jid"].(string)
	if mine == "" {
		t.Fatalf("alice's call has no jid: %s", raw)
	}
	if code, _, raw := a.runner(t, alice, "jobs.lookup_jid", []string{mine}, nil); code != http.StatusOK {
		t.Errorf("alice could not read her own runner call: %d %s", code, raw)
	}
}

// **Orchestrations.** The same rule for runs: alice listed, showed and
// resumed -- re-ran -- bob's orchestration of db1.
func TestAnOperatorThroughTheAPISeesAndResumesOnlyTheirOwnRuns(t *testing.T) {
	a := newAPILab(t, map[string]string{
		"reach/db.sls": "ping_db:\n  salt.function:\n    - name: test.ping\n    - tgt: 'db1.example'\n",
	})
	alice, bob := a.login(t, "alice"), a.login(t, "bob")

	code, out, raw := a.runner(t, bob, "state.orchestrate", nil, map[string]any{"sls": "reach.db"})
	if code != http.StatusOK {
		t.Fatalf("bob could not run his orchestration: %d %s", code, raw)
	}
	ret, _ := out["return"].(map[string]any)
	jid, _ := ret["jid"].(string)
	if jid == "" {
		t.Fatalf("bob's run has no jid: %s", raw)
	}
	before := len(a.jobsReaching(t, "db1.example"))

	if code, _, raw := a.runner(t, alice, "state.orch_show", []string{jid}, nil); code == http.StatusOK {
		t.Errorf("alice showed bob's orchestration: %s", raw)
	}
	if code, body := a.call(t, http.MethodGet, api.PathOrchJob+jid, alice, ""); code == http.StatusOK {
		t.Errorf("alice showed bob's orchestration at GET /v1/orch/%s: %s", jid, body)
	}
	if code, _, raw := a.runner(t, alice, "state.orch_list", nil, nil); code != http.StatusOK || strings.Contains(raw, jid) {
		t.Errorf("alice's list of runs (%d) names bob's %s: %s", code, jid, raw)
	}
	if code, _, raw := a.runner(t, alice, "state.orch_resume", []string{jid}, map[string]any{"from": "ping_db"}); code == http.StatusOK {
		t.Errorf("alice resumed bob's orchestration: %s", raw)
	}
	if after := len(a.jobsReaching(t, "db1.example")); after != before {
		t.Errorf("alice's resume sent %d more jobs to db1", after-before)
	}

	// bob still sees his own.
	if code, _, raw := a.runner(t, bob, "state.orch_show", []string{jid}, nil); code != http.StatusOK {
		t.Errorf("bob could not show his own run: %d %s", code, raw)
	}
}

// **Kill.** The API asked the hub to kill as itself, and the hub judged
// the API's grant against the job: anybody with `jobs.kill` stopped any
// job. alice stopped one bound for db2.
func TestAnOperatorThroughTheAPIKillsOnlyJobsTheyMay(t *testing.T) {
	a := newAPILab(t, nil)
	alice, bob := a.login(t, "alice"), a.login(t, "bob")

	submit := func(target string) string {
		t.Helper()
		res, err := a.operator(t, "api").Submit(context.Background(), transport.SubmitRequest{
			Target: target, Fun: "test.ping", OnBehalfOf: "local:bob",
		})
		if err != nil {
			t.Fatal(err)
		}
		return res.JID
	}

	theirs := submit("db2.example")
	if code, body := a.call(t, http.MethodDelete, api.PathJob+theirs, alice, ""); code == http.StatusOK {
		t.Errorf("alice, narrowed to web*, killed a job bound for db2: %s", body)
	}
	if j, err := a.server.Jobs.Get(job.ID(theirs)); err != nil || j.State == job.Aborted {
		t.Errorf("the job alice was refused is %v (%v)", j, err)
	}
	// bob may, and does.
	if code, body := a.call(t, http.MethodDelete, api.PathJob+theirs, bob, ""); code != http.StatusOK {
		t.Errorf("bob could not kill a job bound for db2: %d %s", code, body)
	}
}
