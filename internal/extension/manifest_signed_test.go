package extension

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Everything a manifest says is covered by the signature, not only the
// files it lists.
//
// The signature used to be over the Merkle root of `files` alone, so
// `declares`, `executables`, `name`, `version`, `kind` and `description`
// were outside anything a key signed. Anyone who could write
// `manifest.json` -- the hub's file server, a gitfs tree, a node's cache
// -- could give a signed extension `root` and `network` that its
// publisher never declared, point it at a different signed file, or
// relabel it as another extension or an older release, and the bundle
// still loaded as signed. Issue 321 finding H-02; DIVERGENCE 5.331.
func TestEveryManifestFieldIsCoveredByTheSignature(t *testing.T) {
	cases := []struct {
		name   string
		tamper func(*Manifest)
	}{
		{"declares gains root and network", func(m *Manifest) { m.Declares = []string{"root", "network"} }},
		{"declares gains network", func(m *Manifest) { m.Declares = []string{"network"} }},
		{"executables repointed at another signed file", func(m *Manifest) {
			m.Executables = map[string]string{"freebsd/amd64": "README", "linux/amd64": "README"}
		}},
		{"executables gains a platform", func(m *Manifest) {
			m.Executables = map[string]string{"freebsd/amd64": "echo", "linux/amd64": "echo", "darwin/arm64": "echo"}
		}},
		{"version relabelled", func(m *Manifest) { m.Version = "9.9.9" }},
		{"name relabelled", func(m *Manifest) { m.Name = "other" }},
		{"kind changed", func(m *Manifest) { m.Kind = "returner" }},
		{"description changed", func(m *Manifest) { m.Description = "harmless, honestly" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, key, manifest := aBundle(t, nil)
			c.tamper(manifest)
			writeManifest(t, dir, manifest)
			if b, err := Load(dir, loadOpts(key)); err == nil {
				t.Fatalf("a signed bundle whose manifest was rewritten (%s) loaded as signed by %s, "+
					"with declares %v and executables %v",
					c.name, b.SignedBy, b.Manifest.Declares, b.Manifest.Executables)
			}
		})
	}
}

// A field this build does not know is refused rather than ignored.
//
// The metadata digest is computed from the fields this build parses. A
// field it does not parse is therefore outside the signature, and a
// later build that gave the field a meaning would act on something
// nobody signed. So an unknown field is not tolerated.
func TestAManifestWithAFieldThisBuildDoesNotKnowIsRefused(t *testing.T) {
	dir, key, _ := aBundle(t, nil)
	path := filepath.Join(dir, ManifestName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(raw, []byte("{\n"), []byte("{\n  \"privileged\": true,\n"), 1)
	if bytes.Equal(tampered, raw) {
		t.Fatal("the test did not change the manifest")
	}
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(dir, loadOpts(key))
	if err == nil {
		t.Fatal("a manifest with an unknown field loaded")
	}
	if !strings.Contains(err.Error(), "privileged") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}

// The pin covers the declarations as well.
//
// A pin's root is what an operator copies out of `extensions sign` or
// `sys.list_extensions`. If it covered only the files, an estate running
// with `extension_require_signature: false` and a pin -- the arrangement
// the pin exists to make safe -- would load a bundle whose files matched
// and whose declarations had been rewritten.
func TestThePinCoversTheDeclarations(t *testing.T) {
	dir, _, manifest := aBundle(t, nil)
	if err := os.Remove(filepath.Join(dir, SignatureName)); err != nil {
		t.Fatal(err)
	}
	bundle, err := Load(dir, LoadOptions{Platform: "freebsd/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	root, err := bundle.Root()
	if err != nil {
		t.Fatal(err)
	}
	pin := LoadOptions{Platform: "freebsd/amd64", Pin: Pin{Version: manifest.Version, Root: root}}
	if _, err := Load(dir, pin); err != nil {
		t.Fatalf("the untampered, unsigned bundle does not match its own pin: %v", err)
	}

	manifest.Declares = []string{"root", "network"}
	writeManifest(t, dir, manifest)
	if _, err := Load(dir, pin); err == nil {
		t.Fatal("an unsigned bundle whose declarations were rewritten still matched its pin")
	}
}

// Two manifests that differ only in metadata have different roots, and
// the same manifest always has the same one.
func TestTheRootCoversTheMetadata(t *testing.T) {
	_, _, m := aBundle(t, nil)
	base, err := m.SigningRoot()
	if err != nil {
		t.Fatal(err)
	}
	again, err := m.SigningRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base, again) {
		t.Fatal("the root is not stable")
	}
	files, err := MerkleRoot(m.Files)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base, files) {
		t.Fatal("the signing root is the file root, so it covers nothing else")
	}

	declared := *m
	declared.Declares = []string{"network"}
	other, err := declared.SigningRoot()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(base, other) {
		t.Errorf("adding a declaration did not change the root (%s)", hex.EncodeToString(base))
	}

	// Order is not meaning: declarations and executables are sets, so the
	// order a publisher wrote them in must not change what was signed.
	a, b := *m, *m
	a.Declares = []string{"root", "network"}
	b.Declares = []string{"network", "root"}
	ra, err := a.SigningRoot()
	if err != nil {
		t.Fatal(err)
	}
	rb, err := b.SigningRoot()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ra, rb) {
		t.Error("the order declarations were written in changed the root")
	}
}

// A value's boundary is part of what is signed.
//
// An `executables` key and its value are written next to each other with
// nothing between them, so without a length prefix the platform
// `linux/amd64` running `echo` and the platform `linux/amd64e` running
// `cho` would be the same bytes. The first version of this test moved a
// byte between `name` and `version` instead, and passed with the prefixes
// removed, because the field labels between those two already told them
// apart. It tested a boundary that was never at risk.
func TestTheMetadataEncodingIsUnambiguous(t *testing.T) {
	_, _, m := aBundle(t, nil)
	a, b := *m, *m
	a.Executables = map[string]string{"linux/amd64": "echo"}
	b.Executables = map[string]string{"linux/amd64e": "cho"}
	ra, err := a.SigningRoot()
	if err != nil {
		t.Fatal(err)
	}
	rb, err := b.SigningRoot()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(ra, rb) {
		t.Error("moving bytes across a field boundary did not change the root")
	}
}

// A bundle signed in the old format is refused, and says why.
//
// Accepting both would leave the hole open: a v1 signature covers only
// the files, so honouring one honours whatever its manifest now says.
func TestAVersionOneSignatureIsRefusedByName(t *testing.T) {
	dir, _, manifest := aBundle(t, nil)
	files, err := MerkleRoot(manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := TrustKey{Name: "release", Key: public}
	v1 := ed25519.Sign(private, append([]byte("halite-extension-bundle-v1\x00"), files...))
	if err := os.WriteFile(filepath.Join(dir, SignatureName), v1, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(dir, loadOpts(key))
	if err == nil {
		t.Fatal("a v1 signature was accepted")
	}
	for _, want := range []string{"v1", "re-sign"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// A pin that still holds a bundle's v1 root says so, and gives the new
// one, rather than reporting a mismatch the operator cannot explain.
func TestAPinOfTheVersionOneRootSaysSo(t *testing.T) {
	dir, key, manifest := aBundle(t, nil)
	files, err := MerkleRoot(manifest.Files)
	if err != nil {
		t.Fatal(err)
	}
	opts := loadOpts(key)
	opts.Pin = Pin{Version: manifest.Version, Root: hex.EncodeToString(files)}
	_, err = Load(dir, opts)
	if err == nil {
		t.Fatal("a pin of the v1 root matched the v2 bundle")
	}
	want, err2 := manifest.SigningRoot()
	if err2 != nil {
		t.Fatal(err2)
	}
	for _, s := range []string{"v1", hex.EncodeToString(want)} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("the refusal does not say %q: %v", s, err)
		}
	}
}

// Every field of Manifest but Files is in the signed metadata.
//
// Files is covered by the file tree; every other field has to be in
// metadata(), or it is a field a signature does not cover -- which is the
// whole of H-02 again, for whichever field was added last. So this walks
// the struct rather than a list somebody has to remember to extend, and a
// field of a type it does not know how to vary fails it too.
func TestTheMetadataCoversEveryField(t *testing.T) {
	_, _, base := aBundle(t, nil)
	want := base.metadata()
	typ := reflect.TypeOf(*base)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == "Files" {
			continue
		}
		changed := *base
		v := reflect.ValueOf(&changed).Elem().Field(i)
		switch v.Kind() {
		case reflect.String:
			v.SetString(v.String() + "x")
		case reflect.Slice:
			v.Set(reflect.Append(reflect.ValueOf(append([]string(nil), base.Declares...)), reflect.ValueOf("network")))
		case reflect.Map:
			m := reflect.MakeMap(v.Type())
			for _, k := range v.MapKeys() {
				m.SetMapIndex(k, v.MapIndex(k))
			}
			m.SetMapIndex(reflect.ValueOf("plan9/amd64"), reflect.ValueOf("echo"))
			v.Set(m)
		default:
			t.Errorf("Manifest.%s is a %s, which this test cannot vary; teach it to, and put the field in metadata()",
				field.Name, v.Kind())
			continue
		}
		if bytes.Equal(changed.metadata(), want) {
			t.Errorf("changing Manifest.%s does not change the signed metadata, so the signature does not cover it",
				field.Name)
		}
	}
}

// The signing format, held to one known answer.
//
// A signer outside this repository has to produce these bytes, and a
// node built later has to accept what an earlier one signed, so an
// accidental change to the domain, the metadata encoding or the root
// construction should fail here rather than in the field. The expected
// value was computed twice when the format was set: by this
// implementation, and by a separate Python implementation written from
// the formula on SigningRoot and metadata() (uvarint length prefixes,
// sorted sets, the two domains). They agreed, so the comments describe
// what the code does. That is evidence the format is what it says, not
// that it is a good format; the tests above are that.
func TestTheSigningFormatIsPinned(t *testing.T) {
	m := Manifest{
		Name: "echo", Version: "1.0.0", Kind: "module",
		Description: "returns what it was given",
		Executables: map[string]string{"linux/amd64": "echo", "freebsd/amd64": "echo"},
		Declares:    []string{"network"},
		Files: map[string]string{
			"echo":   strings.Repeat("ab", 32),
			"README": strings.Repeat("cd", 32),
		},
	}
	root, err := m.SigningRoot()
	if err != nil {
		t.Fatal(err)
	}
	const want = "a0b7294a8091988d47729fc978fa3b5c40b3a3797e27f97ccc17d73e41b5f64d"
	if got := hex.EncodeToString(root); got != want {
		t.Errorf("the signing root of the reference manifest is %s, want %s", got, want)
	}
	if msg := signedMessage(root); !bytes.HasPrefix(msg, []byte("halite-extension-bundle-v2\x00")) {
		t.Errorf("the signed message begins %q", msg[:len("halite-extension-bundle-v2\x00")])
	}
}
