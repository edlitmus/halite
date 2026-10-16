package extension

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MerkleRoot is the root of a bundle's file tree: the file half of
// what the signature covers. Manifest.SigningRoot combines it with the
// manifest's other fields, and that is what is signed and pinned.
//
// A tree rather than a hash of the concatenation, because SPEC 24.4
// says so and because the property is worth having: two bundles that
// share files share subtrees, and a diff of what changed between two
// versions is computable without either signature being involved.
//
// Leaves are `SHA-256(path + "\x00" + file digest)`, sorted by path.
// The path is inside the leaf so that renaming a file changes the root
// even when its contents did not — otherwise a bundle could swap which
// file is the executable without changing what it is signed as.
func MerkleRoot(files map[string]string) ([]byte, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("a bundle with no files has no root")
	}
	paths := make([]string, 0, len(files))
	for rel := range files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)

	level := make([][]byte, 0, len(paths))
	for _, rel := range paths {
		digest, err := hex.DecodeString(files[rel])
		if err != nil {
			return nil, fmt.Errorf("%s has a digest that is not hexadecimal", rel)
		}
		leaf := sha256.New()
		leaf.Write([]byte(rel))
		leaf.Write([]byte{0})
		leaf.Write(digest)
		level = append(level, leaf.Sum(nil))
	}

	for len(level) > 1 {
		var next [][]byte
		for i := 0; i < len(level); i += 2 {
			if i+1 == len(level) {
				// An odd node is carried up rather than paired with
				// itself, which is the duplication that makes some
				// Merkle constructions forgeable.
				next = append(next, level[i])
				continue
			}
			node := sha256.New()
			node.Write(level[i])
			node.Write(level[i+1])
			next = append(next, node.Sum(nil))
		}
		level = next
	}
	return level[0], nil
}

// TrustKey is a public key a node will accept an extension from.
type TrustKey struct {
	// Name is how an operator refers to it.
	Name string
	// Key is the Ed25519 public key.
	Key ed25519.PublicKey
}

// ParseTrustKey reads `<name> <base64 public key>`, the form
// `extension_trust_keys` takes.
//
// Ed25519 because it is in the standard library, has one key size, and
// has no parameters to get wrong — there is no curve to choose, no
// padding mode, and no way to configure it into being weak.
func ParseTrustKey(line string) (TrustKey, error) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) != 2 {
		return TrustKey{}, fmt.Errorf("a trust key is `<name> <base64 key>`, not %q", line)
	}
	raw, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return TrustKey{}, fmt.Errorf("the key for %s is not base64: %w", fields[0], err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return TrustKey{}, fmt.Errorf("the key for %s is %d bytes and an Ed25519 public key is %d",
			fields[0], len(raw), ed25519.PublicKeySize)
	}
	return TrustKey{Name: fields[0], Key: ed25519.PublicKey(raw)}, nil
}

// FormatTrustKey renders one for the configuration.
func FormatTrustKey(name string, key ed25519.PublicKey) string {
	return name + " " + base64.StdEncoding.EncodeToString(key)
}

// Sign produces the detached signature over a bundle's signing root
// (Manifest.SigningRoot).
func Sign(key ed25519.PrivateKey, root []byte) []byte {
	return ed25519.Sign(key, signedMessage(root))
}

// Verify checks a signature against every trusted key and answers with
// the one that verified.
//
// Every key is tried rather than the signature naming which to use: a
// signature that says which key signed it is a signature that can ask
// to be checked against a key the attacker chose. The cost is a few
// microseconds per key.
func Verify(keys []TrustKey, root, signature []byte) (TrustKey, error) {
	if len(keys) == 0 {
		return TrustKey{}, fmt.Errorf("this node trusts no extension keys; set extension_trust_keys")
	}
	message := signedMessage(root)
	for _, key := range keys {
		if ed25519.Verify(key.Key, message, signature) {
			return key, nil
		}
	}
	return TrustKey{}, fmt.Errorf("the signature does not verify against any of this node's %d trusted keys",
		len(keys))
}

// signedMessage is what is actually signed: a domain separator and the
// root.
//
// The separator exists so that a signature over an extension bundle can
// never be replayed as a signature over anything else this project
// signs — a job, a state tree, a token. That costs one string and
// removes a whole class of confusion.
//
// v2 since DIVERGENCE 5.333, when the root began to cover the manifest's
// fields. The domain is not what stops a v1 signature verifying: the
// signing root is a different hash from the file root a v1 signature
// covers, so one cannot verify as the other short of a SHA-256 collision,
// and the tests found nothing that depended on the domain changing. It
// changed so that the bytes signed say which format they are, for anybody
// writing a signer of their own. TestTheSigningFormatIsPinned holds the
// domain, the encoding and the root construction to one known answer.
func signedMessage(root []byte) []byte {
	return append([]byte(bundleDomain), root...)
}

const (
	bundleDomain   = "halite-extension-bundle-v2\x00"
	bundleDomainV1 = "halite-extension-bundle-v1\x00"
)

// signedAsV1 reports whether a signature is a valid v1 signature over
// the bundle's file root, by any trusted key.
//
// Used only to explain a refusal. A v1 signature is never accepted: it
// covers the files and nothing the manifest says about them, so honouring
// one would honour whatever `declares` and `executables` now say.
func signedAsV1(keys []TrustKey, filesRoot, signature []byte) (TrustKey, bool) {
	message := append([]byte(bundleDomainV1), filesRoot...)
	for _, key := range keys {
		if ed25519.Verify(key.Key, message, signature) {
			return key, true
		}
	}
	return TrustKey{}, false
}

// DigestFile is the SHA-256 of a file, hexadecimal.
func DigestFile(path string) (string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// EqualDigest compares two hex digests without a timing signal.
//
// A digest is public, so this is not strictly required — but a
// comparison that leaks where two digests first differ is a habit worth
// not having in code that decides whether to execute something.
func EqualDigest(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(a)), []byte(strings.ToLower(b))) == 1
}
