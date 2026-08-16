package master

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/archive"
	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/logging"
	"github.com/edlitmus/halite/internal/returner"
	"github.com/edlitmus/halite/internal/transport"
)

// fleet is a control plane wired up over real mTLS, plus the CA behind it.
type fleet struct {
	ts     *httptest.Server
	server *Server
	store  *ca.Store
	pki    string
	states string
}

func newFleet(t *testing.T, cfg Config) *fleet {
	t.Helper()
	dir := t.TempDir()
	pki := filepath.Join(dir, "pki")
	states := filepath.Join(dir, "states")
	pillarDir := filepath.Join(dir, "pillar")

	for _, d := range []string{states, pillarDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, states, "top.sls", "base:\n  '*':\n    - demo\n")
	writeFile(t, states, "demo.sls", "demo:\n  cmd.run:\n    - name: \"true\"\n")
	writeFile(t, pillarDir, "top.sls", "base:\n  '*':\n    - common\n")
	writeFile(t, pillarDir, "common.sls", "greeting: hi\n")

	store := &ca.Store{Dir: pki}
	if err := store.Init("test ca", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.IssueServer("127.0.0.1", []string{"127.0.0.1"}, time.Hour); err != nil {
		t.Fatal(err)
	}

	cfg.PKIDir = pki
	cfg.StatesRoot = states
	cfg.PillarRoot = pillarDir
	cfg.withDefaults()
	server := New(cfg, logging.Discard())

	ts := httptest.NewUnstartedServer(server.Handler())
	tlsCfg, err := transport.ServerTLS(
		filepath.Join(pki, "master.crt"), filepath.Join(pki, "master.key"), filepath.Join(pki, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	ts.TLS = tlsCfg
	ts.StartTLS()
	t.Cleanup(ts.Close)

	return &fleet{ts: ts, server: server, store: store, pki: pki, states: states}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// host is the address clients dial.
func (f *fleet) host() string { return strings.TrimPrefix(f.ts.URL, "https://") }

// anonymousClient has the CA as its trust root but no certificate of its own.
func (f *fleet) anonymousClient(t *testing.T) *transport.Client {
	t.Helper()
	tlsCfg, err := transport.ClientTLS("", "", filepath.Join(f.pki, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	return transport.NewJSONClient(f.host(), tlsCfg, 10*time.Second)
}

// enrolledClient enrolls an identity, accepts it, and returns a client
// holding the issued certificate.
func (f *fleet) enrolledClient(t *testing.T, id string) *transport.Client {
	t.Helper()
	key, keyPEM, err := ca.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := ca.NewCSR(key, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp transport.EnrollResponse
	err = f.anonymousClient(t).Post(context.Background(), transport.PathEnroll,
		transport.EnrollRequest{ID: id, CSR: string(csrPEM)}, &resp)
	if err != nil {
		t.Fatalf("enroll %s: %v", id, err)
	}
	if resp.Cert == "" {
		if _, err := f.store.Accept(id); err != nil {
			t.Fatal(err)
		}
		err = f.anonymousClient(t).Post(context.Background(), transport.PathEnroll,
			transport.EnrollRequest{ID: id, CSR: string(csrPEM)}, &resp)
		if err != nil {
			t.Fatalf("re-enroll %s: %v", id, err)
		}
	}
	return f.clientFrom(t, id, keyPEM, []byte(resp.Cert))
}

// adminClient issues an operator certificate directly from the CA.
func (f *fleet) adminClient(t *testing.T, name string) *transport.Client {
	t.Helper()
	dir := t.TempDir()
	if err := f.store.IssueLocal(dir, "admin", name, ca.RoleAdmin, nil, time.Hour); err != nil {
		t.Fatal(err)
	}
	tlsCfg, err := transport.ClientTLS(
		filepath.Join(dir, "admin.crt"), filepath.Join(dir, "admin.key"), filepath.Join(f.pki, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	return transport.NewJSONClient(f.host(), tlsCfg, 10*time.Second)
}

func (f *fleet) clientFrom(t *testing.T, id string, keyPEM, certPEM []byte) *transport.Client {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, id+".key", string(keyPEM))
	writeFile(t, dir, id+".crt", string(certPEM))
	tlsCfg, err := transport.ClientTLS(
		filepath.Join(dir, id+".crt"), filepath.Join(dir, id+".key"), filepath.Join(f.pki, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	return transport.NewJSONClient(f.host(), tlsCfg, 10*time.Second)
}

func hello(t *testing.T, client *transport.Client, grains map[string]any) {
	t.Helper()
	err := client.Post(context.Background(), transport.PathHello,
		transport.HelloRequest{Grains: grains, Version: "test"}, nil)
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
}

func TestEnrollmentIsPendingUntilAccepted(t *testing.T) {
	f := newFleet(t, Config{})
	key, _, err := ca.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := ca.NewCSR(key, "web1", nil)
	if err != nil {
		t.Fatal(err)
	}

	var resp transport.EnrollResponse
	req := transport.EnrollRequest{ID: "web1", CSR: string(csrPEM)}
	if err := f.anonymousClient(t).Post(context.Background(), transport.PathEnroll, req, &resp); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if resp.State != string(ca.StatePending) || resp.Cert != "" {
		t.Fatalf("first enrollment returned %q with cert=%v, want pending and no certificate",
			resp.State, resp.Cert != "")
	}

	if _, err := f.store.Accept("web1"); err != nil {
		t.Fatal(err)
	}
	if err := f.anonymousClient(t).Post(context.Background(), transport.PathEnroll, req, &resp); err != nil {
		t.Fatalf("re-enroll: %v", err)
	}
	if resp.State != string(ca.StateAccepted) || resp.Cert == "" {
		t.Errorf("after acceptance got %q with cert=%v, want accepted with a certificate",
			resp.State, resp.Cert != "")
	}
}

func TestAutoAcceptIssuesImmediately(t *testing.T) {
	f := newFleet(t, Config{AutoAccept: true})
	key, _, _ := ca.GenerateKey()
	csrPEM, _ := ca.NewCSR(key, "web1", nil)

	var resp transport.EnrollResponse
	err := f.anonymousClient(t).Post(context.Background(), transport.PathEnroll,
		transport.EnrollRequest{ID: "web1", CSR: string(csrPEM)}, &resp)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	if resp.Cert == "" {
		t.Error("auto-accept must return a certificate on the first request")
	}
}

func TestUnauthenticatedRequestsAreRefused(t *testing.T) {
	f := newFleet(t, Config{})
	client := f.anonymousClient(t)

	for _, path := range []string{
		transport.PathHello, transport.PathJobs, transport.PathPillar,
		transport.PathAgents, transport.PathDispatch,
	} {
		err := client.Get(context.Background(), path, nil)
		if err == nil {
			t.Errorf("%s was served without a client certificate", path)
			continue
		}
		if !strings.Contains(err.Error(), "401") {
			t.Errorf("%s: got %v, want 401", path, err)
		}
	}
}

func TestAgentCannotDispatch(t *testing.T) {
	f := newFleet(t, Config{})
	agent := f.enrolledClient(t, "web1")

	err := agent.Post(context.Background(), transport.PathDispatch,
		transport.DispatchRequest{Target: "*", Kind: transport.KindHighstate}, nil)
	if err == nil {
		t.Fatal("an agent certificate must not be able to dispatch work")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("got %v, want 403", err)
	}
}

func TestDispatchTargetsMatchingAgentsOnly(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")
	db := f.enrolledClient(t, "db1")
	hello(t, web, map[string]any{"id": "web1", "os_family": "FreeBSD"})
	hello(t, db, map[string]any{"id": "db1", "os_family": "Debian"})
	admin := f.adminClient(t, "ed")

	var resp transport.DispatchResponse
	err := admin.Post(context.Background(), transport.PathDispatch,
		transport.DispatchRequest{Target: "os_family:FreeBSD", Kind: transport.KindGrains}, &resp)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(resp.Agents) != 1 || resp.Agents[0] != "web1" {
		t.Fatalf("dispatched to %v, want [web1]", resp.Agents)
	}

	var jobs []transport.Job
	if err := web.Get(context.Background(), transport.PathJobs, &jobs); err != nil {
		t.Fatalf("web1 poll: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != resp.JobID {
		t.Errorf("web1 got %v, want the dispatched job", jobs)
	}
}

func TestAgentIdentityComesFromItsCertificate(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")
	// A host claiming to be someone else must not become that identity.
	hello(t, web, map[string]any{"id": "master", "os_family": "FreeBSD"})
	admin := f.adminClient(t, "ed")

	var agents []transport.Agent
	if err := admin.Get(context.Background(), transport.PathAgents, &agents); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	if agents[0].ID != "web1" {
		t.Errorf("agent id = %q, want web1", agents[0].ID)
	}
	if got := agents[0].Grains["id"]; got != "web1" {
		t.Errorf("id grain = %v, want web1 — a spoofed grain must be overwritten", got)
	}
}

func TestResultsAreOnlyAcceptedFromTargetedAgents(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")
	db := f.enrolledClient(t, "db1")
	hello(t, web, map[string]any{"id": "web1", "os_family": "FreeBSD"})
	hello(t, db, map[string]any{"id": "db1", "os_family": "Debian"})
	admin := f.adminClient(t, "ed")

	var resp transport.DispatchResponse
	err := admin.Post(context.Background(), transport.PathDispatch,
		transport.DispatchRequest{Target: "web1", Kind: transport.KindGrains}, &resp)
	if err != nil {
		t.Fatal(err)
	}

	// db1 was never sent this job, so its answer must be refused.
	err = db.Post(context.Background(), transport.PathResults,
		transport.JobResult{JobID: resp.JobID, Ok: true}, nil)
	if err == nil {
		t.Fatal("a result from an untargeted agent must be refused")
	}

	if err := web.Post(context.Background(), transport.PathResults,
		transport.JobResult{JobID: resp.JobID, Ok: true, Succeeded: 1}, nil); err != nil {
		t.Fatalf("web1 result: %v", err)
	}

	var info transport.JobInfo
	if err := admin.Get(context.Background(), transport.PathJobInfo+resp.JobID, &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Results) != 1 || info.Results[0].AgentID != "web1" {
		t.Errorf("results = %v, want one from web1", info.Results)
	}
}

func TestPollReturnsAsSoonAsWorkArrives(t *testing.T) {
	f := newFleet(t, Config{PollTimeout: 10 * time.Second})
	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})
	admin := f.adminClient(t, "ed")

	polled := make(chan []transport.Job, 1)
	go func() {
		var jobs []transport.Job
		if err := web.Get(context.Background(), transport.PathJobs, &jobs); err != nil {
			polled <- nil
			return
		}
		polled <- jobs
	}()

	// Give the poll time to block before dispatching into it.
	time.Sleep(100 * time.Millisecond)
	var resp transport.DispatchResponse
	if err := admin.Post(context.Background(), transport.PathDispatch,
		transport.DispatchRequest{Target: "*", Kind: transport.KindGrains}, &resp); err != nil {
		t.Fatal(err)
	}

	select {
	case jobs := <-polled:
		if len(jobs) != 1 {
			t.Fatalf("poll returned %d jobs, want 1", len(jobs))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not return when work was dispatched")
	}
}

func TestPollReturnsEmptyOnTimeout(t *testing.T) {
	f := newFleet(t, Config{PollTimeout: 200 * time.Millisecond})
	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})

	var jobs []transport.Job
	start := time.Now()
	if err := web.Get(context.Background(), transport.PathJobs, &jobs); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("got %d jobs, want none", len(jobs))
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Errorf("poll returned after %s, want it held open for the timeout", elapsed)
	}
}

func TestStaleQueuedJobsAreDropped(t *testing.T) {
	f := newFleet(t, Config{PollTimeout: 200 * time.Millisecond, JobTTL: 50 * time.Millisecond})
	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})
	admin := f.adminClient(t, "ed")

	if err := admin.Post(context.Background(), transport.PathDispatch,
		transport.DispatchRequest{Target: "*", Kind: transport.KindGrains}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // outlive the TTL before collecting

	var jobs []transport.Job
	if err := web.Get(context.Background(), transport.PathJobs, &jobs); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("got %d jobs, want none — work older than the TTL must not run", len(jobs))
	}
}

func TestPillarIsRenderedForTheCallingAgent(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})

	var data map[string]any
	if err := web.Get(context.Background(), transport.PathPillar, &data); err != nil {
		t.Fatalf("pillar: %v", err)
	}
	if data["greeting"] != "hi" {
		t.Errorf("pillar = %v, want greeting=hi", data)
	}
}

func TestPillarNeedsGrainsFirst(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")

	err := web.Get(context.Background(), transport.PathPillar, &map[string]any{})
	if err == nil {
		t.Fatal("pillar must not render before the agent has reported grains")
	}
	if !strings.Contains(err.Error(), "409") {
		t.Errorf("got %v, want 409", err)
	}
}

func TestStateTreeIsServedAsAnArchive(t *testing.T) {
	f := newFleet(t, Config{})
	web := f.enrolledClient(t, "web1")
	hello(t, web, map[string]any{"id": "web1"})

	body, err := web.Stream(context.Background(), transport.PathStateTree)
	if err != nil {
		t.Fatalf("state tree: %v", err)
	}
	defer body.Close()

	dest := t.TempDir()
	if err := archive.UnpackTarGz(body, dest); err != nil {
		t.Fatalf("unpack: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "top.sls"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "demo") {
		t.Errorf("top.sls = %q", got)
	}
}

func TestDispatchRejectsMalformedWork(t *testing.T) {
	f := newFleet(t, Config{})
	admin := f.adminClient(t, "ed")

	cases := map[string]transport.DispatchRequest{
		"no target":               {Kind: transport.KindHighstate},
		"unknown kind":            {Target: "*", Kind: "rm -rf"},
		"apply with sls":          {Target: "*", Kind: transport.KindApply},
		"call without a function": {Target: "*", Kind: transport.KindCall},
	}
	for name, req := range cases {
		if err := admin.Post(context.Background(), transport.PathDispatch, req, nil); err == nil {
			t.Errorf("%s: dispatch was accepted, want a 400", name)
		}
	}
}

func TestShutdownIsGraceful(t *testing.T) {
	dir := t.TempDir()
	store := &ca.Store{Dir: dir}
	if err := store.Init("test ca", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.IssueServer("127.0.0.1", []string{"127.0.0.1"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	server := New(Config{
		Addr: "127.0.0.1:0", PKIDir: dir, StatesRoot: dir, PillarRoot: dir,
	}, logging.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()

	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("shutdown returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// slowSink takes its time over every delivery, to catch a shutdown that
// exits without waiting for the returner drain.
type slowSink struct {
	mu        sync.Mutex
	delivered int
}

func (s *slowSink) Name() string { return "slow" }
func (s *slowSink) Return(returner.Record) error {
	time.Sleep(200 * time.Millisecond)
	s.mu.Lock()
	s.delivered++
	s.mu.Unlock()
	return nil
}
func (s *slowSink) Close() error { return nil }

func TestShutdownWaitsForTheReturnerDrain(t *testing.T) {
	dir := t.TempDir()
	store := &ca.Store{Dir: dir}
	if err := store.Init("test ca", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := store.IssueServer("127.0.0.1", []string{"127.0.0.1"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	sink := &slowSink{}
	server := New(Config{
		Addr: "127.0.0.1:0", PKIDir: dir, StatesRoot: dir, PillarRoot: dir,
		Returners: []returner.Returner{sink},
	}, logging.Discard())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	// A record accepted just before shutdown must be flushed before Run
	// returns — after that, main exits and the record is gone.
	server.returners.Submit(returner.Record{Time: time.Now().UTC()})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("shutdown returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.delivered != 1 {
		t.Error("Run returned before the returner drain finished; the record would be lost at exit")
	}
}

func TestEnrollmentIsRefusedOverThePendingCap(t *testing.T) {
	f := newFleet(t, Config{MaxPendingEnrollments: 1})

	enroll := func(id string) (transport.EnrollResponse, error) {
		key, _, err := ca.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		csrPEM, err := ca.NewCSR(key, id, nil)
		if err != nil {
			t.Fatal(err)
		}
		var resp transport.EnrollResponse
		err = f.anonymousClient(t).Post(context.Background(), transport.PathEnroll,
			transport.EnrollRequest{ID: id, CSR: string(csrPEM)}, &resp)
		return resp, err
	}

	if resp, err := enroll("web1"); err != nil || resp.State != string(ca.StatePending) {
		t.Fatalf("first enrollment: state=%q err=%v", resp.State, err)
	}

	// The cap is full; the next identity must be told to come back rather
	// than being allowed to grow pending/ without bound.
	_, err := enroll("web2")
	if err == nil {
		t.Fatal("an enrollment over the pending cap must be refused")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("got %v, want 503 — a full queue is the server's condition, not the agent's mistake", err)
	}
}
