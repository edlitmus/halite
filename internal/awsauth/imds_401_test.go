package awsauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// metadataStandIn is an IMDSv2 that issues numbered tokens and refuses any
// token in forgotten, the way the service refuses one it does not know.
type metadataStandIn struct {
	mu        sync.Mutex
	issued    int
	forgotten map[string]bool
	refuseAll bool
	trace     []string
}

func (s *metadataStandIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodPut {
		s.issued++
		tok := "token-" + string(rune('0'+s.issued))
		s.trace = append(s.trace, "PUT 200")
		w.Write([]byte(tok))
		return
	}
	tok := r.Header.Get("X-aws-ec2-metadata-token")
	if s.refuseAll || s.forgotten[tok] {
		s.trace = append(s.trace, "GET 401")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	s.trace = append(s.trace, "GET 200")
	w.Write([]byte("i-0123456789abcdef0"))
}

// A read refused with 401 is made once more with a new token, and works
// (DIVERGENCE 5.737). It failed, and the token stayed cached for the rest
// of its local life, so the next reads failed too.
func TestAMetadataReadRefusedWith401TakesANewTokenOnce(t *testing.T) {
	svc := &metadataStandIn{forgotten: map[string]bool{}}
	srv := httptest.NewServer(svc)
	defer srv.Close()
	m := &IMDS{Address: srv.URL}

	if _, err := m.Get(context.Background(), "latest/meta-data/instance-id"); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.forgotten["token-1"] = true // the service restarted
	svc.trace = nil
	svc.mu.Unlock()

	for i := 0; i < 2; i++ {
		got, err := m.Get(context.Background(), "latest/meta-data/instance-id")
		if err != nil || string(got) != "i-0123456789abcdef0" {
			t.Fatalf("read %d after the service forgot the token: %q, %v", i+1, got, err)
		}
	}
	if got, want := strings.Join(svc.trace, ", "), "GET 401, PUT 200, GET 200, GET 200"; got != want {
		t.Errorf("the service saw %s; want %s -- one refused read, one new token, then the new token used", got, want)
	}
}

// A service that refuses every token is reported, after one new token, and
// not asked again and again.
func TestAMetadataServiceThatRefusesEveryTokenIsReportedNotLooped(t *testing.T) {
	svc := &metadataStandIn{forgotten: map[string]bool{}, refuseAll: true}
	srv := httptest.NewServer(svc)
	defer srv.Close()
	m := &IMDS{Address: srv.URL}

	_, err := m.Get(context.Background(), "latest/meta-data/instance-id")
	if err == nil || !strings.Contains(err.Error(), "answered 401") {
		t.Fatalf("a service refusing every token: %v", err)
	}
	if got, want := strings.Join(svc.trace, ", "), "PUT 200, GET 401, PUT 200, GET 401"; got != want {
		t.Errorf("the service saw %s; want %s", got, want)
	}
}
