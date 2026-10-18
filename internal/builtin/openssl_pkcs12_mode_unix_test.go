//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// **A pkcs12 bundle is never readable while it is written** (issue 321,
// section 5). openssl created it under the process umask and halite
// chmodded it 0600 afterwards, so with an empty password -- the private
// key in the clear -- it was world-readable for the length of the run,
// and an existing bundle at 0644 was rewritten in place at 0644. The
// openssl here is a script that writes the file it is given with -out and
// records that file's mode and path while it still runs.
func TestPKCS12BundleIsPrivateWhileWritten(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := t.TempDir()
	log := filepath.Join(dir, "seen")
	fake := filepath.Join(dir, "openssl")
	script := `#!/bin/sh
if [ "$1" = version ]; then echo "OpenSSL 3.0.13 30 Jan 2024"; exit 0; fi
out=""
while [ $# -gt 0 ]; do [ "$1" = -out ] && out="$2"; shift; done
echo bundle > "$out"
ls -ln "$out" > "` + log + `"
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"c.pem", "k.pem"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(dir, "b.p12")
	if err := os.WriteFile(bundle, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := newCtx(false)
	c.Runner = &hexec.OSRunner{}
	c.Lookup = func(name string) string {
		if name == "openssl" {
			return fake
		}
		return ""
	}
	if _, err := New().Exec.Call(c, "openssl_cert.pkcs12_create", value.MapOf(
		"path", bundle, "certificate", filepath.Join(dir, "c.pem"), "private_key", filepath.Join(dir, "k.pem"))); err != nil {
		t.Fatal(err)
	}
	seen, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(seen), "-rw-------") {
		t.Errorf("while openssl wrote it, the bundle was:\n%s", seen)
	}
	if strings.Contains(string(seen), " "+bundle+"\n") {
		t.Errorf("openssl wrote into the bundle in place, where the old one is readable:\n%s", seen)
	}
	info, err := os.Stat(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the bundle is %o", info.Mode().Perm())
	}
	if b, _ := os.ReadFile(bundle); string(b) != "bundle\n" {
		t.Errorf("the bundle holds %q", b)
	}
}
