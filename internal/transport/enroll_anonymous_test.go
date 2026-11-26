package transport

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// answerEnrollAsPending is the hub's side of /v1/enroll reduced to what these
// tests look at: whether the connection carried a client certificate. Anything
// else it is asked for is an error, so a test that reached the wrong route
// fails instead of passing on a default answer.
func answerEnrollAsPending(presented *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathEnroll {
			http.Error(w, "not the enroll route", http.StatusNotFound)
			return
		}
		presented.Add(int32(len(r.TLS.PeerCertificates)))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(EnrollResponse{State: "pending"})
	})
}

// A node that still holds a certificate the hub no longer accepts is exactly
// the node that has to enroll again, and the hub ends a handshake that
// presents such a certificate (DIVERGENCE 5.730.1, issue 880). `/v1/enroll` is
// reachable without a certificate, so asking over one only adds a way to be
// refused. Each case here is a certificate the hub refuses at the handshake,
// checked against the same hub in the refused test beside this one, plus the
// control that a certificate the hub accepts is not presented either.
func TestEnrollReachesTheHubWhateverCertificateTheNodeStillHolds(t *testing.T) {
	var presented atomic.Int32
	hub := newFixture(t, answerEnrollAsPending(&presented))

	revoked, serial := hub.enrolledPair(t, "revoked.example")
	hub.denied.Revoke(serial, "decommissioned")

	cases := []struct {
		name string
		held tls.Certificate
	}{
		{"a revoked certificate", revoked},
		{"an expired certificate", issueFrom(t, hub.ca, "late.example", -30*time.Second)},
		{"a certificate the hub accepts", issueFrom(t, hub.ca, "fine.example", time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			presented.Store(0)
			held := c.held
			client := &Client{HubURL: hub.url, CA: hub.ca.Cert, ServerName: "localhost", Cert: &held}
			key, err := pki.GenerateKey(pki.ECDSAP256)
			if err != nil {
				t.Fatal(err)
			}

			var got error
			for attempt := 0; attempt < 20; attempt++ {
				_, got = client.Enroll(t.Context(), key, "node.example", "")
				if got == nil || !lostTheAlert(got) {
					break
				}
			}

			if !errors.Is(got, ErrPending) {
				t.Fatalf("Enroll answered %v, want ErrPending: the hub should have been reached", got)
			}
			if n := presented.Load(); n != 0 {
				t.Errorf("the enroll request carried %d client certificate(s); it must carry none", n)
			}
		})
	}
}

// Enrolling anonymously must not change how every other request is made: the
// same client, holding a certificate the hub accepts, still presents it
// everywhere else, or the fix would have made a node anonymous.
func TestAClientThatEnrolledStillPresentsItsCertificateElsewhere(t *testing.T) {
	var presented atomic.Int32
	hub := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case PathEnroll:
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(EnrollResponse{State: "pending"})
		default:
			presented.Add(int32(len(r.TLS.PeerCertificates)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{}"))
		}
	}))
	held := issueFrom(t, hub.ca, "fine.example", time.Hour)
	client := &Client{HubURL: hub.url, CA: hub.ca.Cert, ServerName: "localhost", Cert: &held}
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Enroll(t.Context(), key, "node.example", ""); !errors.Is(err, ErrPending) {
		t.Fatalf("Enroll answered %v, want ErrPending", err)
	}
	httpClient, err := client.client()
	if err != nil {
		t.Fatal(err)
	}
	res, err := httpClient.Get(hub.url + "/v1/anything")
	if err != nil {
		t.Fatalf("the client with a certificate could not make an ordinary request: %v", err)
	}
	res.Body.Close()

	if presented.Load() != 1 {
		t.Errorf("an ordinary request carried %d client certificate(s), want 1", presented.Load())
	}
}
