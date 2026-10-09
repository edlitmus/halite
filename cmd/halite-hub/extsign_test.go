package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// `extensions key create` and `extensions sign` make a bundle a hub
// accepts when it trusts the key and pins the root, and refuses when it
// trusts another key. Before them signing was `go run ./tools/extbundle`
// from a source checkout. DIVERGENCE 5.260.
func TestASignedBundleIsAcceptedOnlyByTheKeyItWasSignedWith(t *testing.T) {
	signer, build, tree := t.TempDir(), t.TempDir(), t.TempDir()

	// The executable is this test binary: a real Go executable for the
	// platform the test runs on, which is what the signer reads.
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "hello"), self, 0o755); err != nil {
		t.Fatal(err)
	}

	trust := func(name string) string {
		got := run(t, "extensions", "key", "create", name, "--root", signer)
		if got.code != 0 {
			t.Fatalf("key create %s: %+v", name, got)
		}
		line := regexp.MustCompile(`(?m)^  - '(` + name + ` [A-Za-z0-9+/=]+)'$`).FindStringSubmatch(got.stdout)
		if line == nil {
			t.Fatalf("key create printed no extension_trust_keys line:\n%s", got.stdout)
		}
		return line[1]
	}
	release, other := trust("release"), trust("other")

	got := run(t, "extensions", "sign", build, "--name", "hello", "--exe", "hello",
		"--ext-version", "2.1.0", "--key", "release", "--root", signer, "--publish", tree)
	if got.code != 0 {
		t.Fatalf("sign: %+v", got)
	}
	root := regexp.MustCompile(`(?m)^root +([0-9a-f]{64})$`).FindStringSubmatch(got.stdout)
	if root == nil {
		t.Fatalf("sign printed no root:\n%s", got.stdout)
	}
	published := filepath.Join(tree, "_ext", "hello", "2.1.0")
	for _, f := range []string{"hello", "manifest.json", "manifest.sig"} {
		if _, err := os.Stat(filepath.Join(published, f)); err != nil {
			t.Errorf("the published bundle has no %s: %v", f, err)
		}
	}

	sync := func(trusted string) string {
		hub := t.TempDir()
		config := filepath.Join(hub, "hub.yaml")
		body := fmt.Sprintf("pki_dir: %s\nstate_dir: %s\ncache_dir: %s\nfile_roots:\n  base: [%s]\n"+
			"extension_require_signature: true\nextension_trust_keys:\n  - '%s'\n"+
			"extension_pins:\n  hello:\n    version: 2.1.0\n    root: %s\n",
			filepath.Join(hub, "pki"), filepath.Join(hub, "state"), filepath.Join(hub, "cache"),
			tree, trusted, root[1])
		if err := os.WriteFile(config, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return run(t, "extensions", "sync", "--root", hub, "--config", config).stdout
	}
	if out := sync(release); !strings.Contains(out, "fetched   hello 2.1.0") {
		t.Errorf("a hub trusting the signing key did not fetch the bundle:\n%s", out)
	}
	if out := sync(other); !strings.Contains(out, "refused   hello 2.1.0") ||
		!strings.Contains(out, "does not verify") {
		t.Errorf("a hub trusting another key did not refuse the bundle:\n%s", out)
	}
}

// What these commands refuse, so that a mistake does not quietly make
// something no node will trust or overwrite something every node does.
func TestSigningRefusesWhatItShould(t *testing.T) {
	signer, build, tree := t.TempDir(), t.TempDir(), t.TempDir()
	self, _ := os.ReadFile(os.Args[0])
	if err := os.WriteFile(filepath.Join(build, "hello"), self, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := run(t, "extensions", "key", "create", "release", "--root", signer); got.code != 0 {
		t.Fatalf("key create: %+v", got)
	}

	// A key is never replaced: everything it signed would stop verifying.
	if got := run(t, "extensions", "key", "create", "release", "--root", signer); got.code == 0 ||
		!strings.Contains(got.stderr, "never replaced") {
		t.Errorf("key create replaced an existing key: %+v", got)
	}
	// A missing key is an error, not a new key nobody trusts.
	if got := run(t, "extensions", "sign", build, "--name", "hello", "--exe", "hello",
		"--key", "nosuch", "--root", signer); got.code == 0 ||
		!strings.Contains(got.stderr, "extensions key create nosuch") {
		t.Errorf("sign with a missing key: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(signer, "pki", "extension-nosuch.key")); !os.IsNotExist(err) {
		t.Error("sign made the key it was asked to use")
	}
	// A published version is never replaced.
	sign := func() result {
		return run(t, "extensions", "sign", build, "--name", "hello", "--exe", "hello",
			"--key", "release", "--root", signer, "--publish", tree)
	}
	if got := sign(); got.code != 0 {
		t.Fatalf("first publish: %+v", got)
	}
	if got := sign(); got.code == 0 || !strings.Contains(got.stderr, "never replaced") {
		t.Errorf("a second publish of 1.0.0 replaced the first: %+v", got)
	}
}
