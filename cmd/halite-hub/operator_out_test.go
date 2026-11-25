package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// `keys operator create` replaces the pair at its destination, which is
// how an operator certificate is renewed, and `--out` lets the destination
// be any path. `--out <pki_dir>/ca` replaced the enrollment CA's key and
// certificate with an operator's and exited 0; the hub then refused to
// start, and the CA could not be got back. Now a pair is replaced only when
// the certificate there is an operator certificate for the same name.
// DIVERGENCE 5.745.
func TestOperatorCreateReplacesOnlyThatOperatorsPair(t *testing.T) {
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	files := pki.Files{Dir: pkiDir}
	ca, err := files.CreateCA(pki.ECDSAP256, "test CA", 10*365*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// The hub's own pair, as serve would leave it.
	hubKey, err := pki.GenerateKey(pki.ECDSAP256)
	if err != nil {
		t.Fatal(err)
	}
	hubDER, err := ca.IssueHub(hubKey, []string{"localhost"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteKey(pki.HubKeyFile, hubKey); err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCert(pki.HubCertFile, hubDER); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "hub.yaml")
	body := fmt.Sprintf("pki_dir: %s\nstate_dir: %s\ncache_dir: %s\n",
		pkiDir, filepath.Join(root, "state"), filepath.Join(root, "cache"))
	if err := os.WriteFile(config, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	create := func(name string, extra ...string) result {
		t.Helper()
		return run(t, append([]string{"keys", "operator", "create", name, "--config", config}, extra...)...)
	}
	mustCreate := func(name string, extra ...string) {
		t.Helper()
		if res := create(name, extra...); res.code != 0 {
			t.Fatalf("operator create %s %v: exited %d: %s", name, extra, res.code, res.stderr)
		}
	}
	snapshot := func(paths ...string) [][]byte {
		var out [][]byte
		for _, p := range paths {
			b, _ := os.ReadFile(p)
			out = append(out, b)
		}
		return out
	}

	mustCreate("bob")
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatal(err)
	}
	lone := filepath.Join(elsewhere, "signer")
	if err := os.WriteFile(lone+".key", []byte("a signer's key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lone+".pub", []byte("its public half\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	junk := filepath.Join(elsewhere, "junk")
	if err := os.WriteFile(junk+".crt", []byte("not PEM\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, out, says string
	}{
		{"the enrollment CA", filepath.Join(pkiDir, "ca"), "not an operator certificate"},
		{"the hub's own pair", filepath.Join(pkiDir, "hub"), "not an operator certificate"},
		{"another operator's pair", filepath.Join(pkiDir, "operator-bob"), "operator bob's certificate"},
		{"a key with no certificate", lone, "no certificate beside it"},
		{"a certificate that does not parse", junk, "not a certificate this command made"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := []string{tc.out + ".key", tc.out + ".crt"}
			before := snapshot(paths...)
			res := create("alice", "--out", tc.out)
			if res.code == 0 {
				t.Fatalf("--out %s was written over: %s", tc.out, res.stdout)
			}
			if !strings.Contains(res.stderr, tc.says) {
				t.Errorf("the refusal does not say %q: %s", tc.says, res.stderr)
			}
			after := snapshot(paths...)
			for i := range paths {
				if !bytes.Equal(before[i], after[i]) {
					t.Errorf("%s changed although the command was refused", paths[i])
				}
			}
		})
	}
	// The CA survived all of that.
	if _, err := files.LoadCA(nil); err != nil {
		t.Fatalf("the CA no longer loads: %v", err)
	}

	// What must still work. Re-issuing in place is how an operator
	// certificate is renewed, at the default place and at an --out of its
	// own; a key alone at the default place is what a run killed between
	// its two writes leaves, and the re-run is what mends it.
	t.Run("re-issue in place", func(t *testing.T) {
		before := snapshot(filepath.Join(pkiDir, "operator-bob.key"))
		mustCreate("bob")
		if bytes.Equal(before[0], snapshot(filepath.Join(pkiDir, "operator-bob.key"))[0]) {
			t.Error("a re-issue left the old key")
		}
	})
	t.Run("re-issue at an --out of its own", func(t *testing.T) {
		mine := filepath.Join(elsewhere, "ci")
		mustCreate("ci", "--out", mine)
		mustCreate("ci", "--out", mine)
	})
	t.Run("a key alone at the default place", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(pkiDir, "operator-carol.key"), []byte("half a run\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		mustCreate("carol")
		if _, err := files.KeyPair(pki.OperatorCertFile("carol"), "operator-carol.key"); err != nil {
			t.Fatal(err)
		}
	})
}
