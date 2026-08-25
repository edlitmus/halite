package extension

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Bundle is an unpacked extension on disk.
type Bundle struct {
	// Dir is where it lives, `<cache>/<name>/<version>`.
	Dir string
	// Manifest is what it says it is.
	Manifest *Manifest
	// SignedBy is the trust key that verified it, or empty when
	// signatures were not required.
	SignedBy string
	// Unsigned records that this bundle was loaded without a valid
	// signature, which `extension_require_signature: false` permits and
	// which is reported everywhere the extension is.
	Unsigned bool
}

// Pin is what an estate fixes an extension at.
//
// SPEC 24.4: pinned by version and digest in configuration or pillar,
// so that a file server change cannot silently swap an extension. Both
// halves matter — a version alone is a label the publisher controls.
type Pin struct {
	Version string
	// Root is the hexadecimal Merkle root the bundle must have.
	Root string
}

// LoadOptions is how a bundle is verified.
type LoadOptions struct {
	// TrustKeys are the keys this node accepts.
	TrustKeys []TrustKey
	// RequireSignature is SPEC 24.4's `extension_require_signature`.
	// False permits an unsigned bundle and is for development; the
	// caller logs a warning on every load, which is what the
	// specification asks for.
	RequireSignature bool
	// Pin fixes the version and the Merkle root. Zero pins nothing,
	// which an estate should not do and which this package permits so
	// that the pin can be introduced after the extension works.
	Pin Pin
	// Platform is `<goos>/<goarch>`. Empty takes this build's.
	Platform string
}

// Load verifies a bundle directory and answers with what it is.
//
// Verified on every load, which SPEC 24.4 requires and which is the
// difference between a bundle that was correct when it was fetched and
// one that is correct now. The cache is a directory on a managed node,
// and a node whose cache can be written to is a node where an unsigned
// executable would otherwise run on the next highstate.
func Load(dir string, opts LoadOptions) (*Bundle, error) {
	manifestPath := filepath.Join(dir, ManifestName)
	raw, err := os.ReadFile(filepath.Clean(manifestPath))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}
	manifest, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}

	// The files on disk are what is checked, not the manifest's list of
	// them: a manifest that agrees with itself proves nothing.
	if err := verifyFiles(dir, manifest); err != nil {
		return nil, err
	}

	root, err := MerkleRoot(manifest.Files)
	if err != nil {
		return nil, err
	}
	rootHex := hex.EncodeToString(root)

	bundle := &Bundle{Dir: dir, Manifest: manifest}

	signature, sigErr := os.ReadFile(filepath.Clean(filepath.Join(dir, SignatureName)))
	switch {
	case sigErr == nil:
		key, err := Verify(opts.TrustKeys, root, signature)
		if err != nil {
			// A bundle with a signature that does not verify is refused
			// whatever `require_signature` says. The setting permits an
			// *absent* signature for development; a wrong one is
			// tampering or a key rotation nobody finished.
			return nil, fmt.Errorf("%s %s: %w", manifest.Name, manifest.Version, err)
		}
		bundle.SignedBy = key.Name
	case opts.RequireSignature:
		return nil, fmt.Errorf("%s %s carries no signature, and this node requires one",
			manifest.Name, manifest.Version)
	default:
		bundle.Unsigned = true
	}

	if err := checkPin(manifest, rootHex, opts.Pin); err != nil {
		return nil, err
	}

	platform := opts.Platform
	if platform == "" {
		platform = Platform(runtime.GOOS, runtime.GOARCH)
	}
	if _, err := manifest.ExecutableFor(platform); err != nil {
		return nil, err
	}
	return bundle, nil
}

// checkPin holds the bundle to what the estate fixed it at.
func checkPin(manifest *Manifest, rootHex string, pin Pin) error {
	if pin.Version != "" && pin.Version != manifest.Version {
		return fmt.Errorf("%s is pinned to %s and this bundle is %s",
			manifest.Name, pin.Version, manifest.Version)
	}
	if pin.Root != "" && !EqualDigest(pin.Root, rootHex) {
		// The version matched and the contents did not, which is
		// exactly the swap the digest half of the pin exists to catch.
		return fmt.Errorf("%s %s has root %s and is pinned to %s",
			manifest.Name, manifest.Version, rootHex, pin.Root)
	}
	return nil
}

// verifyFiles digests everything the manifest lists and refuses
// anything it does not.
//
// Both directions. A file whose digest is wrong is tampering; a file
// that is present and unlisted is a file outside the Merkle root, which
// is to say one nobody signed — and an extension's working directory is
// somewhere it can load from.
func verifyFiles(dir string, manifest *Manifest) error {
	for rel, want := range manifest.Files {
		got, err := DigestFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("%s %s: %s: %w", manifest.Name, manifest.Version, rel, err)
		}
		if !EqualDigest(got, want) {
			return fmt.Errorf("%s %s: %s does not match the manifest", manifest.Name, manifest.Version, rel)
		}
	}

	listed := map[string]bool{ManifestName: true, SignatureName: true}
	for rel := range manifest.Files {
		listed[rel] = true
	}
	var extra []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if !listed[filepath.ToSlash(rel)] {
			extra = append(extra, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return fmt.Errorf("%s %s holds %s, which the manifest does not list and nobody signed",
			manifest.Name, manifest.Version, strings.Join(extra, ", "))
	}
	return nil
}

// Executable is the path to run for this platform.
func (b *Bundle) Executable(platform string) (string, error) {
	if platform == "" {
		platform = Platform(runtime.GOOS, runtime.GOARCH)
	}
	rel, err := b.Manifest.ExecutableFor(platform)
	if err != nil {
		return "", err
	}
	return filepath.Join(b.Dir, filepath.FromSlash(rel)), nil
}

// Root is the bundle's Merkle root, hexadecimal — what an operator
// copies into a pin.
func (b *Bundle) Root() (string, error) {
	root, err := MerkleRoot(b.Manifest.Files)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(root), nil
}

// Build makes a manifest for a directory, digesting everything in it.
//
// For whoever publishes an extension, and for the tests. The manifest
// and the signature are excluded from the file list: the first cannot
// contain its own digest, and the second is over the root the first
// describes.
func Build(dir string, manifest Manifest) (*Manifest, error) {
	files := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(rel)
		if slashed == ManifestName || slashed == SignatureName {
			return nil
		}
		digest, err := DigestFile(path)
		if err != nil {
			return err
		}
		files[slashed] = digest
		return nil
	})
	if err != nil {
		return nil, err
	}
	manifest.Files = files
	if err := manifest.Check(); err != nil {
		return nil, err
	}
	return &manifest, nil
}
