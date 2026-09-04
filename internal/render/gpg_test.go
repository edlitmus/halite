package render

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// gpgKeyring builds a throwaway GNUPGHOME with one key in it, and returns
// the home and the recipient. It skips loudly rather than failing where
// gpg cannot be driven: this exercises the system binary, and a machine
// without one is not a defect in the renderer.
func gpgKeyring(t *testing.T) (home, recipient string) {
	t.Helper()
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg differential skipped: no gpg on PATH. SPEC 12.6 drives the system binary.")
	}
	home = t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	recipient = "halite-test@example.invalid"

	run := func(args ...string) (string, error) {
		cmd := exec.Command("gpg", args...)
		cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run("--batch", "--pinentry-mode", "loopback", "--passphrase", "",
		"--quick-generate-key", "halite test <"+recipient+">", "default", "default", "never")
	if err != nil {
		t.Skipf("gpg differential skipped: a throwaway key could not be generated here: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		// The agent holds the socket open under the temp directory, and
		// a running one would keep it from being removed.
		cmd := exec.Command("gpgconf", "--kill", "gpg-agent")
		cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
		_ = cmd.Run()
	})
	return home, recipient
}

func gpgEncrypt(t *testing.T, home, recipient, plaintext string) string {
	t.Helper()
	cmd := exec.Command("gpg", "--batch", "--yes", "--trust-model", "always",
		"--encrypt", "--armor", "-r", recipient)
	cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
	cmd.Stdin = strings.NewReader(plaintext)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("encrypting: %v", err)
	}
	return string(out)
}

// TestGPGRendererDecryptsAPillarTree is the whole point: an existing
// Salt tree's `#!yaml|gpg` pillar has to come out with plaintext values.
func TestGPGRendererDecryptsAPillarTree(t *testing.T) {
	home, recipient := gpgKeyring(t)
	secret := gpgEncrypt(t, home, recipient, "s3cret-value")

	// Indented the way Salt trees carry it: a block scalar under a key.
	indented := strings.ReplaceAll(strings.TrimRight(secret, "\n"), "\n", "\n      ")
	src := "#!yaml|gpg\n" +
		"users:\n" +
		"  ed:\n" +
		"    password: |\n      " + indented + "\n" +
		"    shell: /bin/sh\n" +
		"ports:\n" +
		"  - 80\n" +
		"  - 443\n"

	res, err := Render([]byte(src), Options{File: "users.sls", GPG: GPGOptions{Home: home}})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	m, ok := res.Value.(*value.Map)
	if !ok {
		t.Fatalf("value = %T", res.Value)
	}
	got, _ := value.Traverse(m, "users:ed:password", ":")
	if got != "s3cret-value" {
		t.Errorf("password = %#v, want the plaintext", got)
	}

	// Everything that is not a PGP message is untouched, including the
	// types: a pillar whose integers became strings would break every
	// comparison downstream.
	if v, _ := value.Traverse(m, "users:ed:shell", ":"); v != "/bin/sh" {
		t.Errorf("shell = %#v", v)
	}
	ports, _ := m.Get("ports")
	list, ok := ports.([]any)
	if !ok || len(list) != 2 || list[0] != int64(80) {
		t.Errorf("ports = %#v, want the parsed integers", ports)
	}
}

func TestGPGRendererFailsLoudly(t *testing.T) {
	home, recipient := gpgKeyring(t)

	// A message this keyring cannot open is an error naming where it
	// was, and never the ciphertext or a partial result.
	other := t.TempDir()
	res, err := Render([]byte("#!yaml|gpg\nk:\n  nested: |\n    "+
		strings.ReplaceAll(strings.TrimRight(gpgEncrypt(t, home, recipient, "x"), "\n"), "\n", "\n    ")+"\n"),
		Options{File: "p.sls", GPG: GPGOptions{Home: other}})
	if err == nil {
		t.Fatalf("decryption with the wrong keyring should fail, got %#v", res.Value)
	}
	if !strings.Contains(err.Error(), "k:nested") {
		t.Errorf("the error should name the key path, got: %v", err)
	}

	// SPEC 12.6: a missing binary fails at load rather than at the first
	// encrypted value, so a tree with no encrypted values still reports
	// that the renderer cannot run.
	_, err = Render([]byte("#!yaml|gpg\nk: plain\n"),
		Options{File: "p.sls", GPG: GPGOptions{Binary: filepath.Join(t.TempDir(), "no-such-gpg")}})
	if err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("a missing binary should fail at load: %v", err)
	}
}

// The ciphertext must not reach the argument vector, where every account
// on the machine can read it while the process runs. SPEC 12.6 states the
// rule; this checks it by standing in for gpg and recording what arrived.
func TestGPGCiphertextGoesOnStandardInputNeverTheArgumentVector(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	// A compiled stand-in rather than a shell script. The script was
	// `#!/bin/sh`, which is not a program on every platform this ships
	// to: on Windows it could not be started at all, so the one check
	// that this ciphertext never reaches an argument vector did not run
	// there.
	stand := buildStandIn(t, dir)
	t.Setenv("HALITE_STANDIN_ARGV", argvFile)

	const ciphertext = pgpHeader + "\nZGVmaW5pdGVseS1zZWNyZXQ=\n-----END PGP MESSAGE-----"
	src := "#!yaml|gpg\nk: |\n  " + strings.ReplaceAll(ciphertext, "\n", "\n  ") + "\n"

	res, err := Render([]byte(src), Options{File: "p.sls", GPG: GPGOptions{Binary: stand}})
	if err != nil {
		t.Fatalf("rendering through the stand-in: %v", err)
	}

	// `cat` echoes its input, so the value proves the ciphertext arrived
	// on standard input.
	m := res.Value.(*value.Map)
	got, _ := m.Get("k")
	if s, _ := got.(string); !strings.Contains(s, "ZGVmaW5pdGVseS1zZWNyZXQ=") {
		t.Errorf("the ciphertext did not arrive on standard input; got %#v", got)
	}

	recorded, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, arg := range strings.Split(strings.TrimSpace(string(recorded)), "\n") {
		if strings.Contains(arg, "PGP") || strings.Contains(arg, "ZGVmaW5pdGVseS1zZWNyZXQ=") {
			t.Errorf("an argument carries the ciphertext: %q", arg)
		}
	}
	if !strings.Contains(string(recorded), "--decrypt") {
		t.Errorf("the vector should ask for a decryption: %q", recorded)
	}
}

// TestDecryptedValuesAreOfferedToTheRedactor is the seam SPEC 26.1
// depends on: the renderer is the only place that knows a value was
// encrypted, and once it hands the plaintext on, nothing downstream can
// tell it from any other string.
func TestDecryptedValuesAreOfferedToTheRedactor(t *testing.T) {
	home, recipient := gpgKeyring(t)
	secret := gpgEncrypt(t, home, recipient, "s3cret-from-the-pillar")
	indented := strings.ReplaceAll(strings.TrimRight(secret, "\n"), "\n", "\n    ")

	var offered []string
	res, err := Render([]byte("#!yaml|gpg\nk: |\n    "+indented+"\n"), Options{
		File:     "p.sls",
		GPG:      GPGOptions{Home: home},
		OnSecret: func(v string) { offered = append(offered, v) },
	})
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if len(offered) != 1 || offered[0] != "s3cret-from-the-pillar" {
		t.Errorf("the renderer offered %#v; want the one plaintext it produced", offered)
	}

	// And it is still delivered, because redacting is the log's job and
	// not the renderer's.
	m := res.Value.(*value.Map)
	if got, _ := m.Get("k"); got != "s3cret-from-the-pillar" {
		t.Errorf("the value should still reach the tree: %#v", got)
	}

	// A tree with nothing encrypted offers nothing.
	offered = nil
	if _, err := Render([]byte("#!yaml|gpg\nk: plain\n"), Options{
		File: "p.sls", GPG: GPGOptions{Home: home},
		OnSecret: func(v string) { offered = append(offered, v) },
	}); err != nil {
		t.Fatal(err)
	}
	if len(offered) != 0 {
		t.Errorf("a plain value was offered as a secret: %#v", offered)
	}
}

// buildStandIn compiles the stand-in gpg and returns its path.
//
// Named with the platform's own executable suffix: Windows decides what
// a file is by its extension, and `go build -o` writes exactly the name
// it is given.
func buildStandIn(t *testing.T, dir string) string {
	t.Helper()
	name := "gpg-stand-in"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	build := exec.Command("go", "build", "-o", path, "./testdata/gpgstandin")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building the stand-in gpg: %v", err)
	}
	return path
}
