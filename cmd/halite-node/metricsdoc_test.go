package main

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestTheDocumentedMetricsCertificateStateConverges applies the
// node-serving-certificate state from docs/metrics.md as the page prints
// it, against a pillar holding a real metrics CA as PEM text.
//
// The page said both certificate routes had been run end to end, and the
// hub-side one had. The state's pillar form had not: it substituted the
// PEM bare, and a PEM block's second line then starts a YAML line, so
// the state did not compile. Nothing ran the block that was published.
// So the block is read from the page itself rather than copied here, and
// a page that drifts from what works fails this test. DIVERGENCE 5.241.
//
// It runs twice: with the CA's key in plain pillar, and GPG-encrypted as
// the page tells an operator to keep it. Decrypted pillar values are
// handled apart from the rest -- they are redacted from every output --
// so that the plain form works says nothing about the encrypted one.
func TestTheDocumentedMetricsCertificateStateConverges(t *testing.T) {
	t.Run("plain", func(t *testing.T) { documentedMetricsCert(t, false) })
	t.Run("gpg", func(t *testing.T) {
		if _, err := exec.LookPath("gpg"); err != nil {
			t.Skip("no gpg on PATH; SPEC 12.6 drives the system binary")
		}
		documentedMetricsCert(t, true)
	})
}

func documentedMetricsCert(t *testing.T, encrypt bool) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "metrics.md"))
	if err != nil {
		t.Fatal(err)
	}
	const marker = "# states/metrics_cert.sls\n"
	i := strings.Index(string(doc), marker)
	if i < 0 {
		t.Fatalf("docs/metrics.md has no %q block", strings.TrimSpace(marker))
	}
	sls := string(doc[i:])
	sls = sls[:strings.Index(sls, "```")]

	// The CA lives wherever its maker keeps it; only the pillar carries
	// it to the node.
	caDir := t.TempDir()
	caKey, caCert := filepath.Join(caDir, "metrics-ca.key"), filepath.Join(caDir, "metrics-ca.crt")

	// The CA is made as the page makes it.
	for _, args := range [][]string{
		{"call", "x509.create_private_key", "path=" + caKey, "algo=ec", "keysize=256"},
		{"call", "x509.create_certificate", "private_key=" + caKey, "path=" + caCert,
			"ca=true", "CN=halite metrics CA", "days_valid=3650"},
	} {
		if got := run(t, args...); got.code != 0 {
			t.Fatalf("%v: %+v", args, got)
		}
	}
	indent := func(path string) string {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		for _, line := range strings.SplitAfter(strings.TrimRight(string(b), "\n"), "\n") {
			out.WriteString("    " + line)
		}
		return out.String() + "\n"
	}

	flags := tree(t, map[string]string{"metrics_cert.sls": sls})
	// The state asks the node for pki_dir, which defaults to <root>/pki;
	// an enrolled node has that directory, so make it as enrollment
	// would. The block names no path of its own, so a test that passed
	// with the files anywhere else would be testing something other
	// than what the page prints. DIVERGENCE 5.242.
	pki := filepath.Join(flags[slices.Index(flags, "--root")+1], "pki")
	if err := os.Mkdir(pki, 0o700); err != nil {
		t.Fatal(err)
	}
	// tree points --pillar-root at an empty directory; fill it.
	pillar := flags[slices.Index(flags, "--pillar-root")+1]
	body := "metrics_ca:\n  cert: |\n" + indent(caCert) + "  key: |\n" + indent(caKey)
	if encrypt {
		home := gpgHome(t)
		armored := gpgEncrypt(t, home, caKey)
		body = "#!yaml|gpg\nmetrics_ca:\n  cert: |\n" + indent(caCert) + "  key: |\n" + indent(armored)
		cfg := writeFiles(t, t.TempDir(), map[string]string{"node.yaml": "gpg_home: " + home + "\n"})
		flags = append(flags, "--config", filepath.Join(cfg, "node.yaml"))
	}
	writeFiles(t, pillar, map[string]string{
		"top.sls":        "base:\n  '*':\n    - metrics_ca\n",
		"metrics_ca.sls": body,
	})

	// The exit code is the node's contract: 0 when something changed, 2
	// when the run converged with nothing to do.
	apply := func(want int) result {
		got := run(t, append([]string{"state", "sls", "metrics_cert"}, flags...)...)
		if got.code != want {
			t.Fatalf("the documented state exited %d, want %d: %+v", got.code, want, got)
		}
		return got
	}
	if got := apply(0); !strings.Contains(got.stdout, "changed=2") {
		t.Fatalf("the first run should write the key and the certificate:\n%s", got.stdout)
	}
	if got := apply(2); strings.Contains(got.stdout, "changed=") {
		t.Fatalf("the second run should change nothing:\n%s", got.stdout)
	}

	// What it wrote is what Prometheus will be asked to trust: issued by
	// the metrics CA, for serving.
	load := func(path string) *x509.Certificate {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		block, _ := pem.Decode(b)
		if block == nil {
			t.Fatalf("%s holds no PEM", path)
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	roots := x509.NewCertPool()
	roots.AddCert(load(caCert))
	if _, err := load(filepath.Join(pki, "metrics.crt")).Verify(x509.VerifyOptions{
		Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatalf("the certificate does not verify against the metrics CA for serving: %v", err)
	}
}

// gpgHome makes a throwaway keyring with one key and no passphrase, as
// TestDecryptedPillarNeverReachesTheRun does.
func gpgHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	gpg := exec.Command("gpg", "--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--quick-generate-key", "halite test <t@example.invalid>", "default", "default", "never")
	gpg.Env = append(os.Environ(), "GNUPGHOME="+home)
	if out, err := gpg.CombinedOutput(); err != nil {
		t.Skipf("a throwaway key could not be generated here: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		kill := exec.Command("gpgconf", "--kill", "gpg-agent")
		kill.Env = append(os.Environ(), "GNUPGHOME="+home)
		_ = kill.Run()
	})
	return home
}

// gpgEncrypt encrypts a file to the keyring's key and writes the armored
// result beside it, returning its path. AES256 for the reason
// TestDecryptedPillarNeverReachesTheRun gives: FIPS hosts. DIVERGENCE 5.83.
func gpgEncrypt(t *testing.T, home, path string) string {
	t.Helper()
	out := path + ".asc"
	gpg := exec.Command("gpg", "--batch", "--yes", "--trust-model", "always", "--cipher-algo", "AES256",
		"--encrypt", "--armor", "-r", "t@example.invalid", "--output", out, path)
	gpg.Env = append(os.Environ(), "GNUPGHOME="+home)
	if b, err := gpg.CombinedOutput(); err != nil {
		t.Fatalf("encrypting: %v\n%s", err, b)
	}
	return out
}
