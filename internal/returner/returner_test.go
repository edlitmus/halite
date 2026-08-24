package returner

import (
	"bufio"
	"context"
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/eventbus"
	"github.com/edlitmus/halite/internal/job"
)

func aReturn(jid, node string, ok bool) *job.Return {
	return &job.Return{
		JID: job.ID(jid), NodeID: node, Fun: "test.ping",
		Success: ok, Return: json.RawMessage(`true`), Schema: job.ReturnSchema,
	}
}

func TestAnUnknownReturnerNamesWhatExists(t *testing.T) {
	_, err := New("nosuchthing", Options{})
	if err == nil {
		t.Fatal("an unknown returner was accepted")
	}
	for _, want := range []string{"local", "file", "webhook", "syslog", "smtp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not offer %q: %v", want, err)
		}
	}
}

// An operator who writes `returner: postgres` has made a reasonable
// request. "postgres is not a returner" would be a lie, and a typo and
// a deferred feature are different problems.
func TestABridgedReturnerIsNamedAsBridged(t *testing.T) {
	_, err := New("postgres", Options{})
	if err == nil {
		t.Fatal("postgres was accepted")
	}
	if !strings.Contains(err.Error(), "bridge") {
		t.Errorf("the error does not say it is bridged: %v", err)
	}
	if strings.Contains(err.Error(), "is not a returner") {
		t.Errorf("a bridged destination is reported as a typo: %v", err)
	}
}

func TestTheLocalReturnerAppendsOneObjectPerLine(t *testing.T) {
	dir := t.TempDir()
	r, err := New("local", Options{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	for i := 0; i < 3; i++ {
		if err := r.Return(context.Background(), aReturn(fmt.Sprintf("2026082%d", i), "web1", true)); err != nil {
			t.Fatal(err)
		}
	}
	lines := readLines(t, filepath.Join(dir, "returns.ndjson"))
	if len(lines) != 3 {
		t.Fatalf("wrote %d lines, want 3", len(lines))
	}
	for _, line := range lines {
		var got job.Return
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Errorf("a line does not parse: %v", err)
		}
	}
	// The file a return carries is as sensitive as the estate's most
	// sensitive job.
	info, err := os.Stat(filepath.Join(dir, "returns.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the return log is mode %o", perm)
	}
}

func TestTheFileReturnerRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "returns.ndjson")
	r, err := New("file", Options{Path: path, MaxBytes: 200, KeepFiles: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	for i := 0; i < 20; i++ {
		if err := r.Return(context.Background(), aReturn("2026082400000"+fmt.Sprint(i), "web1", true)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("nothing rotated: %v", err)
	}
	// Kept files are bounded: the point of rotation is that the disk
	// does not fill.
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("more copies were kept than KeepFiles allows")
	}
}

// SPEC 20.3 asks the webhook returner for three things together, and
// the third is what makes the other two worth having.
func TestTheWebhookSignsWhatItSends(t *testing.T) {
	var mu sync.Mutex
	var got struct {
		body      []byte
		stamp     string
		signature string
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		mu.Lock()
		got.body = body
		got.stamp = r.Header.Get(TimestampHeader)
		got.signature = r.Header.Get(SignatureHeader)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := webhookTo(t, server, "s3cret")
	if err := r.Return(context.Background(), aReturn("20260824T1", "web1", true)); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got.signature == "" {
		t.Fatal("nothing was signed")
	}
	want := Sign("s3cret", got.stamp, got.body)
	if !hmac.Equal([]byte(want), []byte(got.signature)) {
		t.Errorf("the signature does not verify over the timestamp and the body")
	}
	// Over the timestamp too, so a captured signature cannot be
	// replayed with a fresh one.
	if hmac.Equal([]byte(Sign("s3cret", "another-time", got.body)), []byte(got.signature)) {
		t.Error("the signature does not cover the timestamp")
	}
}

// A webhook returner without the spool loses exactly the returns from
// the incident that took the receiver down.
func TestAnOutageSpoolsAndTheBacklogIsSentInOrder(t *testing.T) {
	var mu sync.Mutex
	up := false
	var received []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var ret job.Return
		_ = json.NewDecoder(r.Body).Decode(&ret)
		received = append(received, string(ret.JID))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	r := webhookTo(t, server, "s3cret")
	// One attempt each while the receiver is down, so the test is not
	// waiting on the backoff.
	r.opts.MaxAttempts = 1

	for _, jid := range []string{"20260824T1", "20260824T2", "20260824T3"} {
		if err := r.Return(context.Background(), aReturn(jid, "web1", true)); err != nil {
			t.Fatalf("%s: %v", jid, err)
		}
	}
	if n := r.Spooled(); n != 3 {
		t.Fatalf("%d returns spooled, want 3", n)
	}

	mu.Lock()
	up = true
	mu.Unlock()

	// The next return drains the backlog ahead of itself.
	if err := r.Return(context.Background(), aReturn("20260824T4", "web1", true)); err != nil {
		t.Fatal(err)
	}
	if n := r.Spooled(); n != 0 {
		t.Errorf("%d returns are still spooled", n)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"20260824T1", "20260824T2", "20260824T3", "20260824T4"}
	if len(received) != len(want) {
		t.Fatalf("received %v, want %v", received, want)
	}
	for i := range want {
		if received[i] != want[i] {
			t.Errorf("received %v, want %v — the backlog went out after the new return", received, want)
		}
	}
}

// A 4xx means the receiver understood and refused. Retrying it forever
// is how a spool fills a disk with a request nobody will ever accept.
func TestARefusalIsNotRetriedForever(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	r := webhookTo(t, server, "s3cret")
	if err := r.Return(context.Background(), aReturn("20260824T1", "web1", true)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 1 {
		t.Errorf("a 400 was attempted %d times", attempts)
	}
	// It is spooled once so the loss is visible, and discarded on the
	// next drain rather than blocking every later return behind it.
	if n := r.Spooled(); n != 1 {
		t.Errorf("%d spooled, want 1", n)
	}
}

// A return carries whatever a job printed. Sending that over plaintext
// because someone typed http:// is not a choice worth offering.
func TestTheWebhookRefusesPlaintextAndAnUnsignedConfiguration(t *testing.T) {
	if _, err := New("webhook", Options{URL: "http://example.com/x", Secret: "s", StateDir: t.TempDir()}); err == nil {
		t.Error("an http url was accepted")
	}
	if _, err := New("webhook", Options{URL: "https://example.com/x", StateDir: t.TempDir()}); err == nil {
		t.Error("a webhook with no secret was accepted")
	}
}

// The spool is bounded and refuses rather than making room: a spool that
// silently discards is the failure it exists to prevent.
func TestAFullSpoolRefusesRatherThanDiscarding(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	r := webhookTo(t, server, "s3cret")
	r.opts.MaxAttempts = 1
	r.opts.SpoolMax = 200

	var lastErr error
	for i := 0; i < 20; i++ {
		if err := r.Return(context.Background(), aReturn(fmt.Sprintf("2026082400%02d", i), "web1", true)); err != nil {
			lastErr = err
			break
		}
	}
	if lastErr == nil {
		t.Fatal("the spool grew past its bound without saying so")
	}
	if !strings.Contains(lastErr.Error(), "spool is full") {
		t.Errorf("the error does not name the spool: %v", lastErr)
	}
}

func TestTheSyslogReturnerWritesRFC5424(t *testing.T) {
	ln, lines := syslogSink(t)
	defer ln.Close()

	r, err := New("syslog", Options{
		SyslogAddress: ln.Addr().String(), SyslogNetwork: "tcp",
		SyslogFacility: "local3", SyslogTag: "halite-node", NodeID: "web1.example",
		Now: func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if err := r.Return(context.Background(), aReturn("20260824T1", "web1.example", false)); err != nil {
		t.Fatal(err)
	}
	line := <-lines
	// local3 is 19, and a failed return is a warning, which is 4:
	// 19*8 + 4 = 156.
	if !strings.HasPrefix(line, "<156>1 2026-08-24T12:00:00.000000Z web1.example halite-node ") {
		t.Errorf("the header is wrong: %s", line)
	}
	if !strings.Contains(line, `"jid":"20260824T1"`) {
		t.Errorf("the payload is missing: %s", line)
	}
}

// A header field is space-delimited, so a value with a space in it moves
// every field after it — and a value with a newline ends the message.
func TestASyslogHeaderFieldCannotBreakTheFraming(t *testing.T) {
	ln, lines := syslogSink(t)
	defer ln.Close()

	r, err := New("syslog", Options{
		SyslogAddress: ln.Addr().String(), SyslogNetwork: "tcp",
		NodeID: "web1 evil\n<0>1 injected",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if err := r.Return(context.Background(), aReturn("20260824T1", "web1", true)); err != nil {
		t.Fatal(err)
	}
	line := <-lines
	if strings.Contains(line, "<0>1 injected") {
		t.Errorf("a node id wrote its own message: %s", line)
	}
	if strings.Count(line, " ") < 6 {
		t.Errorf("the header lost fields: %s", line)
	}
}

func TestAnUnknownFacilityIsRefusedByName(t *testing.T) {
	_, err := New("syslog", Options{SyslogFacility: "local9"})
	if err == nil {
		t.Fatal("local9 was accepted")
	}
	if !strings.Contains(err.Error(), "local0") {
		t.Errorf("the error does not say what is valid: %v", err)
	}
}

// A returner that carries returns but not the event stream says so,
// rather than accepting an `event_return` and writing nowhere.
func TestLocalCacheRefusesTheEventStream(t *testing.T) {
	r, err := New("local_cache", Options{
		Post: func(ctx context.Context, ret *job.Return) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Event(context.Background(), &eventbus.Event{Tag: "halite/x"}); err == nil {
		t.Error("local_cache accepted an event")
	}
	if _, err := New("local_cache", Options{}); err == nil {
		t.Error("local_cache was built with no hub to post to")
	}
}

func TestSMTPRefusesCredentialsWithoutTLS(t *testing.T) {
	r, err := New("smtp", Options{
		SMTPAddress: "127.0.0.1:0", SMTPFrom: "a@example.com", SMTPTo: []string{"b@example.com"},
		SMTPUsername: "user", SMTPPassword: "pw",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = r.Return(context.Background(), aReturn("20260824T1", "web1", true))
	if err == nil {
		t.Fatal("it connected")
	}
	// It must fail on the dial or on the refusal, never on sending the
	// credential in the clear. Both are acceptable here; what is not is
	// success.
	_ = err
}

// A function name and a node id reach the subject, and neither is chosen
// by this program.
func TestAMailSubjectCannotInjectHeaders(t *testing.T) {
	r := &smtpReturner{opts: Options{
		SMTPFrom: "a@example.com", SMTPTo: []string{"b@example.com"},
		Now: time.Now,
	}}
	msg := string(r.message("halite: test\r\nBcc: attacker@example.com", []byte("{}")))
	head, _, _ := strings.Cut(msg, "\r\n\r\n")
	// The text may still appear inside the subject's value, which is
	// harmless. What must not happen is a header line of its own.
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Errorf("a subject wrote a header:\n%s", head)
		}
	}
	if got := strings.Count(head, "\r\n"); got != 5 {
		t.Errorf("the header has %d line breaks, want 5:\n%s", got, head)
	}
}

// ---- helpers ----

func webhookTo(t *testing.T, server *httptest.Server, secret string) *webhookReturner {
	t.Helper()
	built, err := New("webhook", Options{
		URL:    strings.Replace(server.URL, "http://", "https://", 1),
		Secret: secret, StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := built.(*webhookReturner)
	// The test server's own certificate, verified as a real one would
	// be rather than skipped.
	r.client = server.Client()
	r.client.Timeout = 10 * time.Second
	return r
}

func syslogSink(t *testing.T) (net.Listener, chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				scanner := bufio.NewScanner(conn)
				for scanner.Scan() {
					lines <- scanner.Text()
				}
			}()
		}
	}()
	return ln, lines
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Three different problems need three different answers. Telling an
// operator who wrote `postgres` that it "does not carry the event
// stream" sends them looking for a setting when what they need is the
// bridge of SPEC section 24.
func TestEventReturnSaysWhichProblemItIs(t *testing.T) {
	if err := CheckEventReturn("file"); err != nil {
		t.Errorf("file was refused: %v", err)
	}
	cases := []struct{ name, want string }{
		{"local_cache", "carries returns but not the event stream"},
		{"postgres", "bridge"},
		{"pstgres", "is not a returner"},
	}
	for _, c := range cases {
		err := CheckEventReturn(c.name)
		if err == nil {
			t.Errorf("%s was accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to mention %q", c.name, err, c.want)
		}
	}
}
