package awsauth

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// tracingTransport records each request's method and the status it got.
type tracingTransport struct {
	mu    sync.Mutex
	trace []string
}

func (tr *tracingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := http.DefaultTransport.RoundTrip(r)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if err != nil {
		tr.trace = append(tr.trace, r.Method+" error")
	} else {
		tr.trace = append(tr.trace, r.Method+" "+res.Status[:3])
	}
	return res, err
}

// Item 2: the real service refuses a token it does not know with 401, and
// a read given one recovers with a new token rather than failing.
func TestLiveAMetadataReadRecoversFromATokenTheServiceDoesNotKnow(t *testing.T) {
	liveEC2(t)
	tr := &tracingTransport{}
	m := &IMDS{Client: &http.Client{Timeout: 5 * time.Second, Transport: tr}}
	if _, err := m.Get(context.Background(), "latest/meta-data/instance-id"); err != nil {
		t.Fatal(err)
	}
	// A token the service never issued, cached as though it had: what a
	// restarted or forgetful metadata service leaves a reader holding.
	m.mu.Lock()
	m.token = "AQAEAHalite-issue-613-not-a-token"
	m.expires = m.now().Add(TokenTTL)
	m.mu.Unlock()
	tr.mu.Lock()
	tr.trace = nil
	tr.mu.Unlock()

	id, err := m.Get(context.Background(), "latest/meta-data/instance-id")
	tr.mu.Lock()
	trace := strings.Join(tr.trace, ", ")
	tr.mu.Unlock()
	t.Logf("the metadata service saw: %s", trace)
	if err != nil {
		t.Fatalf("a read holding a token the service does not know: %v", err)
	}
	if !strings.HasPrefix(string(id), "i-") {
		t.Errorf("the instance id read back as %q", id)
	}
	if trace != "GET 401, PUT 200, GET 200" {
		t.Errorf("the service saw %s; want GET 401, PUT 200, GET 200", trace)
	}
}
