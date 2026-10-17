package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// enrollClient speaks to the hub the way a node with no certificate
// does: the node's own TLS configuration, which offers halite/1 and
// negotiates h2, since the hub refuses a handshake without halite/1.
func enrollClient(l *lab, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: transport.EnrollConfig(l.ca.Cert, "127.0.0.1"), ForceAttemptHTTP2: true},
		Timeout:   timeout,
	}
}

// **The enrollment body is bounded to what a CSR needs** (issue 321,
// M-06). It had MaxRequestBody's 64 MiB. The body here is a real CSR and
// a 1 MiB "token": on main the hub read it all, and in manual mode kept
// a pending record for the name.
func TestAnEnrollmentBodyPastTheBoundIsRefusedAndKeepsNothing(t *testing.T) {
	l := newLab(t)
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	der, err := pki.NewNodeCSR(key, "big.example")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(transport.EnrollRequest{
		CSR:   string(pki.EncodeCSR(der)),
		Token: strings.Repeat("x", 1<<20),
	})
	res, err := enrollClient(l, 10*time.Second).Post(l.url+transport.PathEnroll, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode < 400 || res.StatusCode >= 500 {
		t.Errorf("a %d-byte enrollment body got %d", len(body), res.StatusCode)
	}
	if _, err := l.server.Authority.Store.Get("big.example"); !errors.Is(err, keystore.ErrNotFound) {
		t.Errorf("the oversized request left a record: %v", err)
	}
}

// **An enrollment that trickles in is cut off.** The listener sets only
// ReadHeaderTimeout, so a caller that sent its headers and then a byte
// at a time held the request for as long as it liked. The request now
// has its own read deadline; the test shortens it and expects the hub to
// answer well before a client-side bound ten times longer.
func TestAnEnrollmentThatTricklesInIsCutOff(t *testing.T) {
	l := newLab(t)
	l.server.EnrollReadTimeout = 300 * time.Millisecond
	pr, pw := io.Pipe()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		defer pw.Close()
		_, _ = pw.Write([]byte(`{"csr":"`))
		for {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
				if _, err := pw.Write([]byte("A")); err != nil {
					return
				}
			}
		}
	}()
	req, err := http.NewRequest(http.MethodPost, l.url+transport.PathEnroll, pr)
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = 4096
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	res, err := enrollClient(l, 3*time.Second).Do(req)
	elapsed := time.Since(start)
	if err == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("the hub was still reading a trickled enrollment after %v: %v", elapsed, err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("the hub took %v to give up on a trickled enrollment", elapsed)
	}
	status := 0
	if res != nil {
		status = res.StatusCode
	}
	t.Logf("cut off after %v (status %d, err %v)", elapsed, status, err)
}
