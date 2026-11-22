package api

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

// mtlsLab is the API serving through its own Listen, with hook_client_ca
// set to senders, so the handshake that verifies a sender's certificate
// is the one the service really does. httptest would stand up its own
// TLS and test that instead.
type mtlsLab struct {
	url     string
	servers *x509.CertPool
	stub    *stubHub
}

func newMTLSLab(t *testing.T, senders *pki.CA) *mtlsLab {
	t.Helper()
	l, hub := hookLab(t, "hooks:\n  deploy:\n    auth: mtls\n    client: ci.example\n")

	serverCA, err := pki.NewCA(pki.ECDSAP256, "api CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := pki.GenerateKey(pki.ECDSAP256)
	der, err := serverCA.IssueHub(key, []string{"127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := pki.EncodeKey(key)
	serving, err := tls.X509KeyPair(pki.EncodeCert(der), keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(senders.Cert)

	ln, err := Listen("127.0.0.1:0", func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &serving, nil }, pool)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: l.server.Handler(), ErrorLog: nil}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	servers := x509.NewCertPool()
	servers.AddCert(serverCA.Cert)
	return &mtlsLab{url: "https://" + ln.Addr().String(), servers: servers, stub: hub}
}

// sender is a certificate a sender would present: issued by ca to name.
func sender(t *testing.T, ca *pki.CA, name string) tls.Certificate {
	t.Helper()
	key, _ := pki.GenerateKey(pki.ECDSAP256)
	csrDER, _ := pki.NewNodeCSR(key, name)
	csr, err := pki.DecodeCSR(pki.EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}
	der, err := ca.IssueNode(csr, name, time.Hour)
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

// deliverAs posts to the hook presenting certs, which may be none.
//
// The certificate is offered whatever the server's request names. Go's
// client by default sends one only if its issuer is among the CAs the
// server lists, so a test that left it to choose would never offer an
// untrusted one; curl and most other clients send what they are given.
func (m *mtlsLab) deliverAs(t *testing.T, certs ...tls.Certificate) (int, string, error) {
	t.Helper()
	cfg := &tls.Config{RootCAs: m.servers, MinVersion: tls.VersionTLS13}
	if len(certs) > 0 {
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return &certs[0], nil }
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg}}
	defer client.CloseIdleConnections()
	res, err := client.Post(m.url+"/v1/hook/deploy", "application/json", strings.NewReader(`{"version":"1.2"}`))
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(body), nil
}

func newSenderCA(t *testing.T, name string) *pki.CA {
	t.Helper()
	ca, err := pki.NewCA(pki.ECDSAP256, name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// The four cases issue 767 names, through the real handshake: the
// sender the hook names is delivered; a certificate from the same CA
// naming somebody else, a certificate from a CA nobody configured, and no
// certificate at all are not.
func TestAnMTLSHookAdmitsOnlyTheSenderItNames(t *testing.T) {
	senders := newSenderCA(t, "hook senders")
	lab := newMTLSLab(t, senders)

	t.Run("the named sender", func(t *testing.T) {
		status, body, err := lab.deliverAs(t, sender(t, senders, "ci.example"))
		if err != nil || status != http.StatusAccepted {
			t.Fatalf("got %d %s %v, want the delivery accepted", status, body, err)
		}
		if !strings.Contains(body, "halite/hook/deploy") {
			t.Errorf("the answer is %s", body)
		}
	})
	t.Run("another holder of the same CA", func(t *testing.T) {
		status, body, err := lab.deliverAs(t, sender(t, senders, "build.example"))
		if err != nil || status != http.StatusUnauthorized || !strings.Contains(body, "does not name ci.example") {
			t.Errorf("got %d %s %v, want 401 saying the certificate names somebody else", status, body, err)
		}
	})
	t.Run("a CA nobody configured", func(t *testing.T) {
		// Refused at the handshake, before any handler runs: the
		// listener verifies an offered certificate against
		// hook_client_ca, and this one does not chain to it.
		status, body, err := lab.deliverAs(t, sender(t, newSenderCA(t, "somebody else"), "ci.example"))
		if err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Errorf("got %d %s %v, want the handshake refused over the certificate", status, body, err)
		}
		t.Logf("refused with: %v", err)
	})
	t.Run("no certificate", func(t *testing.T) {
		status, body, err := lab.deliverAs(t)
		if err != nil || status != http.StatusUnauthorized || !strings.Contains(body, "needs a client certificate") {
			t.Errorf("got %d %s %v, want 401 asking for a certificate", status, body, err)
		}
	})
}

// The listener asks for a certificate and does not require one, so an
// operator with only a token is not turned away by the handshake that
// mtls hooks need.
func TestAClientWithNoCertificateStillReachesTheRestOfTheAPI(t *testing.T) {
	lab := newMTLSLab(t, newSenderCA(t, "hook senders"))
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: lab.servers, MinVersion: tls.VersionTLS13},
	}}
	defer client.CloseIdleConnections()
	res, err := client.Get(lab.url + PathHealthz)
	if err != nil {
		t.Fatalf("a client with no certificate was refused at the handshake: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("health answered %d", res.StatusCode)
	}
}
