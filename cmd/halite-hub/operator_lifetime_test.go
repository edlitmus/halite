package main

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/pki"
)

// `keys operator create --lifetime` issued whatever it was given: -1h a
// certificate already expired, 0 one that expired as it was written, and
// 876000h one good for a century under a CA of ten. Each printed as a
// success. Now a lifetime that is not positive is refused before the hub
// is opened, one that runs past the CA is refused before anything is
// issued, and neither leaves a certificate behind. DIVERGENCE 5.606.
func TestOperatorLifetimeIsChecked(t *testing.T) {
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	if _, err := (pki.Files{Dir: pkiDir}).CreateCA(pki.ECDSAP256, "test CA", 10*365*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "hub.yaml")
	body := fmt.Sprintf("pki_dir: %s\nstate_dir: %s\ncache_dir: %s\n",
		pkiDir, filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err := os.WriteFile(config, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ lifetime, says string }{
		{"-1h", "not a positive duration"},
		{"0", "not a positive duration"},
		{"0s", "not a positive duration"},
		{"soon", "--lifetime"},
		{"876000h", "runs past the enrollment CA"},
	} {
		name := "op" + strings.NewReplacer("-", "m", "0", "z").Replace(tc.lifetime)
		res := run(t, "keys", "operator", "create", name, "--config", config, "--lifetime="+tc.lifetime)
		if res.code != cli.ExitUsage {
			t.Errorf("--lifetime %s: exited %d, want %d: %s", tc.lifetime, res.code, cli.ExitUsage, res.stderr)
		}
		if !strings.Contains(res.stderr, tc.says) || !strings.Contains(res.stderr, "--lifetime") {
			t.Errorf("--lifetime %s: the message does not say %q and name the flag: %s", tc.lifetime, tc.says, res.stderr)
		}
		if _, err := os.Stat(filepath.Join(pkiDir, "operator-"+name+".crt")); err == nil {
			t.Errorf("--lifetime %s: a certificate was written all the same", tc.lifetime)
		}
	}

	// The control: a lifetime the CA can back is issued, for that long.
	res := run(t, "keys", "operator", "create", "alice", "--config", config, "--lifetime", "48h")
	if res.code != 0 {
		t.Fatalf("--lifetime 48h: exited %d: %s", res.code, res.stderr)
	}
	data, err := os.ReadFile(filepath.Join(pkiDir, "operator-alice.crt"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("the operator certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if left := time.Until(cert.NotAfter); left < 47*time.Hour || left > 49*time.Hour {
		t.Errorf("a 48h certificate expires in %s", left)
	}
}
