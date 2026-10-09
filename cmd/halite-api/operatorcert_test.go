package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// issueOperatorFiles writes an operator certificate and key where
// `halite-hub keys operator create` leaves them, and answers with the
// serial.
func issueOperatorFiles(t *testing.T, ca *pki.CA, files pki.Files, name string) string {
	t.Helper()
	key, _ := pki.GenerateKey(pki.ECDSAP256)
	der, err := ca.IssueOperator(key, name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := pki.EncodeKey(key)
	if err := os.WriteFile(files.Path("operator-"+name+".key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.Path(pki.OperatorCertFile(name)), pki.EncodeCert(der), 0o644); err != nil {
		t.Fatal(err)
	}
	cert, _ := pki.DecodeCert(pki.EncodeCert(der))
	return pki.SerialString(cert)
}

// The API's operator certificate, issued again in place, is what the
// client it already built presents on its next request. It was loaded
// once at startup, so a re-issue did nothing until halite-api was
// restarted, and an API left running was refused by the hub once the
// certificate it started with expired. DIVERGENCE 5.271.
func TestAReissuedOperatorCertificateIsPresentedWithoutARestart(t *testing.T) {
	ca, err := pki.NewCA(pki.ECDSAP256, "hub CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// A hub endpoint whose health answer is the serial of the client
	// certificate the request arrived with.
	hubKey, _ := pki.GenerateKey(pki.ECDSAP256)
	hubDER, err := ca.IssueHub(hubKey, []string{"127.0.0.1", "localhost"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	hubKeyPEM, _ := pki.EncodeKey(hubKey)
	hubPair, err := tls.X509KeyPair(pki.EncodeCert(hubDER), hubKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", transport.ServerConfig(hubPair, ca.Cert, transport.NewDenylist()))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 {
			fmt.Fprint(w, "anonymous")
			return
		}
		fmt.Fprint(w, pki.SerialString(r.TLS.PeerCertificates[0]))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	root := t.TempDir()
	files := pki.Files{Dir: filepath.Join(root, "pki")}
	if err := os.MkdirAll(files.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.Path(pki.CACertFile), pki.EncodeCert(ca.Cert.Raw), 0o644); err != nil {
		t.Fatal(err)
	}
	first := issueOperatorFiles(t, ca, files, "api")
	if err := os.WriteFile(filepath.Join(root, "api.yaml"),
		[]byte("hub: "+ln.Addr().String()+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.API, config.LoadOptions{Root: root, AllowMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	args := &cli.Args{Flags: map[string]string{"root": root, "server-name": "localhost"}}
	client, err := hubClient(&service{cfg: cfg, root: root}, args)
	if err != nil {
		t.Fatal(err)
	}

	ask := func() string {
		t.Helper()
		got, err := client.Health(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(got)
	}
	if got := ask(); got != first {
		t.Fatalf("the API presented %q, want the operator certificate %s", got, first)
	}
	second := issueOperatorFiles(t, ca, files, "api")
	if got := ask(); got != second {
		t.Errorf("after the re-issue the API presented %q, want the new %s (the old was %s)", got, second, first)
	}
}
