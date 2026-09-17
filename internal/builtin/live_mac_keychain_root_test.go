package builtin

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// `mac_keychain`'s writes, driven against the real `security` on this
// Mac.
//
// # Two tests, because two things need showing
//
// The round trip — import a certificate, find it, remove it, find it
// gone — needs no root at all when the keychain is one the test made.
// That is the thorough one, and it is the one that could run on a Mac
// in CI if there ever is one.
//
// The System keychain is the reason every write here declares root. It
// gets its own test, doing the least that demonstrates the privileged
// path: one import, one read, one removal.
//
// # The PKCS#12 has to be one `security` will accept
//
// Generating it is not a formality. OpenSSL 3 defaults to a SHA-256 MAC
// that Apple's Security framework cannot verify, and `security import`
// answers "MAC verification failed during PKCS12 import (wrong
// password?)" — which blames the password for an algorithm problem and
// would have sent somebody looking in the wrong place for an hour.
// `-macalg sha1` alone then fails differently ("Unknown format in
// import"). The form below works on both the OpenSSL 3.6 a Mac may have
// from Homebrew and the LibreSSL 3.3 at /usr/bin/openssl, which is why
// it names the algorithms rather than passing `-legacy` — LibreSSL has
// no such flag.
//
// # What it touches
//
// A keychain file and a certificate under `t.TempDir()`, and — in the
// root test only — one certificate named `halite-selftest-<pid>` in
// `/Library/Keychains/System.keychain`, imported and removed. It is
// self-signed and is given no trust settings, so at no point does this
// Mac trust anything new.

const macKeychainTestPassword = "halite-selftest-p12"

// macKeychainSelfSigned writes a throwaway PKCS#12 and returns its path
// and the friendly name inside it.
func macKeychainSelfSigned(t *testing.T, c *exec.Context, dir, name string) string {
	t.Helper()
	openssl := c.Which("openssl")
	if openssl == "" {
		t.Skip("no `openssl` to make a certificate with")
	}

	key := filepath.Join(dir, "key.pem")
	cert := filepath.Join(dir, "cert.pem")
	p12 := filepath.Join(dir, "cert.p12")

	run := func(argv ...string) {
		t.Helper()
		res, err := c.Run(exec.Command{Argv: argv, IgnoreExitCode: true})
		if err != nil {
			t.Fatalf("%s: %v", strings.Join(argv, " "), err)
		}
		if res.Code != 0 {
			t.Fatalf("%s exited %d: %s", strings.Join(argv, " "), res.Code, res.Stderr+res.Stdout)
		}
	}

	run("openssl", "req", "-x509", "-newkey", "rsa:2048", "-keyout", key, "-out", cert,
		"-days", "1", "-nodes", "-subj", "/CN="+name)
	run("openssl", "pkcs12", "-export", "-out", p12, "-inkey", key, "-in", cert,
		"-passout", "pass:"+macKeychainTestPassword, "-name", name,
		"-keypbe", "PBE-SHA1-3DES", "-certpbe", "PBE-SHA1-3DES", "-macalg", "sha1")
	return p12
}

func macKeychainLive(t *testing.T) *exec.Context {
	t.Helper()
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1 to import a certificate on this Mac")
	}
	if runtime.GOOS != "darwin" {
		t.Skipf("mac_keychain is macOS's, and this is %s", runtime.GOOS)
	}
	c := realCtx(t)
	if c.Which("security") == "" {
		t.Fatal("HALITE_SYSTEM_LIVE is set and there is no `security`; this is not a Mac")
	}
	return c
}

// The whole round trip, in a keychain this test made. No root: a
// keychain a test owns is one it can write.
func TestLiveMacKeychainRoundTripsAThrowawayKeychain(t *testing.T) {
	c := macKeychainLive(t)

	dir := t.TempDir()
	name := "halite-selftest-throwaway"
	p12 := macKeychainSelfSigned(t, c, dir, name)
	keychain := filepath.Join(dir, "halite-selftest.keychain-db")

	res, err := c.Run(exec.Command{
		Argv:           []string{"security", "create-keychain", "-p", "halite-selftest-kc", keychain},
		IgnoreExitCode: true,
	})
	if err != nil || res.Code != 0 {
		t.Fatalf("create-keychain: %v %s", err, res.Stderr+res.Stdout)
	}
	t.Cleanup(func() {
		_, _ = c.Run(exec.Command{
			Argv:           []string{"security", "delete-keychain", keychain},
			IgnoreExitCode: true,
		})
	})

	// friendly_name reads the file, not the keychain, and is the one
	// function here that shells to `openssl` rather than `security`.
	got, err := macKeychainFriendlyName(c, p12, macKeychainTestPassword)
	if err != nil {
		t.Fatalf("friendly_name: %v", err)
	}
	if got != name {
		t.Errorf("friendly_name read %#v, want %q", got, name)
	}

	if err := macKeychainInstall(c, p12, macKeychainTestPassword, keychain, true, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	certs, err := macKeychainCerts(c, keychain, "")
	if err != nil {
		t.Fatalf("reading the keychain back: %v", err)
	}
	if len(certs) != 1 {
		t.Fatalf("a keychain with one imported certificate holds %d: %+v", len(certs), certs)
	}
	if certs[0].Name != name {
		t.Errorf("the certificate read back as %q, want %q", certs[0].Name, name)
	}
	if len(certs[0].Hash) != 40 {
		t.Errorf("the SHA-1 hash read back as %q, which is not 40 hex characters", certs[0].Hash)
	}

	// Importing the same certificate again is the converged state, and
	// must not leave the keychain holding two.
	if err := macKeychainInstall(c, p12, macKeychainTestPassword, keychain, true, nil); err != nil {
		t.Errorf("a second install of the same certificate was an error: %v", err)
	}
	again, err := macKeychainCerts(c, keychain, "")
	if err != nil {
		t.Fatalf("reading the keychain after a second install: %v", err)
	}
	if len(again) != 1 {
		t.Errorf("a second install left the keychain holding %d certificates", len(again))
	}

	if err := macKeychainUninstall(c, name, keychain); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	gone, err := macKeychainCerts(c, keychain, "")
	if err != nil {
		t.Fatalf("reading the keychain after uninstall: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("the certificate is still there after uninstall: %+v", gone)
	}

	// The one that could not converge before: uninstalling what is
	// already gone.
	if err := macKeychainUninstall(c, name, keychain); err != nil {
		t.Errorf("uninstalling an already-absent certificate was an error, so a tree "+
			"carrying this under `module.run` fails on every run after the first: %v", err)
	}
}

// The System keychain, which is why these functions declare root.
func TestLiveMacKeychainImportsIntoTheSystemKeychain(t *testing.T) {
	c := macKeychainLive(t)
	if os.Geteuid() != 0 {
		t.Skip("writing the System keychain needs root; run it under sudo")
	}

	const system = "/Library/Keychains/System.keychain"
	dir := t.TempDir()
	name := "halite-selftest-system"
	p12 := macKeychainSelfSigned(t, c, dir, name)

	// Refuse to run if something of this name is already there, rather
	// than removing somebody else's certificate in the cleanup.
	if found, err := macKeychainCerts(c, system, name); err == nil && len(found) > 0 {
		t.Fatalf("%q is already in the System keychain; refusing to touch it", name)
	}
	t.Cleanup(func() { _ = macKeychainUninstall(c, name, system) })

	if err := macKeychainInstall(c, p12, macKeychainTestPassword, system, false, nil); err != nil {
		t.Fatalf("importing into the System keychain: %v", err)
	}

	found, err := macKeychainCerts(c, system, name)
	if err != nil {
		t.Fatalf("reading the System keychain: %v", err)
	}
	if len(found) == 0 {
		t.Fatalf("%q was imported into the System keychain and cannot be found there", name)
	}
	if found[0].Name != name {
		t.Errorf("read back as %q, want %q", found[0].Name, name)
	}

	if err := macKeychainUninstall(c, name, system); err != nil {
		t.Fatalf("removing it from the System keychain: %v", err)
	}
	after, err := macKeychainCerts(c, system, name)
	if err != nil {
		t.Fatalf("reading the System keychain after removal: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("%q is still in the System keychain after uninstall: %+v", name, after)
	}
}
