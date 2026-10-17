package renewal

import (
	"errors"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// issued makes a node key and a certificate for it, as PEM.
func issued(t *testing.T, ca *pki.CA) ([]byte, func(pki.Files) error) {
	t.Helper()
	key, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	der, err := pki.NewNodeCSR(key, "web1.example")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := pki.DecodeCSR(pki.EncodeCSR(der))
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.IssueNode(csr, "web1.example", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return pki.EncodeCert(cert), func(f pki.Files) error { return f.WriteKey(pki.NodeKeyFile, key) }
}

// **A renewal interrupted after its key keeps the certificate it was
// issued** (issue 321, M-26). It wrote the key and then the certificate,
// and the hub revokes the old serial when it issues: dying in between
// left the new key, the old revoked certificate and no copy of the new
// one. Now the certificate is on disk first, as node.crt.new, and Recover
// finishes the install the next time the node loads its identity.
func TestAnInterruptedInstallIsFinishedByRecover(t *testing.T) {
	ca, err := pki.NewCA(pki.ECDSAP256, "test CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	files := pki.Files{Dir: t.TempDir()}
	oldCert, writeOldKey := issued(t, ca)
	if err := writeOldKey(files); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCertPEM(pki.NodeCertFile, oldCert); err != nil {
		t.Fatal(err)
	}

	// Interrupted after the key: the new key is in place, the rename of
	// the certificate never happens.
	newCert, writeNewKey := issued(t, ca)
	stop := errors.New("the process died here")
	err = InstallIdentity(files, &transport.Enrollment{CertPEM: newCert}, func() error {
		if err := SetKeyAsideForTest(files); err != nil {
			return err
		}
		if err := writeNewKey(files); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("InstallIdentity: %v", err)
	}
	if _, err := files.KeyPair(pki.NodeCertFile, pki.NodeKeyFile); err == nil {
		t.Fatal("the test did not reach the state it is about: the pair still matches")
	}
	installed, err := Recover(files)
	if err != nil || !installed {
		t.Fatalf("Recover: %v %v", installed, err)
	}
	if _, err := files.KeyPair(pki.NodeCertFile, pki.NodeKeyFile); err != nil {
		t.Errorf("after Recover the pair does not match: %v", err)
	}

	// Interrupted before the key: node.crt.new does not go with the key in
	// place, which is still the old pair's, so it is dropped.
	stale, _ := issued(t, ca)
	if err := files.WriteCertPEM(NewCertFile, stale); err != nil {
		t.Fatal(err)
	}
	installed, err = Recover(files)
	if err != nil || installed {
		t.Fatalf("Recover over a stale node.crt.new: %v %v", installed, err)
	}
	if files.Exists(NewCertFile) {
		t.Error("a node.crt.new that matched nothing was kept")
	}
	if _, err := files.KeyPair(pki.NodeCertFile, pki.NodeKeyFile); err != nil {
		t.Errorf("the working pair was disturbed: %v", err)
	}
}

// SetKeyAsideForTest moves node.key aside as a renewal does.
func SetKeyAsideForTest(files pki.Files) error {
	_, err := SetKeyAside(files, time.Now())
	return err
}
