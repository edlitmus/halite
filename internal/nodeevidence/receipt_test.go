package nodeevidence

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

func testCA(t *testing.T) *pki.CA {
	t.Helper()
	ca, err := pki.NewCA(pki.ECDSAP256, "receipt test CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

const (
	someHash  = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherHash = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// A receipt the hub signs is one the node can check, and changing any of
// the four things it is about -- or who signed it -- makes it fail.
//
// Each alteration is checked separately, because a payload that left one
// field out would still round-trip and would still reject the other
// three: the signature would simply not be about that field, and a hub
// could then sign one head and be held to a different one.
func TestAReceiptVerifiesAndAnAlteredOneDoesNot(t *testing.T) {
	ca := testCA(t)
	received := ReceivedNow(time.Date(2026, 10, 6, 12, 0, 0, 123456789, time.UTC))
	sig, err := SignReceipt(ca.Key, "web1.example", 42, someHash, received)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReceipt(ca.Cert, "web1.example", 42, someHash, received, sig); err != nil {
		t.Fatalf("a receipt the CA signed does not verify: %v", err)
	}

	later := ReceivedNow(time.Date(2026, 10, 6, 12, 0, 1, 123456789, time.UTC))
	cases := map[string]func() error{
		"another node": func() error { return VerifyReceipt(ca.Cert, "db1.example", 42, someHash, received, sig) },
		"another seq":  func() error { return VerifyReceipt(ca.Cert, "web1.example", 43, someHash, received, sig) },
		"another hash": func() error { return VerifyReceipt(ca.Cert, "web1.example", 42, otherHash, received, sig) },
		"another time": func() error { return VerifyReceipt(ca.Cert, "web1.example", 42, someHash, later, sig) },
		"another CA": func() error {
			return VerifyReceipt(testCA(t).Cert, "web1.example", 42, someHash, received, sig)
		},
		"a damaged signature": func() error {
			der, _ := base64.StdEncoding.DecodeString(sig)
			der[len(der)-1] ^= 1
			return VerifyReceipt(ca.Cert, "web1.example", 42, someHash, received,
				base64.StdEncoding.EncodeToString(der))
		},
	}
	for name, check := range cases {
		if check() == nil {
			t.Errorf("a receipt with %s verified", name)
		}
	}
}

// The payload starts with text, never 0x30, so a receipt the CA key
// signs can never be read as the DER of a certificate or a revocation
// list -- the only other things that key signs.
func TestAReceiptPayloadCannotBeMistakenForDER(t *testing.T) {
	payload, err := ReceiptPayload("web1.example", 1, someHash, ReceivedNow(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if payload[0] == 0x30 {
		t.Fatal("the payload begins as a DER SEQUENCE does")
	}
	if !strings.HasPrefix(string(payload), "halite evidence anchor receipt v1\n") {
		t.Fatalf("the payload does not begin with its domain: %q", payload)
	}
}

// No field can carry a newline, which is what keeps the line encoding
// unambiguous; and a time has exactly one spelling, so the text stored
// beside a signature is the text the signature covers.
func TestAReceiptPayloadRefusesWhatWouldMakeItAmbiguous(t *testing.T) {
	now := ReceivedNow(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	refused := map[string]func() error{
		"a newline in the hash": func() error {
			_, err := ReceiptPayload("web1.example", 1, someHash+"\nreceived "+now, now)
			return err
		},
		"a hash of another shape": func() error {
			_, err := ReceiptPayload("web1.example", 1, "sha256:ABC", now)
			return err
		},
		"a newline in the node": func() error {
			_, err := ReceiptPayload("web1.example\nseq 9", 1, someHash, now)
			return err
		},
		"record 0": func() error {
			_, err := ReceiptPayload("web1.example", 0, someHash, now)
			return err
		},
		"a second spelling of the time": func() error {
			_, err := ReceiptPayload("web1.example", 1, someHash, "2026-10-06T12:00:00+00:00")
			return err
		},
		"a time that is not one": func() error {
			_, err := ReceiptPayload("web1.example", 1, someHash, "yesterday")
			return err
		},
	}
	for name, try := range refused {
		if try() == nil {
			t.Errorf("a payload with %s was built", name)
		}
	}
}

// What the comment on ReceiptPayload claims: a receipt can be checked by
// hand, with the payload written out and openssl. Run rather than
// asserted, because "any ECDSA tool can check this" is exactly the kind
// of sentence this project has found to be false before.
func TestOpenSSLVerifiesAReceipt(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not on this machine, so the foreign check cannot run here")
	}
	ca := testCA(t)
	received := ReceivedNow(time.Now())
	sig, err := SignReceipt(ca.Key, "web1.example", 7, someHash, received)
	if err != nil {
		t.Fatal(err)
	}
	der, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := ReceiptPayload("web1.example", 7, someHash, received)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	caPath := write("ca.crt", pki.EncodeCert(ca.Cert.Raw))
	payloadPath := write("payload", payload)
	sigPath := write("receipt.sig", der)
	pubPath := filepath.Join(dir, "ca.pub")
	if out, err := exec.Command(openssl, "x509", "-in", caPath, "-pubkey", "-noout",
		"-out", pubPath).CombinedOutput(); err != nil {
		t.Fatalf("openssl x509 -pubkey: %v: %s", err, out)
	}
	out, err := exec.Command(openssl, "dgst", "-sha256", "-verify", pubPath,
		"-signature", sigPath, payloadPath).CombinedOutput()
	if err != nil {
		t.Fatalf("openssl could not verify a receipt: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "Verified OK") {
		t.Errorf("openssl said %q", out)
	}
	// And the refusal, so that this test is not passing on an openssl
	// that verifies everything.
	write("payload", append(payload, 'x'))
	if out, err := exec.Command(openssl, "dgst", "-sha256", "-verify", pubPath,
		"-signature", sigPath, payloadPath).CombinedOutput(); err == nil {
		t.Errorf("openssl verified a receipt over a different payload: %s", out)
	}
}
