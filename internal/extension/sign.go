package extension

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/edlitmus/halite/internal/fileperm"
)

// Signing a bundle, for whoever publishes an extension.
//
// It was only `go run ./tools/extbundle`, which needs a checkout of this
// repository and a Go toolchain on the machine that signs -- so signing
// could not be a job an estate ran for itself. `halite-hub extensions
// sign` and extbundle are both this, so they cannot sign differently.
// DIVERGENCE 5.260.

// SignOptions is one bundle to sign.
type SignOptions struct {
	// Dir holds the executable; the manifest and the signature are
	// written beside it.
	Dir, Name, Version, Kind string
	// Exe is the executable, relative to Dir.
	Exe string
	// Platform is `goos/goarch`, checked against the executable rather
	// than believed; empty reads it from the executable.
	Platform string
	Declares []string
	Key      ed25519.PrivateKey
}

// SignBundle writes the manifest of everything in Dir and its signature,
// and answers with the Merkle root a node pins.
//
// The platform is read before anything is written, so a bundle that is
// going to be refused leaves nothing behind.
func SignBundle(o SignOptions) ([]byte, error) {
	if o.Dir == "" || o.Name == "" || o.Exe == "" {
		return nil, fmt.Errorf("a bundle needs a directory, a name and an executable")
	}
	if len(o.Key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("the signing key is not an Ed25519 private key")
	}
	if o.Version == "" {
		o.Version = "1.0.0"
	}
	if o.Kind == "" {
		o.Kind = "module"
	}
	target, err := ExecutablePlatform(filepath.Join(o.Dir, o.Exe), o.Platform)
	if err != nil {
		return nil, err
	}
	manifest, err := Build(o.Dir, Manifest{
		Name: o.Name, Version: o.Version, Kind: o.Kind,
		Executables: map[string]string{target: o.Exe},
		Declares:    o.Declares,
	})
	if err != nil {
		return nil, err
	}
	raw, err := manifest.Encode()
	if err != nil {
		return nil, err
	}
	if err := fileperm.WriteFile(filepath.Join(o.Dir, ManifestName), raw, 0o644); err != nil {
		return nil, err
	}
	root, err := MerkleRoot(manifest.Files)
	if err != nil {
		return nil, err
	}
	if err := fileperm.WriteFile(filepath.Join(o.Dir, SignatureName), Sign(o.Key, root), 0o644); err != nil {
		return nil, err
	}
	return root, nil
}

// LoadSigningKey reads a key CreateSigningKey wrote: the 64-byte Ed25519
// private key in base64.
func LoadSigningKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("%s is not a signing key: %w", path, err)
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s is not an Ed25519 signing key: %d bytes", path, len(decoded))
	}
	return ed25519.PrivateKey(decoded), nil
}

// CreateSigningKey makes a signing key at path, readable by its owner
// alone, and refuses to replace one: every extension signed with the old
// key would stop verifying the moment a node trusted only the new one.
func CreateSigningKey(path string) (ed25519.PublicKey, error) {
	if _, err := os.Stat(path); err == nil {
		return nil, fmt.Errorf("%s already exists; a signing key is never replaced", path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := fileperm.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(private)), 0o600); err != nil {
		return nil, err
	}
	return public, nil
}

// SplitDeclares reads `network,root`, on a comma.
//
// Not filepath.SplitList, which splits on the operating system's path
// list separator: `network,root` was one declaration named
// "network,root" everywhere, and a declaration that does not parse is a
// permission the sandbox never grants.
func SplitDeclares(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
