package tracing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/redact"
)

// What the collector receives is held to the redactor, as a log line is.
// Before issue 614 nothing in this package called it: a span's status
// message -- an error's text, from span.Fail -- and its attributes went
// to the collector as they were.
//
// The test runs the whole path a process runs: TracerFrom with the
// process's secret set, a span failed with an error, attributes of
// every shape attrValue prints, End, and the OTLP body as the
// collector's end of the socket sees it.
func TestASpanIsRedactedBeforeItIsExported(t *testing.T) {
	const secret = "hunter2-correct-horse-battery"
	var (
		mu   sync.Mutex
		body string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := readAll(r)
		mu.Lock()
		body += string(b)
		mu.Unlock()
	}))
	defer srv.Close()

	secrets := redact.New()
	secrets.Add(secret)
	tr, err := TracerFrom(Settings{
		Mode: ModeOTLP, Endpoint: srv.URL, SampleRate: 1, SampleRateSet: true,
		Service: "halite-node", Version: "test", Secrets: secrets,
	})
	if err != nil {
		t.Fatal(err)
	}
	tr.Sampler = AlwaysSample{}
	tr.BatchWait = 10 * time.Millisecond
	tr.Start()

	span := tr.StartSpan(SpanContext{}, "state "+secret, KindInternal)
	span.Fail(errors.New("cmd.run: mysql -p" + secret + " exited 1"))
	span.SetAttr("plain", "--password="+secret)
	span.SetAttr("error", errors.New("refused "+secret))
	span.SetAttr("list", []any{"a", secret})
	span.SetAttr("url", &url.URL{Scheme: "https", Host: "git.example", Path: "/r.git", User: url.UserPassword("deploy", "not-registered-anywhere")})
	span.SetAttr("count", 3)
	span.End()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tr.Stop(ctx)

	mu.Lock()
	defer mu.Unlock()
	if body == "" {
		t.Fatal("nothing reached the collector, so nothing was tested")
	}
	if strings.Contains(body, secret) {
		t.Errorf("a registered secret reached the collector:\n%s", body)
	}
	if strings.Contains(body, "not-registered-anywhere") {
		t.Errorf("a URL's password reached the collector:\n%s", body)
	}
	// Redacted, not dropped: the message still says what failed.
	for _, want := range []string{"cmd.run: mysql -p", "exited 1", `"count"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the export lost %q:\n%s", want, body)
		}
	}
}
