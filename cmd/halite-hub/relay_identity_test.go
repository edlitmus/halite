package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// writeRelayPair enrols a relay into dir the way `halite-node enroll`
// leaves it: node.crt and node.key, issued by a CA standing in for the
// upstream's.
func writeRelayPair(t *testing.T, dir, nodeID string) pki.Files {
	t.Helper()
	ca, err := pki.NewCA(pki.ECDSAP256, "upstream CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := pki.GenerateKey(pki.ECDSAP256)
	csrDER, _ := pki.NewNodeCSR(key, nodeID)
	csr, err := pki.DecodeCSR(pki.EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}
	der, err := ca.IssueNode(csr, nodeID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	files := pki.Files{Dir: dir}
	if err := files.WriteKey(pki.NodeKeyFile, key); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCertPEM(pki.NodeCertFile, pki.EncodeCert(der)); err != nil {
		t.Fatal(err)
	}
	return files
}

// The relay's identity upstream is the one its certificate names. It was
// read from `node_id`, which a hub's configuration does not have, so a
// relay set up as the operations guide says did not start. And a pair
// given outside relay_pki_dir is not one a renewal, which writes to the
// directory, may replace. DIVERGENCE 5.263.
func TestARelaysIdentityIsItsCertificates(t *testing.T) {
	files := writeRelayPair(t, t.TempDir(), "relay1.example")
	id, err := relayIdentityFrom(files, files.Path(pki.NodeCertFile), files.Path(pki.NodeKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if id.nodeID != "relay1.example" {
		t.Errorf("the relay's identity is %q, want the certificate's relay1.example", id.nodeID)
	}
	if !id.renewable {
		t.Error("a pair in relay_pki_dir should be renewable")
	}

	elsewhere := filepath.Join(t.TempDir(), "relay.key")
	if err := os.Rename(files.Path(pki.NodeKeyFile), elsewhere); err != nil {
		t.Fatal(err)
	}
	id, err = relayIdentityFrom(files, files.Path(pki.NodeCertFile), elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if id.renewable {
		t.Error("a key given outside relay_pki_dir was taken as renewable; a renewal would write a different one")
	}

	if _, err := relayIdentityFrom(files, filepath.Join(t.TempDir(), "missing.crt"), elsewhere); err == nil ||
		!strings.Contains(err.Error(), "missing.crt") {
		t.Errorf("a missing certificate gave %v", err)
	}
}
