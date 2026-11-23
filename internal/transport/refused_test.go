package transport

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// issueFrom issues a node certificate from ca with the given lifetime, which
// may be negative, for a certificate that has already expired.
func issueFrom(t *testing.T, ca *pki.CA, nodeID string, lifetime time.Duration) tls.Certificate {
	t.Helper()
	key, _ := pki.GenerateKey(pki.ECDSAP256)
	csrDER, _ := pki.NewNodeCSR(key, nodeID)
	csr, err := pki.DecodeCSR(pki.EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}
	der, err := ca.IssueNode(csr, nodeID, lifetime)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := pki.EncodeKey(key)
	pair, err := tls.X509KeyPair(pki.EncodeCert(der), keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

// lostAlert is what Go's HTTP/2 client says, with no cause attached, when
// the alert arrives after the client's half of a TLS 1.3 handshake has
// finished and the connection dies before the client reads it. Measured: 40
// of 1,200 refused handshakes in 300 runs of the test below, mostly the expired
// case, once the revoked one. A node that gets it retries, and the next
// attempt reads the alert.
const lostAlert = "http2: client conn could not be established"

// getWith makes one request on a fresh client, again while the alert was
// lost, so that each case is checked against the alert it gets rather than
// against the race.
func getWith(cfg *tls.Config, url string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}}
		var res *http.Response
		res, err = client.Get(url)
		if err == nil {
			res.Body.Close()
			return nil
		}
		if !strings.Contains(err.Error(), lostAlert) {
			return err
		}
	}
	return err
}

// A node exits on CertificateRefused and retries on everything else, so the
// line between them is drawn here against what a real Go TLS server with the
// hub's own configuration sends in each case, rather than against alerts
// written out by hand (issue 839). Each case also checks the error text it
// expects, so that a case which stopped producing the failure it is named for
// fails rather than passing on some other error.
func TestOnlyARevokedOrUnacceptableCertificateIsRefusedForGood(t *testing.T) {
	hub := newFixture(t, http.HandlerFunc(echoIdentity))
	other := newFixture(t, http.HandlerFunc(echoIdentity))

	revoked, serial := hub.enrolledPair(t, "revoked.example")
	hub.denied.Revoke(serial, "decommissioned")

	stopped, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stoppedURL := "https://" + stopped.Addr().String()
	stopped.Close()

	cases := []struct {
		name    string
		cfg     *tls.Config
		url     string
		text    string
		refused bool
	}{
		{"a revoked certificate", ClientConfig(revoked, hub.ca.Cert, "localhost"), hub.url,
			"remote error: tls: bad certificate", true},
		{"a certificate from a CA the hub does not hold",
			ClientConfig(issueFrom(t, other.ca, "stranger.example", time.Hour), hub.ca.Cert, "localhost"), hub.url,
			"remote error: tls: unknown certificate authority", false},
		{"an expired certificate",
			ClientConfig(issueFrom(t, hub.ca, "late.example", -30*time.Second), hub.ca.Cert, "localhost"), hub.url,
			"remote error: tls: expired certificate", false},
		{"a hub whose CA is not the one this node pinned",
			ClientConfig(issueFrom(t, other.ca, "moved.example", time.Hour), other.ca.Cert, "localhost"), hub.url,
			"certificate signed by unknown authority", false},
		{"a hub that is down",
			ClientConfig(issueFrom(t, hub.ca, "patient.example", time.Hour), hub.ca.Cert, "localhost"), stoppedURL,
			"connection refused", false},
	}
	// The lost alert is not a refusal: it carries nothing that says which
	// failure it was, so the node must retry it.
	if CertificateRefused(&url.Error{Op: "Get", URL: hub.url, Err: errors.New(lostAlert)}) {
		t.Error("a lost alert was read as a refusal")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := getWith(c.cfg, c.url)
			if err == nil {
				t.Fatal("the request succeeded")
			}
			if !strings.Contains(err.Error(), c.text) {
				t.Fatalf("the error is %q, which is not the failure this case is about (%q)", err, c.text)
			}
			if got := CertificateRefused(err); got != c.refused {
				t.Errorf("CertificateRefused(%q) = %v, want %v", err, got, c.refused)
			}
		})
	}

	// The control: the same hub accepts a certificate it issued, so the
	// failures above are each case's and not the fixture's.
	if err := getWith(ClientConfig(issueFrom(t, hub.ca, "fine.example", time.Hour), hub.ca.Cert, "localhost"), hub.url); err != nil {
		t.Fatalf("a valid certificate was refused: %v", err)
	}
}
