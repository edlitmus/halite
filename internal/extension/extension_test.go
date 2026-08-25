package extension

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// aBundle writes a signed bundle to a fresh directory and answers with
// the directory, the trust key, and the manifest.
func aBundle(t *testing.T, adjust func(*Manifest)) (string, TrustKey, *Manifest) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "echo"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("an extension\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := Build(dir, Manifest{
		Name: "echo", Version: "1.0.0", Kind: "module",
		Executables: map[string]string{"freebsd/amd64": "echo", "linux/amd64": "echo"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if adjust != nil {
		adjust(manifest)
	}
	writeManifest(t, dir, manifest)

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root, err := MerkleRoot(manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SignatureName), Sign(private, root), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, TrustKey{Name: "release", Key: public}, manifest
}

func writeManifest(t *testing.T, dir string, m *Manifest) {
	t.Helper()
	raw, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadOpts(key TrustKey) LoadOptions {
	return LoadOptions{
		TrustKeys: []TrustKey{key}, RequireSignature: true,
		Platform: "freebsd/amd64",
	}
}

func TestASignedBundleLoads(t *testing.T) {
	dir, key, _ := aBundle(t, nil)

	bundle, err := Load(dir, loadOpts(key))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.SignedBy != "release" {
		t.Errorf("it was verified by %q", bundle.SignedBy)
	}
	if bundle.Unsigned {
		t.Error("a signed bundle is reported as unsigned")
	}
	exe, err := bundle.Executable("freebsd/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(exe) != "echo" {
		t.Errorf("the executable is %s", exe)
	}
}

// Code delivered to a node must be signed by a key the node trusts.
func TestABundleSignedByAnotherKeyIsRefused(t *testing.T) {
	dir, _, _ := aBundle(t, nil)
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Load(dir, loadOpts(TrustKey{Name: "someone-else", Key: other}))
	if err == nil {
		t.Fatal("a bundle signed by an untrusted key loaded")
	}
	if !strings.Contains(err.Error(), "does not verify") {
		t.Errorf("the refusal is %v", err)
	}
}

// The cache is a directory on a managed node. A node whose cache can be
// written to is one where a tampered executable would otherwise run on
// the next highstate.
func TestATamperedFileIsRefused(t *testing.T) {
	dir, key, _ := aBundle(t, nil)
	if err := os.WriteFile(filepath.Join(dir, "echo"), []byte("#!/bin/sh\nrm -rf /\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir, loadOpts(key))
	if err == nil {
		t.Fatal("a tampered executable loaded")
	}
	if !strings.Contains(err.Error(), "does not match the manifest") {
		t.Errorf("the refusal is %v", err)
	}
}

// A file that is present and unlisted is outside the Merkle root, which
// is to say one nobody signed — and the bundle directory is somewhere
// an extension can load from.
func TestAnUnlistedFileIsRefused(t *testing.T) {
	dir, key, _ := aBundle(t, nil)
	if err := os.WriteFile(filepath.Join(dir, "extra.so"), []byte("smuggled"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(dir, loadOpts(key))
	if err == nil {
		t.Fatal("a bundle with an unsigned extra file loaded")
	}
	if !strings.Contains(err.Error(), "nobody signed") {
		t.Errorf("the refusal is %v", err)
	}
}

// The version is a label the publisher controls; the digest is not.
func TestThePinHoldsBothVersionAndDigest(t *testing.T) {
	dir, key, manifest := aBundle(t, nil)
	root, err := MerkleRoot(manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	rootHex := hex.EncodeToString(root)

	opts := loadOpts(key)
	opts.Pin = Pin{Version: "1.0.0", Root: rootHex}
	if _, err := Load(dir, opts); err != nil {
		t.Fatalf("a correctly pinned bundle was refused: %v", err)
	}

	opts.Pin = Pin{Version: "2.0.0"}
	if _, err := Load(dir, opts); err == nil {
		t.Error("a bundle at the wrong version loaded")
	}

	// The version matches and the contents do not: the swap the digest
	// half exists to catch.
	opts.Pin = Pin{Version: "1.0.0", Root: strings.Repeat("ab", 32)}
	_, err = Load(dir, opts)
	if err == nil {
		t.Fatal("a bundle with the pinned version and different contents loaded")
	}
	if !strings.Contains(err.Error(), "pinned to") {
		t.Errorf("the refusal is %v", err)
	}
}

// `extension_require_signature: false` permits an *absent* signature
// for development. A wrong one is tampering or an unfinished key
// rotation, and is refused whatever the setting says.
func TestAnAbsentSignatureIsPermittedAndAWrongOneIsNot(t *testing.T) {
	dir, key, _ := aBundle(t, nil)
	if err := os.Remove(filepath.Join(dir, SignatureName)); err != nil {
		t.Fatal(err)
	}

	opts := loadOpts(key)
	if _, err := Load(dir, opts); err == nil {
		t.Error("an unsigned bundle loaded on a node that requires signatures")
	}

	opts.RequireSignature = false
	bundle, err := Load(dir, opts)
	if err != nil {
		t.Fatalf("an unsigned bundle was refused in development mode: %v", err)
	}
	if !bundle.Unsigned {
		t.Error("an unsigned bundle is not reported as unsigned")
	}

	// A signature that is present and wrong.
	if err := os.WriteFile(filepath.Join(dir, SignatureName),
		make([]byte, ed25519.SignatureSize), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, opts); err == nil {
		t.Error("a bundle with an invalid signature loaded with require_signature off")
	}
}

func TestANodeWithNoTrustKeysRefusesEverything(t *testing.T) {
	dir, _, _ := aBundle(t, nil)
	_, err := Load(dir, LoadOptions{RequireSignature: true, Platform: "freebsd/amd64"})
	if err == nil {
		t.Fatal("a bundle loaded on a node that trusts no keys")
	}
	if !strings.Contains(err.Error(), "extension_trust_keys") {
		t.Errorf("the refusal does not say what to set: %v", err)
	}
}

// A node that finds no executable for itself refuses rather than
// running something built for another machine.
func TestABundleForAnotherPlatformIsRefused(t *testing.T) {
	dir, key, _ := aBundle(t, nil)
	opts := loadOpts(key)
	opts.Platform = "windows/arm64"

	_, err := Load(dir, opts)
	if err == nil {
		t.Fatal("a bundle with no executable for this platform loaded")
	}
	if !strings.Contains(err.Error(), "windows/arm64") {
		t.Errorf("the refusal is %v", err)
	}
}

// Renaming a file must change the root even when its contents did not,
// or a bundle could swap which file is the executable without changing
// what it is signed as.
func TestTheRootCoversPathsAsWellAsContents(t *testing.T) {
	same := strings.Repeat("00", 32)
	a, err := MerkleRoot(map[string]string{"one": same})
	if err != nil {
		t.Fatal(err)
	}
	b, err := MerkleRoot(map[string]string{"two": same})
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(a) == hex.EncodeToString(b) {
		t.Error("two bundles with the same contents under different names share a root")
	}
}

// The order files are listed in must not change the root.
func TestTheRootIsStable(t *testing.T) {
	files := map[string]string{
		"a": strings.Repeat("11", 32),
		"b": strings.Repeat("22", 32),
		"c": strings.Repeat("33", 32),
	}
	first, err := MerkleRoot(files)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		again, err := MerkleRoot(files)
		if err != nil {
			t.Fatal(err)
		}
		if hex.EncodeToString(first) != hex.EncodeToString(again) {
			t.Fatal("the root is not stable across calls")
		}
	}
}

// A signature over an extension bundle must never be replayable as a
// signature over anything else this project signs.
func TestASignatureIsDomainSeparated(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := make([]byte, 32)
	signature := Sign(private, root)

	// The same bytes, signed without the separator, must not verify as
	// a bundle signature.
	bare := ed25519.Sign(private, root)
	if _, err := Verify([]TrustKey{{Name: "k", Key: public}}, root, bare); err == nil {
		t.Error("a signature over the bare root verified as a bundle signature")
	}
	if _, err := Verify([]TrustKey{{Name: "k", Key: public}}, root, signature); err != nil {
		t.Errorf("the real signature did not verify: %v", err)
	}
}

// A manifest naming a path outside the bundle is one to refuse rather
// than to unpack and find out about.
func TestAManifestIsCheckedBeforeAnythingIsTrusted(t *testing.T) {
	cases := []struct {
		name   string
		adjust func(*Manifest)
		want   string
	}{
		{"no name", func(m *Manifest) { m.Name = "" }, "names no extension"},
		{"a name with a slash", func(m *Manifest) { m.Name = "../evil" }, "usable extension name"},
		{"no version", func(m *Manifest) { m.Version = "" }, "names no version"},
		{"an unknown kind", func(m *Manifest) { m.Kind = "wheel" }, "not an extension kind"},
		{"an absolute path", func(m *Manifest) {
			m.Files["/etc/passwd"] = strings.Repeat("00", 32)
		}, "absolute"},
		{"a traversing path", func(m *Manifest) {
			m.Files["../../etc/passwd"] = strings.Repeat("00", 32)
		}, "leaves the bundle"},
		{"an unsigned executable", func(m *Manifest) {
			m.Executables["freebsd/amd64"] = "not-in-the-list"
		}, "not signed"},
		{"an undeclarable need", func(m *Manifest) { m.Declares = []string{"kernel"} }, "does not understand"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, _, _ := aBundle(t, nil)
			raw, err := os.ReadFile(filepath.Join(base, ManifestName))
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := ParseManifest(raw)
			if err != nil {
				t.Fatal(err)
			}
			tc.adjust(manifest)
			if err := manifest.Check(); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			} else if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal is %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestATrustKeyRoundTrips(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	line := FormatTrustKey("release", public)
	key, err := ParseTrustKey(line)
	if err != nil {
		t.Fatal(err)
	}
	if key.Name != "release" || !key.Key.Equal(public) {
		t.Errorf("the key came back as %+v", key)
	}
	for _, bad := range []string{"", "release", "release not-base64!!", "release " + line} {
		if _, err := ParseTrustKey(bad); err == nil {
			t.Errorf("%q was accepted as a trust key", bad)
		}
	}
}
