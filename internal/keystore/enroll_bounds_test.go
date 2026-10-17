package keystore

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// paddedRequest is a valid node CSR carrying an extension of pad bytes,
// which is how issue 321's M-06 made 11.5 MB pending records: nothing
// looked at a CSR's size before parsing it and keeping it verbatim.
func paddedRequest(t *testing.T, nodeID string, pad int) []byte {
	t.Helper()
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: nodeID},
		URIs:    []*url.URL{pki.NodeURI(nodeID)},
		ExtraExtensions: []pkix.Extension{{
			Id:    asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1},
			Value: make([]byte, pad),
		}},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return pki.EncodeCSR(der)
}

// **A CSR is refused by its size before it is parsed or kept.** On main
// a 1 MiB extension was parsed, and the whole CSR written into the
// pending record.
func TestAnOversizedCSRIsRefusedAndNotStored(t *testing.T) {
	a, _ := newAuthority(t)
	big := paddedRequest(t, "big.example", 1<<20)
	if _, err := a.Enroll(Request{CSR: big}); err == nil || errors.Is(err, ErrPending) {
		t.Fatalf("a %d-byte CSR was taken: %v", len(big), err)
	}
	if _, err := a.Store.Get("big.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the oversized CSR left a record: %v", err)
	}

	// A real node's CSR, at both key sizes halite-node makes, is well
	// inside the bound.
	for _, alg := range []pki.KeyAlgorithm{pki.ECDSAP256, pki.ECDSAP384} {
		key, err := pki.GenerateKey(alg)
		if err != nil {
			t.Fatal(err)
		}
		der, err := pki.NewNodeCSR(key, "a-long-node-name.with.several.labels.example")
		if err != nil {
			t.Fatal(err)
		}
		if n := len(pki.EncodeCSR(der)); n > MaxCSRBytes/8 {
			t.Errorf("a %v CSR is %d bytes, uncomfortably near the %d bound", alg, n, MaxCSRBytes)
		}
	}
}

// **Pending records are bounded**, and the bound does not stop a retry,
// an operator's decision, or a token.
func TestPendingRecordsAreBounded(t *testing.T) {
	a, _ := newAuthority(t)
	a.MaxPending = 3
	for i := 0; i < 3; i++ {
		csr, _ := request(t, fmt.Sprintf("n%d.example", i))
		if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	over, _ := request(t, "n3.example")
	_, err := a.Enroll(Request{CSR: over})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "already pending") {
		t.Fatalf("the fourth pending request: %v", err)
	}
	if _, err := a.Store.Get("n3.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the refused request left a record: %v", err)
	}

	// A node already pending asks again, as `enroll --wait` does, and is
	// told it is pending, not refused.
	again, _ := request(t, "again.example")
	b, _ := newAuthority(t)
	b.MaxPending = 1
	if _, err := b.Enroll(Request{CSR: again}); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := b.Enroll(Request{CSR: again}); !errors.Is(err, ErrPending) {
		t.Errorf("a retry at the bound: %v", err)
	}

	// A token that spends is not counted: token mode cannot be locked out
	// by somebody filling the pending bound.
	a.Mode = ModeToken
	now := time.Now()
	_, secret, err := a.Store.MintToken(TokenOptions{TTL: time.Hour}, now)
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Enroll(Request{CSR: over, Token: secret, RemoteAddr: "10.0.0.1:1"})
	if err != nil || res.State != Accepted {
		t.Errorf("a valid token at the pending bound: %v %v", res, err)
	}
	// And a bad one, for a new name, is bounded like any other caller.
	bad, _ := request(t, "n4.example")
	if _, err := a.Enroll(Request{CSR: bad, Token: "not-a-token", RemoteAddr: "10.0.0.1:1"}); !errors.Is(err, ErrRefused) {
		t.Errorf("a bad token: %v", err)
	}
	if _, err := a.Store.Get("n4.example"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a bad token past the bound left a record: %v", err)
	}
}

// **A bogus token cannot hold a name against the real node.** In token
// mode a request for web1 with a token that does not spend leaves web1
// pending under the caller's key; on main the real web1, with a valid
// token scoped to it, was then refused as "a different key" until an
// operator deleted the record (issue 321, M-06, the triage's "wider").
func TestATokenTakesANameFromAPendingRequestByAnotherKey(t *testing.T) {
	a, _ := newAuthority(t)
	a.Mode = ModeToken
	now := time.Now()
	a.Now = func() time.Time { return now }
	_, secret, err := a.Store.MintToken(TokenOptions{TTL: time.Hour, NodeGlob: "web*.example"}, now)
	if err != nil {
		t.Fatal(err)
	}

	squatter, _ := request(t, "web1.example")
	if _, err := a.Enroll(Request{CSR: squatter, Token: "guessed", RemoteAddr: "192.0.2.9:1"}); !errors.Is(err, ErrRefused) {
		t.Fatalf("the squatter's bad token: %v", err)
	}
	squat, err := a.Store.Get("web1.example")
	if err != nil || squat.State != Pending {
		t.Fatalf("the squatter left %v %v", squat, err)
	}

	real, _ := request(t, "web1.example")
	res, err := a.Enroll(Request{CSR: real, Token: secret, RemoteAddr: "10.0.0.5:1"})
	if err != nil {
		t.Fatalf("the real node, with a valid token, was refused: %v", err)
	}
	if res.State != Accepted || res.Superseded != squat.Fingerprint {
		t.Errorf("result %+v; want accepted, superseding %s", res, squat.Fingerprint)
	}
	rec, _ := a.Store.Get("web1.example")
	if rec.Fingerprint == squat.Fingerprint || rec.State != Accepted || rec.Source != SourceToken {
		t.Errorf("the record is %+v", rec)
	}

	// Once accepted the name is established: another key is refused,
	// token or no token.
	thief, _ := request(t, "web1.example")
	_, secret2, _ := a.Store.MintToken(TokenOptions{TTL: time.Hour, NodeGlob: "web*.example"}, now)
	if _, err := a.Enroll(Request{CSR: thief, Token: secret2, RemoteAddr: "10.0.0.6:1"}); !errors.Is(err, ErrRefused) {
		t.Errorf("a valid token took an accepted name: %v", err)
	}

	// A pending request is superseded only by a token that spends: a
	// second bad token leaves the first pending record where it was.
	other, _ := request(t, "web2.example")
	if _, err := a.Enroll(Request{CSR: other, Token: "guessed", RemoteAddr: "192.0.2.9:1"}); !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
	first, _ := a.Store.Get("web2.example")
	other2, _ := request(t, "web2.example")
	if _, err := a.Enroll(Request{CSR: other2, Token: "guessed-again", RemoteAddr: "192.0.2.9:1"}); !errors.Is(err, ErrRefused) {
		t.Fatal(err)
	}
	if now2, _ := a.Store.Get("web2.example"); now2.Fingerprint != first.Fingerprint {
		t.Errorf("a bad token replaced a pending record")
	}

	// In manual mode nothing changes: first key wins, and the operator
	// decides.
	m, _ := newAuthority(t)
	one, _ := request(t, "db1.example")
	two, _ := request(t, "db1.example")
	if _, err := m.Enroll(Request{CSR: one}); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	if _, err := m.Enroll(Request{CSR: two}); !errors.Is(err, ErrRefused) {
		t.Errorf("manual mode, a second key: %v", err)
	}
}
