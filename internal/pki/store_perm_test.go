package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/edlitmus/halite/internal/fileperm"
)

// The key store's restrictions, asserted rather than read out of the source.
//
// # What these can and cannot see, by platform
//
// This is worth stating before the tests, because on unix two of the three
// would pass against the code as it was before the fix — and a test that
// passes before and after is not testing the fix.
//
//   - `MkdirAll(dir, 0o700)` already produces a 0700 directory on unix, and
//     `OpenFile(path, …, 0o600)` already produces a 0600 file. So the
//     directory-creation and key-writing cases below are witnessed on unix by
//     nothing: they are there because on **Windows** neither call is an access
//     control decision at all, and `test (windows-2022)` runs this package's
//     unit suite on every push. That leg is the witness.
//   - `TestEnsureRestrictsADirectorySomebodyElseMade` is the one that bites
//     here. A directory that already exists keeps the mode it has —
//     `MkdirAll` returns nil and changes nothing — so a 0777 directory stayed
//     0777 on every platform. Removing `fileperm.Apply` from `Ensure` fails it
//     on unix and on Windows.
//
// `fileperm.Others` is the question in both places: it names any account that
// can reach the path beyond its owner, SYSTEM and Administrators, and on unix
// it answers from the mode. One question, two implementations, so a test does
// not have to know which platform it is on.
//
// **Not covered, and stated rather than implied**: `WriteKey` applies the
// restriction to the open file *before* writing the bytes, so the key is never
// on disk reachable by another account. Nothing here observes that ordering —
// it would take a second process racing the write — so it is a claim made by
// reading the function and not one this suite holds.
//
// plan.md 19e, DIVERGENCE 5.144.

func testKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// noOthers fails naming who can reach the path, because "the restriction is
// wrong" is not an answer anybody can act on and "Everyone can read it" is.
func noOthers(t *testing.T, path, what string) {
	t.Helper()
	others, err := fileperm.Others(path)
	if err != nil {
		t.Fatalf("who can reach %s could not be read: %v", path, err)
	}
	if len(others) > 0 {
		t.Errorf("%s (%s) can be reached by %v; it holds key material and should be "+
			"reachable by its owner alone. %s", what, path, others, fileperm.Advice(path))
	}
}

func TestEnsureRestrictsTheKeyDirectory(t *testing.T) {
	f := Files{Dir: filepath.Join(t.TempDir(), "pki")}
	if err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	noOthers(t, f.Dir, "the key directory")
}

// **The case that is observable on every platform.** `MkdirAll` on a directory
// that exists returns nil and changes nothing, so an existing one keeps
// whatever mode it has — which is the case that matters in practice, because
// it is the directory nobody will go back and check: one made by hand, or left
// by an older build that never restricted it.
func TestEnsureRestrictsADirectorySomebodyElseMade(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	// On unix the umask may have taken bits out of 0777 already, so the
	// starting state is asserted rather than assumed: a test whose setup did
	// not produce the condition it tests for passes for the wrong reason.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		before, err := fileperm.Others(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(before) == 0 {
			t.Fatalf("the setup produced a directory nobody else can reach, so this "+
				"test cannot see what it is for: %s", dir)
		}
	}

	f := Files{Dir: dir}
	if err := f.Ensure(); err != nil {
		t.Fatal(err)
	}
	noOthers(t, dir, "a key directory that already existed")
}

func TestWriteKeyRestrictsTheKey(t *testing.T) {
	f := Files{Dir: filepath.Join(t.TempDir(), "pki")}
	if err := f.WriteKey(CAKeyFile, testKey(t)); err != nil {
		t.Fatal(err)
	}
	noOthers(t, f.Path(CAKeyFile), "the CA private key")
	// And the directory it landed in, because a private key inside a
	// world-readable directory is a private key anybody can copy: WriteKey
	// calls Ensure, so this is the assertion that it is the restricting
	// Ensure and not a bare MkdirAll.
	noOthers(t, f.Dir, "the directory the CA key is in")
}
