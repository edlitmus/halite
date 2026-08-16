package returner

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/logging"
	"github.com/edlitmus/halite/internal/transport"
)

func sampleRecord(agent string) Record {
	return Record{
		Time: time.Now().UTC(),
		Job:  transport.Job{ID: "job1", Kind: transport.KindHighstate, Target: "*"},
		Result: transport.JobResult{
			JobID: "job1", AgentID: agent, Ok: true, Succeeded: 2, Changed: 1,
			States: []transport.StateOutcome{
				{ID: "nginx", Function: "pkg.installed", Ok: true, Comment: "installed"},
			},
		},
	}
}

func quietLogger() *logging.Logger { return logging.Discard() }

func TestParseBuildsKnownKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.ndjson")
	r, err := Parse("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name() != "file:"+path {
		t.Errorf("name = %q", r.Name())
	}
	r.Close()

	if _, err := Parse("webhook:https://example.com/hook"); err != nil {
		t.Errorf("webhook: %v", err)
	}
}

func TestParseRejectsBadSpecs(t *testing.T) {
	for _, spec := range []string{
		"", "file", "file:", "syslog:/dev/log", "webhook:", "webhook:not-a-url",
		"webhook:ftp://example.com",
	} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", spec)
		}
	}
}

// TestWebhookRequiresHTTPSOffTheLoopback guards the one returner that
// puts results on a network. A record carries the run's changes, which
// can hold anything a state templated out of pillar.
func TestWebhookRequiresHTTPSOffTheLoopback(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		wantErr  bool
	}{
		{"https://example.com/halite", false},
		{"http://example.com/halite", true},
		{"http://10.0.0.1:8080/halite", true},
		{"http://127.0.0.1:8080/halite", false},
		{"http://localhost:8080/halite", false},
		{"http://[::1]:8080/halite", false},
	} {
		_, err := NewWebhook(tc.endpoint)
		if tc.wantErr && err == nil {
			t.Errorf("NewWebhook(%q) succeeded; results would go out in the clear", tc.endpoint)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("NewWebhook(%q): %v", tc.endpoint, err)
		}
	}
}

// TestWebhookDoesNotFollowARedirectOffHost checks that an endpoint
// cannot point the record at somebody else after the fact.
func TestWebhookDoesNotFollowARedirectOffHost(t *testing.T) {
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the record reached the redirect target")
	}))
	defer elsewhere.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	hook, err := NewWebhook(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	hook.retryWait = time.Millisecond
	if err := hook.Return(sampleRecord("web1")); err == nil {
		t.Fatal("a redirect to another host must fail the delivery, not follow it")
	}
}

func TestFileReturnerAppendsOneJSONObjectPerLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "results.ndjson")
	r, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	for _, agent := range []string{"web1", "web2"} {
		if err := r.Return(sampleRecord(agent)); err != nil {
			t.Fatal(err)
		}
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), content)
	}
	var decoded Record
	if err := json.Unmarshal([]byte(lines[0]), &decoded); err != nil {
		t.Fatalf("line 1 is not JSON: %v", err)
	}
	if decoded.Result.AgentID != "web1" || decoded.Job.Kind != transport.KindHighstate {
		t.Errorf("decoded = %+v", decoded)
	}
	if len(decoded.Result.States) != 1 {
		t.Errorf("state outcomes lost in the round trip: %+v", decoded.Result)
	}
}

func TestFileReturnerIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.ndjson")
	r, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Results can contain file diffs, so the log is owner-only.
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %04o, want 0600", mode)
	}
}

func TestFileReturnerReopensAppending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.ndjson")
	first, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Return(sampleRecord("web1")); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := second.Return(sampleRecord("web2")); err != nil {
		t.Fatal(err)
	}

	content, _ := os.ReadFile(path)
	if count := strings.Count(string(content), "\n"); count != 2 {
		t.Errorf("got %d lines, want 2 — reopening truncated the log", count)
	}
}

func TestFileReturnerReportsASyncFailure(t *testing.T) {
	r, err := NewFile(filepath.Join(t.TempDir(), "results.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	// Swap the log for a pipe: writes succeed, fsync cannot. If Return
	// stops syncing, this passes silently and durability is gone.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	go io.Copy(io.Discard, pr) // keep the write side from filling up
	r.file.Close()
	r.file = pw
	defer r.Close()

	if err := r.Return(sampleRecord("web1")); err == nil {
		t.Error("a failed fsync must be reported: the record is not durable")
	}
}

func TestFileReturnerAfterCloseIsAnError(t *testing.T) {
	r, err := NewFile(filepath.Join(t.TempDir(), "results.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if err := r.Return(sampleRecord("web1")); err == nil {
		t.Error("writing to a closed returner must fail rather than panic")
	}
	if err := r.Close(); err != nil {
		t.Errorf("a second close must be harmless: %v", err)
	}
}

func TestWebhookPostsJSON(t *testing.T) {
	var (
		mu       sync.Mutex
		received []Record
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q", ct)
		}
		var rec Record
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			t.Errorf("body is not JSON: %v", err)
		}
		mu.Lock()
		received = append(received, rec)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r, err := NewWebhook(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Return(sampleRecord("web1")); err != nil {
		t.Fatalf("return: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 || received[0].Result.AgentID != "web1" {
		t.Errorf("received = %+v", received)
	}
}

func TestWebhookReportsAFailingEndpoint(t *testing.T) {
	var (
		mu       sync.Mutex
		attempts int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer server.Close()

	r, _ := NewWebhook(server.URL)
	r.retryWait = time.Millisecond
	if err := r.Return(sampleRecord("web1")); err == nil {
		t.Error("a 500 from the endpoint must be reported")
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != webhookRetries+1 {
		t.Errorf("made %d attempts, want the retry budget of %d used up", attempts, webhookRetries+1)
	}
}

func TestWebhookRetriesATransientFailure(t *testing.T) {
	var (
		mu       sync.Mutex
		attempts int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		n := attempts
		mu.Unlock()
		if n < 3 {
			http.Error(w, "not yet", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	r, err := NewWebhook(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	r.retryWait = time.Millisecond
	if err := r.Return(sampleRecord("web1")); err != nil {
		t.Fatalf("a failure within the retry budget must be ridden out: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 — success on the last retry", attempts)
	}
}

func TestWebhookNameHidesCredentials(t *testing.T) {
	r, err := NewWebhook("https://user:hunter2@example.com/hook?token=abcd1234")
	if err != nil {
		t.Fatal(err)
	}
	name := r.Name()
	for _, secret := range []string{"hunter2", "abcd1234"} {
		if strings.Contains(name, secret) {
			t.Errorf("%q leaks %q into logs", name, secret)
		}
	}
	if !strings.Contains(name, "example.com") {
		t.Errorf("name should still identify the endpoint: %q", name)
	}
}

func TestManagerDeliversToEveryReturner(t *testing.T) {
	dir := t.TempDir()
	first, _ := NewFile(filepath.Join(dir, "one.ndjson"))
	second, _ := NewFile(filepath.Join(dir, "two.ndjson"))
	manager := NewManager([]Returner{first, second}, quietLogger())

	done := make(chan struct{})
	finished := make(chan struct{})
	go func() { manager.Run(done); close(finished) }()

	manager.Submit(sampleRecord("web1"))
	close(done)
	<-finished

	for _, name := range []string{"one.ndjson", "two.ndjson"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(content) == 0 {
			t.Errorf("%s: got %q, %v", name, content, err)
		}
	}
}

func TestManagerFlushesQueuedRecordsOnShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.ndjson")
	sink, _ := NewFile(path)
	manager := NewManager([]Returner{sink}, quietLogger())

	// Queue before the drain goroutine starts, so everything is pending.
	for i := 0; i < 10; i++ {
		manager.Submit(sampleRecord("web1"))
	}
	done := make(chan struct{})
	close(done)
	manager.Run(done)

	content, _ := os.ReadFile(path)
	if count := strings.Count(string(content), "\n"); count != 10 {
		t.Errorf("wrote %d records, want all 10 flushed on shutdown", count)
	}
}

func TestManagerDropsRatherThanBlocks(t *testing.T) {
	manager := NewManager([]Returner{blackhole{}}, quietLogger())

	// Nothing is draining, so everything past the queue depth is dropped.
	// If Submit blocked, this would never return.
	for i := 0; i < queueDepth+50; i++ {
		manager.Submit(sampleRecord("web1"))
	}
	if dropped := manager.Dropped(); dropped != 50 {
		t.Errorf("dropped = %d, want 50", dropped)
	}
}

func TestManagerCountsDropsPerReturner(t *testing.T) {
	manager := NewManager([]Returner{blackhole{}, blackhole{}}, quietLogger())

	// Each sink has its own queue, so each drops the same overflow.
	for i := 0; i < queueDepth+50; i++ {
		manager.Submit(sampleRecord("web1"))
	}
	if dropped := manager.Dropped(); dropped != 100 {
		t.Errorf("dropped = %d, want 50 per sink", dropped)
	}
}

func TestSlowSinkDelaysOnlyItsOwnWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.ndjson")
	file, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blocked := stuck{release: make(chan struct{})}
	manager := NewManager([]Returner{blocked, file}, quietLogger())

	done := make(chan struct{})
	finished := make(chan struct{})
	go func() { manager.Run(done); close(finished) }()

	manager.Submit(sampleRecord("web1"))

	// The stuck sink is holding its own goroutine hostage; the file sink
	// must still get the record. One shared delivery loop fails here.
	deadline := time.After(10 * time.Second)
	for {
		content, _ := os.ReadFile(path)
		if strings.Count(string(content), "\n") == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the file sink never got the record while another sink was stuck")
		case <-time.After(10 * time.Millisecond):
		}
	}

	close(blocked.release)
	close(done)
	<-finished
}

func TestSubmitAfterShutdownDropsInsteadOfPanicking(t *testing.T) {
	sink, err := NewFile(filepath.Join(t.TempDir(), "results.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager([]Returner{sink}, quietLogger())
	done := make(chan struct{})
	close(done)
	manager.Run(done)

	manager.Submit(sampleRecord("web1")) // the queue is closed; this must not panic
	if dropped := manager.Dropped(); dropped != 1 {
		t.Errorf("dropped = %d, want the post-shutdown record counted", dropped)
	}
}

func TestShutdownStrandsNoAcceptedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.ndjson")
	sink, err := NewFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager([]Returner{sink}, quietLogger())

	done := make(chan struct{})
	finished := make(chan struct{})
	go func() { manager.Run(done); close(finished) }()

	// Submissions race the shutdown on purpose: every record must end up
	// either written or counted as dropped, never stranded in a channel.
	const submitters, each = 4, 50
	var wg sync.WaitGroup
	for i := 0; i < submitters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				manager.Submit(sampleRecord("web1"))
			}
		}()
	}
	close(done)
	wg.Wait()
	<-finished

	content, _ := os.ReadFile(path)
	written := strings.Count(string(content), "\n")
	if written+manager.Dropped() != submitters*each {
		t.Errorf("written %d + dropped %d != submitted %d — a record was stranded",
			written, manager.Dropped(), submitters*each)
	}
}

func TestManagerWithNoReturnersIsInert(t *testing.T) {
	manager := NewManager(nil, quietLogger())
	if manager.Configured() {
		t.Error("a manager with no returners must report itself unconfigured")
	}
	manager.Submit(sampleRecord("web1")) // must not panic or block
	done := make(chan struct{})
	close(done)
	manager.Run(done) // must return immediately
}

// blackhole accepts everything and keeps nothing.
type blackhole struct{}

func (blackhole) Name() string        { return "blackhole" }
func (blackhole) Return(Record) error { return nil }
func (blackhole) Close() error        { return nil }

// stuck blocks every delivery until released, like a blackholed webhook
// sitting in its connect timeout.
type stuck struct{ release chan struct{} }

func (s stuck) Name() string        { return "stuck" }
func (s stuck) Return(Record) error { <-s.release; return nil }
func (s stuck) Close() error        { return nil }
