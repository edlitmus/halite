package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWhatCountsAsNoHubAnswering(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"refused by the hub", &RefusedError{Status: 403}, false},
		{"hub failing to compile", &StatusError{Status: 500}, false},
		{"rate limited", &StatusError{Status: 429}, false},
		{"balancer with no hub", &StatusError{Status: 503}, true},
		{"cancelled job", fmt.Errorf("x: %w", context.Canceled), false},
		{"timed out", fmt.Errorf("x: %w", context.DeadlineExceeded), true},
		{"no such host", &net.DNSError{Err: "no such host", IsNotFound: true}, true},
		{"unrelated", errors.New("the pillar the hub sent is not readable"), false},
	}
	for _, c := range cases {
		if got := Unreachable(c.err); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// The rest are the errors the real net/http client produces, because the
// whole risk is in how they are wrapped: an alert and a failed verification
// both arrive looking like network failures.
func TestRealFailuresAreClassified(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	l.Close()
	if _, err := http.Get("http://" + closed); !Unreachable(err) {
		t.Errorf("a closed port is not unreachable: %v", err)
	}

	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	// The default client does not trust this server: an impostor's shape.
	if _, err := http.Get(srv.URL); err == nil || Unreachable(err) {
		t.Errorf("an unverifiable hub counted as unreachable: %v", err)
	}

	// A server that refuses this client's lack of a certificate: a hub
	// rejecting a node.
	strict := httptest.NewUnstartedServer(http.NotFoundHandler())
	strict.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}
	strict.StartTLS()
	defer strict.Close()
	c := strict.Client()
	if _, err := c.Get(strict.URL); err == nil || Unreachable(err) {
		t.Errorf("a TLS alert from a hub counted as unreachable: %v", err)
	}
}

// A node revoked while it was off hears only this alert. The hub answered, so
// it is not an outage, and it means the pillar copy has to go.
func TestAHubRejectingTheCertificateIsRejectedNotUnreachable(t *testing.T) {
	bad := &net.OpError{Op: "remote error", Err: errors.New("tls: bad certificate")}
	if !Rejected(fmt.Errorf("/v1/pillar: %w", bad)) {
		t.Error("a rejected certificate was not recognised")
	}
	if Unreachable(bad) {
		t.Error("a rejected certificate counted as an outage")
	}
	dial := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	if Rejected(dial) || !Unreachable(dial) {
		t.Error("a refused connection was read as a rejection")
	}
	other := &net.OpError{Op: "remote error", Err: errors.New("tls: internal error")}
	if Rejected(other) {
		t.Error("an unrelated alert would have removed the pillar copy")
	}
}
