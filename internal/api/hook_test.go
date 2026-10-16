package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/log"
	"github.com/edlitmus/halite/internal/metrics"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
	"github.com/edlitmus/halite/internal/yaml"
)

func parseHooks(t *testing.T, src string) []*Hook {
	t.Helper()
	hooks, err := parseHooksErr(src)
	if err != nil {
		t.Fatal(err)
	}
	return hooks
}

func parseHooksErr(src string) ([]*Hook, error) {
	doc, _, err := yaml.Parse([]byte(src), yaml.Options{File: "api.yaml"})
	if err != nil {
		return nil, err
	}
	root, ok := doc.(*value.Map)
	if !ok {
		return nil, errFixture
	}
	raw, _ := root.Get("hooks")
	return ParseHooks(raw)
}

var errFixture = errString("the fixture is not a mapping")

type errString string

func (e errString) Error() string { return string(e) }

// The promise SPEC 22.2 makes is that no configuration produces an
// unauthenticated hook. It is kept by refusing to load one, not by a
// default that happens to be safe.
func TestAHookWithoutAuthenticationWillNotLoad(t *testing.T) {
	_, err := parseHooksErr("hooks:\n  deploy:\n    secret: s3cret\n")
	if err == nil {
		t.Fatal("a hook with no authentication method loaded")
	}
	if !strings.Contains(err.Error(), "no unauthenticated hook") {
		t.Errorf("the refusal says %q", err)
	}

	cases := map[string]string{
		"hmac with no secret":  "hooks:\n  a:\n    auth: hmac\n",
		"token with no secret": "hooks:\n  a:\n    auth: token\n",
		"an unknown method":    "hooks:\n  a:\n    auth: none\n    secret: x\n",
		"an unknown setting":   "hooks:\n  a:\n    auth: token\n    secret: x\n    nonsense: 1\n",
		"not a mapping":        "hooks:\n  - a\n",
	}
	for name, src := range cases {
		if _, err := parseHooksErr(src); err == nil {
			t.Errorf("%s loaded", name)
		}
	}
}

func TestHookDefaults(t *testing.T) {
	hooks := parseHooks(t, "hooks:\n  deploy:\n    auth: hmac\n    secret: s3cret\n")
	if len(hooks) != 1 {
		t.Fatalf("parsed %d hooks", len(hooks))
	}
	h := hooks[0]
	if h.Path != "deploy" || h.Auth != "hmac" {
		t.Errorf("parsed as %+v", h)
	}
	// The principal is derived from the path, so a reactor can
	// authorize on who sent it without the operator naming one.
	if h.Principal != "hook:deploy" {
		t.Errorf("the principal is %q", h.Principal)
	}
	if h.ReplayWindow != DefaultReplayWindow || h.MaxBody != DefaultHookBody {
		t.Errorf("the defaults are %+v", h)
	}
}

// hookLab is an API with one HMAC hook and a stub hub behind it.
func hookLab(t *testing.T, src string) (*lab, *stubHub) {
	t.Helper()
	l, hub := executeLab(t, operatorPolicy)
	l.server.Hooks = NewHooks(parseHooks(t, src))
	return l, hub
}

const hmacHook = "hooks:\n  deploy:\n    auth: hmac\n    secret: s3cret\n"

// sign produces the header pair a sender would.
func sign(secret, stamp, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(stamp))
	mac.Write([]byte("."))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func (l *lab) deliver(t *testing.T, path, body, stamp, signature string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, l.http.URL+"/v1/hook/"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if stamp != "" {
		req.Header.Set(DefaultTimestampHeader, stamp)
	}
	if signature != "" {
		req.Header.Set(DefaultSignatureHeader, signature)
	}
	return do(t, req)
}

func TestASignedDeliveryReachesTheBus(t *testing.T) {
	l, hub := hookLab(t, hmacHook)
	body := `{"version":"1.2"}`
	stamp := time.Now().UTC().Format(time.RFC3339)

	res, out := l.deliver(t, "deploy", body, stamp, sign("s3cret", stamp, body))
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("a signed delivery answered %d: %s", res.StatusCode, out)
	}
	if !strings.Contains(out, "halite/hook/deploy") {
		t.Errorf("the answer is %s", out)
	}

	// It reached the hub as an event, carrying the principal the
	// delivery authenticated as — so a reactor authorizes on that
	// rather than on the payload.
	var sent string
	for _, call := range hub.seen {
		if strings.Contains(call.body, "event.send") {
			sent = call.body
		}
	}
	if sent == "" {
		t.Fatal("nothing reached the bus")
	}
	if !strings.Contains(sent, "hook:deploy") {
		t.Errorf("the event does not carry the principal: %s", sent)
	}

	// The shape matters as much as the content: `event.send` declares
	// `data` as a mapping, so a payload handed over as a JSON string is
	// refused by the hub's own signature check. A fake hub accepts
	// either, which is how that reached a lab rather than a test.
	var call struct {
		Kwarg struct {
			Data map[string]any `json:"data"`
		} `json:"kwarg"`
	}
	if err := json.Unmarshal([]byte(sent), &call); err != nil {
		t.Fatalf("the call is not readable: %v", err)
	}
	inner, ok := call.Kwarg.Data["body"].(map[string]any)
	if !ok {
		t.Fatalf("the body is not a mapping: %#v", call.Kwarg.Data["body"])
	}
	if inner["version"] != "1.2" {
		t.Errorf("the body did not survive: %#v", inner)
	}
}

// The signature is over the raw bytes and the timestamp together. A
// captured signature replayed with a fresh timestamp must not verify.
func TestTheSignatureCoversTheBodyAndTheTimestamp(t *testing.T) {
	l, _ := hookLab(t, hmacHook)
	body := `{"version":"1.2"}`
	stamp := time.Now().UTC().Format(time.RFC3339)
	good := sign("s3cret", stamp, body)

	// A different body with the same signature.
	res, _ := l.deliver(t, "deploy", `{"version":"9.9"}`, stamp, good)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a tampered body answered %d", res.StatusCode)
	}
	// The same body with a different timestamp.
	later := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	res, _ = l.deliver(t, "deploy", body, later, good)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a signature replayed under a new timestamp answered %d", res.StatusCode)
	}
	// No signature at all.
	res, _ = l.deliver(t, "deploy", body, stamp, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unsigned delivery answered %d", res.StatusCode)
	}
}

// Outside the window, and replayed inside it: both refused, and the
// second is what a signature alone cannot catch.
func TestTheReplayWindowAndTheNonceCache(t *testing.T) {
	l, _ := hookLab(t, hmacHook)
	body := `{"a":1}`

	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	res, out := l.deliver(t, "deploy", body, old, sign("s3cret", old, body))
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a delivery an hour old answered %d", res.StatusCode)
	}
	if !strings.Contains(out, "replay window") {
		t.Errorf("the refusal says %q", out)
	}

	stamp := time.Now().UTC().Format(time.RFC3339)
	signature := sign("s3cret", stamp, body)
	res, _ = l.deliver(t, "deploy", body, stamp, signature)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("the first delivery answered %d", res.StatusCode)
	}
	res, out = l.deliver(t, "deploy", body, stamp, signature)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("the same delivery twice answered %d", res.StatusCode)
	}
	if !strings.Contains(out, "already been accepted") {
		t.Errorf("the replay refusal says %q", out)
	}
}

// A bearer-token hook takes a token and nothing else.
func TestATokenHookTakesABearerToken(t *testing.T) {
	l, _ := hookLab(t, "hooks:\n  ci:\n    auth: token\n    secret: s3cret\n")

	req, _ := http.NewRequest(http.MethodPost, l.http.URL+"/v1/hook/ci", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer s3cret")
	res, body := do(t, req)
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("a correct token answered %d: %s", res.StatusCode, body)
	}

	req, _ = http.NewRequest(http.MethodPost, l.http.URL+"/v1/hook/ci", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer wrong")
	res, _ = do(t, req)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong token answered %d", res.StatusCode)
	}
}

// An unconfigured path answers the way any unconfigured path does: a
// hook endpoint that distinguished "no such hook" from "wrong
// signature" would enumerate the hooks.
func TestAnUnconfiguredHookIsIndistinguishable(t *testing.T) {
	l, _ := hookLab(t, hmacHook)
	res, body := l.deliver(t, "nosuchhook", `{}`, "", "")
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("an unconfigured hook answered %d", res.StatusCode)
	}
	if strings.Contains(body, "deploy") {
		t.Errorf("the answer names a configured hook: %s", body)
	}
}

// The content-type allowlist and the body limit apply before anything
// is parsed.
func TestTheContentTypeAndBodyLimitApply(t *testing.T) {
	l, _ := hookLab(t, hmacHook)
	body := `{"a":1}`
	stamp := time.Now().UTC().Format(time.RFC3339)

	req, _ := http.NewRequest(http.MethodPost, l.http.URL+"/v1/hook/deploy", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set(DefaultTimestampHeader, stamp)
	req.Header.Set(DefaultSignatureHeader, sign("s3cret", stamp, body))
	res, _ := do(t, req)
	if res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("a disallowed content type answered %d", res.StatusCode)
	}

	l.server.Hooks.byPath["deploy"].MaxBody = 8
	big := strings.Repeat("x", 4096)
	res, _ = l.deliver(t, "deploy", big, stamp, sign("s3cret", stamp, big))
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("an oversized delivery answered %d", res.StatusCode)
	}
}

// One noisy sender must not be able to drive the bus.
func TestAHookIsRateLimited(t *testing.T) {
	l, _ := hookLab(t, "hooks:\n  deploy:\n    auth: token\n    secret: s3cret\n    rate_limit: 2\n")
	deliver := func() int {
		req, _ := http.NewRequest(http.MethodPost, l.http.URL+"/v1/hook/deploy", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer s3cret")
		res, _ := do(t, req)
		return res.StatusCode
	}
	limited := false
	for i := 0; i < 6; i++ {
		if deliver() == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("a burst was never rate limited")
	}
}

// A delivery that fails downstream must remain retryable. Recording the
// replay nonce when the signature verified, rather than when the
// delivery landed, turned a transient fault into a lost event: the
// sender's retry carries the same signature and was refused as a
// replay.
func TestADeliveryThatFailedDownstreamCanBeRetried(t *testing.T) {
	l, hub := hookLab(t, hmacHook)
	hub.runnerFails = "the hub is having a moment"

	body := `{"a":1}`
	stamp := time.Now().UTC().Format(time.RFC3339)
	signature := sign("s3cret", stamp, body)

	res, _ := l.deliver(t, "deploy", body, stamp, signature)
	if res.StatusCode == http.StatusAccepted {
		t.Fatalf("the delivery succeeded when the hub refused: %d", res.StatusCode)
	}

	// The sender retries with the same signature, and the hub is back.
	hub.runnerFails = ""
	res, out := l.deliver(t, "deploy", body, stamp, signature)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("the retry answered %d: %s", res.StatusCode, out)
	}

	// And once it has landed, the same delivery is a replay.
	res, _ = l.deliver(t, "deploy", body, stamp, signature)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a replay after a successful delivery answered %d", res.StatusCode)
	}
}

// `auth: mtls` could never succeed -- the API asks no client for a
// certificate -- and would have accepted any certificate if it had. It is
// refused when the configuration loads, saying why. DIVERGENCE 5.327.
func TestAnMTLSHookIsRefusedAtLoad(t *testing.T) {
	_, err := parseHooksErr("hooks:\n  deploy:\n    auth: mtls\n")
	if err == nil {
		t.Fatal("an mtls hook loaded, and no delivery to it can ever authenticate")
	}
	for _, want := range []string{"hook deploy", "auth: mtls", "client certificates", "hmac", "token"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// gatedHub is a hub whose first event.send waits for the test to let it
// go, so a second delivery can be sent while the first is provably in
// the middle of being delivered. Every later call answers at once.
type gatedHub struct {
	mu      sync.Mutex
	sends   int
	arrived chan struct{}
	release chan struct{}
	server  *httptest.Server
}

func newGatedHub(t *testing.T) *gatedHub {
	t.Helper()
	g := &gatedHub{arrived: make(chan struct{}), release: make(chan struct{})}
	g.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != transport.PathRunners {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		g.mu.Lock()
		g.sends++
		first := g.sends == 1
		g.mu.Unlock()
		if first {
			close(g.arrived)
			<-g.release
		}
		writeJSON(w, http.StatusOK, transport.RunnerResponse{
			JID: "20261015T000000.000001", Fun: "event.send", Success: true,
			Return: json.RawMessage(`true`),
		})
	}))
	t.Cleanup(g.server.Close)
	return g
}

func (g *gatedHub) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sends
}

// Two copies of one signed delivery, the second arriving while the first
// is being put on the bus. The check that a signature had not been
// delivered and the record that it had were either side of the call to
// the hub, so both copies passed and the event was delivered twice.
// DIVERGENCE 5.327.
func TestTwoCopiesOfOneDeliveryReachTheBusOnce(t *testing.T) {
	l := newLab(t)
	l.server.Metrics = metrics.NewRegistry()
	l.server.Hooks = NewHooks(parseHooks(t, hmacHook))
	hub := newGatedHub(t)
	l.server.Hub = &transport.Client{
		HubURL: hub.server.URL, CA: hub.server.Certificate(),
		ServerName: "127.0.0.1", Timeout: 10 * time.Second,
	}

	body := `{"version":"1.2"}`
	stamp := time.Now().UTC().Format(time.RFC3339)
	signature := sign("s3cret", stamp, body)

	first := make(chan int, 1)
	go func() {
		res, _ := l.deliver(t, "deploy", body, stamp, signature)
		first <- res.StatusCode
	}()
	// The first copy is inside the hub call now, and stays there until
	// released.
	<-hub.arrived

	res, out := l.deliver(t, "deploy", body, stamp, signature)
	close(hub.release)
	if code := <-first; code != http.StatusAccepted {
		t.Errorf("the first copy answered %d", code)
	}
	if res.StatusCode != http.StatusConflict {
		t.Errorf("the copy sent while the first was in flight answered %d: %s", res.StatusCode, out)
	}
	if got := hub.count(); got != 1 {
		t.Errorf("one delivery reached the bus %d times", got)
	}

	// And once it has landed, it is a replay like any other.
	res, out = l.deliver(t, "deploy", body, stamp, signature)
	if res.StatusCode != http.StatusUnauthorized || !strings.Contains(out, "already been accepted") {
		t.Errorf("a copy after the delivery landed answered %d: %s", res.StatusCode, out)
	}
	if got := hub.count(); got != 1 {
		t.Errorf("one delivery reached the bus %d times", got)
	}
}

// A caller holding no secret could spend a hook's whole rate limit,
// because the limit was taken before authentication from one bucket per
// path: the real sender's deliveries were then refused with 429. The
// clock is fixed, so no bucket refills during the test. DIVERGENCE 5.327.
func TestAnAnonymousFloodDoesNotSpendTheSendersAllowance(t *testing.T) {
	l, hub := hookLab(t, "hooks:\n  deploy:\n    auth: hmac\n    secret: s3cret\n    rate_limit: 2\n")
	now := time.Now().UTC().Truncate(time.Second)
	l.server.Now = func() time.Time { return now }
	stamp := now.Format(time.RFC3339)

	for i := 0; i < 20; i++ {
		res, out := l.deliver(t, "deploy", `{"flood":true}`, stamp, "not-a-signature")
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous delivery %d answered %d: %s", i, res.StatusCode, out)
		}
	}

	// The sender's allowance is untouched: both of its burst go through.
	for i, body := range []string{`{"n":1}`, `{"n":2}`} {
		res, out := l.deliver(t, "deploy", body, stamp, sign("s3cret", stamp, body))
		if res.StatusCode != http.StatusAccepted {
			t.Fatalf("signed delivery %d after the flood answered %d: %s", i, res.StatusCode, out)
		}
	}
	sent := 0
	for _, call := range hub.seen {
		if strings.Contains(call.body, "event.send") {
			sent++
		}
	}
	if sent != 2 {
		t.Errorf("%d deliveries reached the bus, want the two signed ones", sent)
	}

	// The limit still holds for the sender itself, and a delivery it
	// turned away is not consumed: the same signature goes through once
	// the bucket has refilled.
	body := `{"n":3}`
	signature := sign("s3cret", stamp, body)
	res, _ := l.deliver(t, "deploy", body, stamp, signature)
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("a third signed delivery in the same instant answered %d", res.StatusCode)
	}
	now = now.Add(time.Second)
	res, out := l.deliver(t, "deploy", body, stamp, signature)
	if res.StatusCode != http.StatusAccepted {
		t.Errorf("the rate-limited delivery, retried, answered %d: %s", res.StatusCode, out)
	}
}

// Refusals are logged while the hook's refusal bucket has room, and not
// after: a caller holding no secret can make as many as it likes, and a
// warning for each would let it fill the log. The clock is fixed, so the
// bucket of two does not refill.
func TestRefusalsAreLoggedAtTheHooksRate(t *testing.T) {
	l, _ := hookLab(t, "hooks:\n  deploy:\n    auth: hmac\n    secret: s3cret\n    rate_limit: 2\n")
	var out bytes.Buffer
	logger, err := log.New(log.Options{Level: log.Info, Stderr: &out})
	if err != nil {
		t.Fatal(err)
	}
	l.server.Log = logger
	now := time.Now().UTC().Truncate(time.Second)
	l.server.Now = func() time.Time { return now }
	stamp := now.Format(time.RFC3339)

	for i := 0; i < 10; i++ {
		if res, _ := l.deliver(t, "deploy", `{}`, stamp, "not-a-signature"); res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("anonymous delivery %d answered %d", i, res.StatusCode)
		}
	}
	if got := strings.Count(out.String(), "hook delivery refused"); got != 2 {
		t.Errorf("ten refusals wrote %d warnings, want the bucket's two:\n%s", got, out.String())
	}
}
