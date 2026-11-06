package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/account"
	"github.com/edlitmus/halite/internal/api"
	"github.com/edlitmus/halite/internal/apitoken"
	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/policy"
	"github.com/edlitmus/halite/internal/redact"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// Issue 659, and the subsection under DIVERGENCE 5.598: the hub masks
// the registered secrets in a return before it keeps it, and so before
// every place a return goes from there.

// returnSecret is the registered value. Long enough for Scrub, and
// spelled so that no other text in a return contains it by accident.
const returnSecret = "s3cr3t-value-123"

// secretSource is an external pillar source whose values are secret, as
// a secrets manager's are by default (5.257). It is how the hub's set is
// taught the value here: through a real pillar compile that a node asked
// for, which is the path a running hub takes, and not by calling Add.
type secretSource struct{}

func (secretSource) Name() string   { return "vault" }
func (secretSource) FailSoft() bool { return false }
func (secretSource) Secret() bool   { return true }
func (secretSource) Pillar(context.Context, pillar.ExtRequest) (*value.Map, error) {
	m := value.NewMap(1)
	m.Set("db_password", returnSecret)
	return m, nil
}

// returnSecretPolicy is read by the hub and by the API. `ed` is the
// lab's operator; `cert:CN=api` is halite-api, acting for `local:carol`,
// who may run cmd.run and read jobs.
const returnSecretPolicy = `
roles:
  administrator:
    - target: '*'
      functions: ['*', 'cmd.run']
    - target: '*'
      runners: ['*', 'pillar.show_pillar']
  api-service:
    - target: '*'
      functions: ['*', 'cmd.run']
    - target: '*'
      runners: ['*']
      act_for: ['local:*']
bindings:
  - principal: 'cert:CN=ed'
    roles: ['administrator']
  - principal: 'cert:CN=api'
    roles: ['api-service']
  - principal: 'local:carol'
    roles: ['administrator']
`

// fullReturner is an event returner that keeps each event whole, where
// recordingReturner keeps only the tag: the question here is what the
// event carried.
type fullReturner struct {
	mu  sync.Mutex
	got []eventbus.Event
}

func (r *fullReturner) Name() string                              { return "full" }
func (r *fullReturner) Return(context.Context, *job.Return) error { return nil }
func (r *fullReturner) Close() error                              { return nil }
func (r *fullReturner) Event(_ context.Context, e *eventbus.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, *e)
	return nil
}

func (r *fullReturner) events() []eventbus.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]eventbus.Event(nil), r.got...)
}

// returnSecretLab is a hub with a job cache, a bus, a pillar whose
// external source is secret, a relay hook that records what it would
// forward, an event returner, and an API in front of it all.
type returnSecretLab struct {
	*lab
	api       *httptest.Server
	shipped   *fullReturner
	forwarded func() []*job.Return
	node      *transport.Client
	op        *transport.Client
}

func newReturnSecretLab(t *testing.T) *returnSecretLab {
	t.Helper()
	l := newLab(t).withJobs(t).withEvents(t).withPillar(t, map[string]string{
		"top.sls":    "base:\n  '*':\n    - common\n",
		"common.sls": "greeting: hello\n",
	})
	p, _, err := policy.Load([]byte(returnSecretPolicy), "policy.yaml")
	if err != nil {
		t.Fatal(err)
	}
	catalogued(p)
	l.server.Policy = p

	// Wired as cmd/halite-hub/pillaropts.go wires it: one set, taught by
	// OnSecret and read through Secrets.
	set := redact.New()
	l.server.Pillar.Ext = []pillar.ExtSource{secretSource{}}
	l.server.Pillar.OnSecret = set.Add
	l.server.Pillar.Secrets = set

	// A relay's OnReturn, recording instead of forwarding.
	var mu sync.Mutex
	var forwarded []*job.Return
	l.server.OnReturn = func(ret *job.Return) {
		mu.Lock()
		defer mu.Unlock()
		c := *ret
		forwarded = append(forwarded, &c)
	}

	shipped := &fullReturner{}
	shipper := &EventReturn{Server: l.server, Returner: shipped, From: eventbus.Earliest, Batch: 50}
	runInBackground(t, shipper.Run)

	ap, _, err := policy.Load([]byte(returnSecretPolicy), "policy.yaml")
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
		Accounts:      &account.File{Accounts: map[string]*account.Account{"carol": {Name: "carol", Hash: hash}}},
		Tokens:        tokens,
		Policy:        ap,
		Hub:           l.operator(t, "api"),
		Metrics:       metrics.NewRegistry(),
		TokenLifetime: time.Hour,
		TokenIdle:     30 * time.Minute,
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	return &returnSecretLab{
		lab: l, api: ts, shipped: shipped,
		forwarded: func() []*job.Return {
			mu.Lock()
			defer mu.Unlock()
			return append([]*job.Return(nil), forwarded...)
		},
		node: l.enrolled(t, "web1.example"),
		op:   l.operator(t, "ed"),
	}
}

// run asks for cmd.run on web1, has web1 ask for its pillar first as a
// node does before every job, and files the return given.
func (r *returnSecretLab) run(t *testing.T, cmd string, output json.RawMessage) string {
	t.Helper()
	sub, err := r.op.Submit(context.Background(), transport.SubmitRequest{
		Target: "web1.example", TargetKind: "L", Fun: "cmd.run", Arg: []string{cmd}, Offline: "queue",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.node.Pillar(context.Background(), transport.PillarRequest{
		NodeID: "web1.example", Env: "base", Grains: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The precondition, checked rather than assumed: the compile the node
	// asked for is what registered the value, on the hub and on the node.
	if res.Secrets == nil || !slices.Contains(*res.Secrets, returnSecret) {
		t.Fatalf("the hub's compile did not name %q as secret: %v", returnSecret, res.Secrets)
	}
	if err := r.node.Return(context.Background(), job.Return{
		JID: job.ID(sub.JID), NodeID: "web1.example", Fun: "cmd.run", FunArgs: []string{cmd},
		Success: true, Return: output, DurationMS: 3, Schema: job.ReturnSchema,
	}); err != nil {
		t.Fatal(err)
	}
	return sub.JID
}

func (r *returnSecretLab) apiGet(t *testing.T, path string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": "carol", "password": "hunter2"})
	res, err := http.Post(r.api.URL+api.PathLogin, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var login api.LoginResponse
	err = json.NewDecoder(res.Body).Decode(&login)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, r.api.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+login.Token)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var b bytes.Buffer
	if _, err := b.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s through the API answered %d: %s", path, res.StatusCode, b.String())
	}
	return b.String()
}

// everywhere collects every place the return went, by name, with what
// each one holds as text. It waits for the asynchronous ones -- the
// event, the event returner, the relay forward -- to have happened, so
// that "the secret is not there" is never "it had not arrived yet".
func (r *returnSecretLab) everywhere(t *testing.T, jid string) map[string]string {
	t.Helper()
	out := map[string]string{}

	// The job cache, as bytes on disk: every file under the job's
	// directory, not only the decoded return, so a copy kept anywhere
	// else in the record is found too.
	var disk strings.Builder
	dir := r.server.Jobs.Dir()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.Contains(path, jid) {
			return err
		}
		b, err := os.ReadFile(path)
		disk.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	out["job cache on disk"] = disk.String()

	st, err := r.op.JobStatus(context.Background(), jid)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	out["hub GET /v1/jobs/{jid}"] = string(b)

	for _, fun := range []string{"jobs.lookup_jid", "jobs.list_job", "jobs.print_job"} {
		b, _ := json.Marshal(returned(t, call(t, r.op, fun, jid)))
		out["hub runner "+fun] = string(b)
	}

	out["API GET /v1/jobs/{jid}"] = r.apiGet(t, api.PathJob+jid)

	tag := tagJobRet(jid, "web1.example")
	events := waitForEvents(t, r.op, []string{tag})
	b, _ = json.Marshal(events)
	out["ret event"] = string(b)

	deadline := time.Now().Add(5 * time.Second)
	for {
		var shipped []eventbus.Event
		for _, e := range r.shipped.events() {
			if strings.Contains(e.Tag, jid) {
				shipped = append(shipped, e)
			}
		}
		var fwd []*job.Return
		for _, ret := range r.forwarded() {
			if string(ret.JID) == jid {
				fwd = append(fwd, ret)
			}
		}
		if (len(shipped) > 0 && len(fwd) > 0) || time.Now().After(deadline) {
			if len(shipped) == 0 {
				t.Fatalf("the event returner shipped nothing for %s", jid)
			}
			if len(fwd) == 0 {
				t.Fatalf("the relay hook forwarded nothing for %s", jid)
			}
			b, _ := json.Marshal(shipped)
			out["event returner"] = string(b)
			b, _ = json.Marshal(fwd)
			out["relay forward (OnReturn)"] = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return out
}

// **A registered secret in a cmd.run return is masked wherever the
// return goes.** Before issue 659 the hub kept the return as the node
// sent it (5.598), so the password a `cat` printed was in the cache on
// disk, in both job routes, in the three `jobs.*` runners and through the
// API.
//
// The command does not contain the secret; its output does. A command
// that does contain one is in the job's own record and its `new` event as
// the operator typed it, which is the job's arguments and not its return,
// and is recorded in 5.598's subsection as not done.
//
// The `ret` event and the event returner never carried the return, and
// are checked so that a change which adds it there has to mask it too.
func TestARegisteredSecretIsMaskedInEveryRouteAReturnTakes(t *testing.T) {
	r := newReturnSecretLab(t)
	output, _ := json.Marshal("db_password: " + returnSecret + "\nport: 5432")
	jid := r.run(t, "cat /etc/app.conf", output)

	for where, text := range r.everywhere(t, jid) {
		if strings.Contains(text, returnSecret) {
			t.Errorf("%s holds the registered secret:\n%s", where, text)
		}
	}

	// And the output is still there: masked, not withheld. 5.268's point.
	looked := mapOf(t, returned(t, call(t, r.op, "jobs.lookup_jid", jid)))
	got, _ := looked.Get("web1.example")
	if want := "db_password: " + redact.Placeholder + "\nport: 5432"; got != want {
		t.Errorf("the cached output is %q, want %q", got, want)
	}
}

// fun_args is the node's copy of the arguments, and is masked with the
// return. Called directly, because through a hub the operator's own copy
// of the same arguments is in the job record anyway (see above).
func TestTheReturnsFunArgsAreMasked(t *testing.T) {
	set := redact.New()
	set.Add(returnSecret)
	s := &Server{Pillar: &PillarOptions{Secrets: set}}
	ret := &job.Return{FunArgs: []string{"echo " + returnSecret, "plain"}, Return: json.RawMessage(`true`)}
	s.scrubReturn(ret)
	if want := []string{"echo " + redact.Placeholder, "plain"}; !slices.Equal(ret.FunArgs, want) {
		t.Errorf("fun_args = %q, want %q", ret.FunArgs, want)
	}
	if string(ret.Return) != "true" {
		t.Errorf("a return with nothing to mask was rewritten: %s", ret.Return)
	}
}

// A hub with no pillar options decrypts nothing and has nothing to mask,
// and must not fail on a return because of it.
func TestAHubWithNoPillarKeepsReturnsAsTheyCame(t *testing.T) {
	s := &Server{}
	ret := &job.Return{FunArgs: []string{"echo " + returnSecret}, Return: json.RawMessage(`"` + returnSecret + `"`)}
	s.scrubReturn(ret)
	if ret.FunArgs[0] != "echo "+returnSecret || string(ret.Return) != `"`+returnSecret+`"` {
		t.Errorf("a hub with no set changed the return: %v %s", ret.FunArgs, ret.Return)
	}
}

// **A return that arrived masked is kept as it arrived.** A node masks a
// state's output itself (5.109), so most returns that reach the hub are
// already masked, and the hub must not damage them: the placeholder is
// not masked again, and nothing is re-encoded.
//
// The numbers are the ones a re-encode does change, measured through
// value.DecodeJSON and EncodeJSON: `1.0` becomes `1`, `-0` becomes `0`,
// and an integer wider than 64 bits becomes `1.2345678901234568e+29`.
// With the return re-encoded whether or not anything matched, this test
// fails; key order and an int64 alone did not make it fail, which is why
// they are not the whole of it.
//
// A return that did hold a secret is re-encoded, and so carries those
// changes with the mask. The mixed case below has none of those numbers.
func TestAnAlreadyMaskedReturnIsKeptAsItArrived(t *testing.T) {
	r := newReturnSecretLab(t)

	masked := `{"zeta":"echo token=**********","alpha":{"stdout":"**********","big":9007199254740993},"list":["**********",1.0,-0,123456789012345678901234567890,null,true]}`
	jid := r.run(t, "echo token=**********", json.RawMessage(masked))
	cached, err := r.server.Jobs.Returns(job.ID(jid))
	if err != nil || len(cached) != 1 {
		t.Fatalf("returns: %v, %v", cached, err)
	}
	var got bytes.Buffer
	if err := json.Compact(&got, cached[0].Return); err != nil {
		t.Fatal(err)
	}
	if got.String() != masked {
		t.Errorf("an already-masked return was changed:\n got %s\nwant %s", got.String(), masked)
	}

	mixed := `{"zeta":"echo token=**********","alpha":{"stdout":"` + returnSecret + `","big":9007199254740993}}`
	jid = r.run(t, "true", json.RawMessage(mixed))
	cached, err = r.server.Jobs.Returns(job.ID(jid))
	if err != nil || len(cached) != 1 {
		t.Fatalf("returns: %v, %v", cached, err)
	}
	got.Reset()
	if err := json.Compact(&got, cached[0].Return); err != nil {
		t.Fatal(err)
	}
	if want := strings.Replace(mixed, returnSecret, redact.Placeholder, 1); got.String() != want {
		t.Errorf("a partly masked return:\n got %s\nwant %s", got.String(), want)
	}
}

// **A return the value model will not read fails closed.** A duplicate
// key is valid to encoding/json, so ReadJSON accepts it, and
// value.DecodeJSON refuses it: measured, not supposed. Keeping that
// return as it came would put the secret in the cache because the return
// was odd. It is kept as masked text instead, and is still valid JSON for
// the cache to write.
func TestAReturnTheValueModelCannotReadIsMaskedAsText(t *testing.T) {
	set := redact.New()
	set.Add(returnSecret)
	s := &Server{Pillar: &PillarOptions{Secrets: set}}
	raw := `{"out":"` + returnSecret + `","out":"again"}`
	ret := &job.Return{JID: "j", NodeID: "web1.example", Return: json.RawMessage(raw)}
	s.scrubReturn(ret)
	if strings.Contains(string(ret.Return), returnSecret) {
		t.Fatalf("the secret survived: %s", ret.Return)
	}
	var text string
	if err := json.Unmarshal(ret.Return, &text); err != nil {
		t.Fatalf("the masked return is not a JSON string: %s: %v", ret.Return, err)
	}
	if want := strings.Replace(raw, returnSecret, redact.Placeholder, 1); text != want {
		t.Errorf("masked text = %q, want %q", text, want)
	}

	// Nothing to mask: left exactly as it came, odd or not.
	odd := `{"out":"a","out":"b"}`
	ret = &job.Return{JID: "j", NodeID: "web1.example", Return: json.RawMessage(odd)}
	s.scrubReturn(ret)
	if string(ret.Return) != odd {
		t.Errorf("an unreadable return with no secret was rewritten: %s", ret.Return)
	}
}

// **A runner's cached return is masked; its direct answer is not.**
// `pillar.show_pillar` exists to return pillar, and the operator who
// called it is answered from the outcome, as before. The copy the hub
// keeps, which `jobs.lookup_jid` serves to whoever reads the job later,
// is masked like a node's.
func TestARunnersCachedReturnIsMaskedAndItsAnswerIsNot(t *testing.T) {
	r := newReturnSecretLab(t)
	// The node's grains, which show_pillar compiles against.
	defer r.connectSilent(t, r.node, "web1.example", `{}`)()

	res := call(t, r.op, "pillar.show_pillar", "web1.example")
	if !strings.Contains(string(res.Return), returnSecret) {
		t.Fatalf("show_pillar's own answer does not hold the pillar value; the test is not testing anything: %s", res.Return)
	}
	b, _ := json.Marshal(returned(t, call(t, r.op, "jobs.lookup_jid", res.JID)))
	if strings.Contains(string(b), returnSecret) {
		t.Errorf("the cached runner return holds the secret: %s", b)
	}
	if !strings.Contains(string(b), redact.Placeholder) {
		t.Errorf("the cached runner return has no mask in it, so it is not the return: %s", b)
	}
}
