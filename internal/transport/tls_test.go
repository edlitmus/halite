package transport

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

type fixture struct {
	ca     *pki.CA
	url    string
	denied *Denylist
}

// newFixture stands up a real TLS server with the real configurations,
// because the properties under test are handshake properties and a mock
// handshake proves nothing about a handshake.
func newFixture(t *testing.T, handler http.Handler) *fixture {
	t.Helper()
	ca, err := pki.NewCA(pki.ECDSAP256, "test CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	hubKey, _ := pki.GenerateKey(pki.ECDSAP256)
	hubDER, err := ca.IssueHub(hubKey, []string{"hub.test", "127.0.0.1", "localhost"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := pki.EncodeKey(hubKey)
	cert, err := tls.X509KeyPair(pki.EncodeCert(hubDER), keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	denied := NewDenylist()
	// A real net/http server over a real TLS listener, rather than
	// httptest: httptest rewrites NextProtos to suit itself, so an ALPN
	// test through it tests the harness.
	ln, err := tls.Listen("tcp", "127.0.0.1:0", ServerConfig(cert, ca.Cert, denied))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return &fixture{ca: ca, url: "https://" + ln.Addr().String(), denied: denied}
}

func (f *fixture) enrolledClient(t *testing.T, nodeID string) (*http.Client, string) {
	t.Helper()
	key, _ := pki.GenerateKey(pki.ECDSAP256)
	csrDER, _ := pki.NewNodeCSR(key, nodeID)
	csr, err := pki.DecodeCSR(pki.EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}
	der, err := f.ca.IssueNode(csr, nodeID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := pki.DecodeCert(pki.EncodeCert(der))
	keyPEM, _ := pki.EncodeKey(key)
	pair, err := tls.X509KeyPair(pki.EncodeCert(der), keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig:   ClientConfig(pair, f.ca.Cert, "localhost"),
		ForceAttemptHTTP2: true,
	}}, pki.SerialString(cert)
}

// echoIdentity answers with the node ID the peer certificate carries, or
// says it is anonymous.
func echoIdentity(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		io.WriteString(w, "anonymous")
		return
	}
	id, err := pki.NodeIDFromCert(r.TLS.PeerCertificates[0])
	if err != nil {
		io.WriteString(w, "unidentified")
		return
	}
	io.WriteString(w, id)
}

func TestMutualAuthenticationCarriesTheIdentity(t *testing.T) {
	f := newFixture(t, http.HandlerFunc(echoIdentity))
	client, _ := f.enrolledClient(t, "web1.example")

	res, err := client.Get(f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if string(body) != "web1.example" {
		t.Errorf("the handler saw %q; the peer certificate names web1.example", body)
	}
	// SPEC 6.1: HTTP/2, negotiated by ALPN.
	if res.ProtoMajor != 2 {
		t.Errorf("negotiated HTTP/%d.%d, want HTTP/2", res.ProtoMajor, res.ProtoMinor)
	}
	if res.TLS.NegotiatedProtocol != Negotiated {
		t.Errorf("negotiated %q, want %q", res.TLS.NegotiatedProtocol, Negotiated)
	}
	if res.TLS.Version != tls.VersionTLS13 {
		t.Errorf("negotiated TLS 0x%04x, want 1.3", res.TLS.Version)
	}
}

// SPEC 7.4: revocation is immediate and does not wait for a CRL to
// propagate, so it is checked in the handshake rather than in a list the
// peer is trusted to have fetched.
func TestRevocationIsRefusedAtTheHandshake(t *testing.T) {
	f := newFixture(t, http.HandlerFunc(echoIdentity))
	client, serial := f.enrolledClient(t, "web1.example")

	if _, err := client.Get(f.url); err != nil {
		t.Fatalf("the certificate should work before it is revoked: %v", err)
	}
	f.denied.Revoke(serial, "operator revoked it")

	// A new connection, since the established one is already
	// authenticated: revocation stops the next handshake, and SPEC 7.4
	// pushes a control message down any live stream for the rest.
	client.CloseIdleConnections()
	_, err := client.Get(f.url)
	if err == nil {
		t.Fatal("a revoked certificate should not complete a handshake")
	}
	if reason, ok := f.denied.Revoked(serial); !ok || reason == "" {
		t.Error("the denylist should hold the reason an operator gave")
	}
}

// A peer that does not offer the protocol identifier is rejected at the
// handshake, which keeps a stray HTTPS client from reaching an endpoint
// at all.
func TestAPeerWithoutTheProtocolIsRejected(t *testing.T) {
	f := newFixture(t, http.HandlerFunc(echoIdentity))
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    poolOf(f.ca.Cert),
		ServerName: "localhost",
		NextProtos: []string{"http/1.1"},
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	if _, err := client.Get(f.url); err == nil {
		t.Error("a peer offering only http/1.1 should be refused")
	}
}

// TLS 1.2 is not offered in either direction. A setting that permits it
// is invariably found in production, so there is not one.
func TestOlderTLSIsNotOffered(t *testing.T) {
	f := newFixture(t, http.HandlerFunc(echoIdentity))
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		MaxVersion: tls.VersionTLS12,
		RootCAs:    poolOf(f.ca.Cert),
		ServerName: "localhost",
		NextProtos: []string{ALPN},
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg}}
	_, err := client.Get(f.url)
	if err == nil {
		t.Fatal("TLS 1.2 should not be accepted")
	}
	if !strings.Contains(err.Error(), "version") && !strings.Contains(err.Error(), "protocol") {
		t.Logf("refused, as it should be: %v", err)
	}
}

// An anonymous client reaches the server, because /v1/health is the one
// endpoint that permits one; the handler is what refuses the rest.
func TestAnAnonymousPeerIsIdentifiedAsSuch(t *testing.T) {
	f := newFixture(t, http.HandlerFunc(echoIdentity))
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   EnrollConfig(f.ca.Cert, "localhost"),
		ForceAttemptHTTP2: true,
	}}
	res, err := client.Get(f.url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if string(body) != "anonymous" {
		t.Errorf("an unenrolled peer presented %q", body)
	}
}

func poolOf(cert *x509.Certificate) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(cert)
	return p
}
