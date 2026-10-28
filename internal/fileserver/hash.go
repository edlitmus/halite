package fileserver

import (
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

// newHash is the one list of digests the file server publishes and a
// node verifies. The algorithm set is the one SPEC section 13.5 permits
// for content comparison; MD5 and SHA-1 are not here, because they exist
// only to verify an upstream source_hash and that path lives in the file
// module.
//
// It was two lists, the hub's in hashOf and the node's in digestOf, and
// both left out sha3-256, which SPEC 13.5 and the `hash_type` reference
// offered. A hub configured with it could hash nothing, served every
// file without a digest, and the node took that as nothing to check.
// DIVERGENCE 5.469.
func newHash(algorithm string) (hash.Hash, error) {
	switch strings.ToLower(algorithm) {
	case "", "sha256":
		return sha256.New(), nil
	case "sha384":
		return sha512.New384(), nil
	case "sha512":
		return sha512.New(), nil
	case "sha3-256":
		return sha3.New256(), nil
	}
	return nil, fmt.Errorf("%q is not a hash the file server offers: sha256, sha384, sha512 or sha3-256", algorithm)
}

// CheckHashType says whether a configured `hash_type` is one the file
// server can publish, so a hub refuses it at start rather than refusing
// every file it is asked for.
func CheckHashType(algorithm string) error {
	_, err := newHash(algorithm)
	return err
}

// hashOf digests a served file.
func hashOf(path, algorithm string) (string, error) {
	h, err := newHash(algorithm)
	if err != nil {
		return "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
