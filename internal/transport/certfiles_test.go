package transport

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/certreload"
	"github.com/edlitmus/halite/internal/pki"
)

// writeNodePair issues to a node and writes the certificate and key where
// a renewal would, answering with the serial.
func (f *fixture) writeNodePair(t *testing.T, nodeID, certPath, keyPath string) string {
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
	keyPEM, _ := pki.EncodeKey(key)
	if err := os.WriteFile(certPath, pki.EncodeCert(der), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	cert, _ := pki.DecodeCert(pki.EncodeCert(der))
	return pki.SerialString(cert)
}

// A client built once with CertFiles presents a certificate renewed in
// place from the next request, on a new connection, and keeps its
// connection while nothing changes. DIVERGENCE 5.254.
//
// The relay is the client this is for: its upstream revokes the old
// serial as it issues the new one, so both halves matter. Reading the new
// pair into a new tls.Config but keeping the pooled HTTP/2 connection
// would go on authenticating as the revoked serial, which is why the
// handler answers with the serial of the connection the request came in
// on rather than anything the client says about itself.
func TestARenewedClientCertificateIsPresentedOnANewConnection(t *testing.T) {
	f := newFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s %s", pki.SerialString(r.TLS.PeerCertificates[0]), r.RemoteAddr)
	}))
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "node.crt"), filepath.Join(dir, "node.key")
	first := f.writeNodePair(t, "relay1.example", certPath, keyPath)

	var changed []string
	certs, err := certreload.NewClient(certPath, keyPath,
		func(msg string, _ ...any) { changed = append(changed, msg) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{HubURL: f.url, CA: f.ca.Cert, ServerName: "localhost", CertFiles: certs}

	ask := func() (serial, addr string) {
		t.Helper()
		body, _, _, err := client.get(context.Background(), "/who", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Sscanf(string(body), "%s %s", &serial, &addr); err != nil {
			t.Fatalf("the handler answered %q: %v", body, err)
		}
		return serial, addr
	}

	serial, conn := ask()
	if serial != first {
		t.Fatalf("the first request presented %s, want the enrolled %s", serial, first)
	}
	if again, sameConn := ask(); again != first || sameConn != conn {
		t.Errorf("with nothing changed on disk the request came as %s on %s, want %s on the same connection %s",
			again, sameConn, first, conn)
	}

	renewed := f.writeNodePair(t, "relay1.example", certPath, keyPath)
	serial, newConn := ask()
	if serial != renewed {
		t.Errorf("after the renewal the request presented %s, want the renewed %s (the enrolled one was %s)",
			serial, renewed, first)
	}
	if newConn == conn {
		t.Errorf("after the renewal the request came on the old connection %s", conn)
	}
	if len(changed) != 1 {
		t.Errorf("the renewal should be said once, was said %d times: %q", len(changed), changed)
	}
}
