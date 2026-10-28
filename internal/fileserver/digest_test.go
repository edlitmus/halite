package fileserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// cannedFetch answers every file request with one body and one
// published digest, the two things a hub sends.
type cannedFetch struct {
	body   []byte
	digest string
}

func (f cannedFetch) FileManifest(context.Context, string, string) ([]byte, error) {
	return []byte(`{"files":[]}`), nil
}

func (f cannedFetch) FetchFile(context.Context, string, string, string) ([]byte, string, bool, error) {
	return f.body, f.digest, false, nil
}

// A body is cached only against a digest the hub published for it. A
// missing digest used to mean "do not check": the body was written into
// the cache and served to states as the file. The hub left the header
// out whenever it could not hash the file, and it could not hash any
// file when `hash_type` was `sha3-256`, which SPEC 13.5 and the
// configuration reference both offered. A digest with no algorithm was
// skipped the same way. DIVERGENCE 5.469.
func TestABodyIsCachedOnlyAgainstAPublishedDigest(t *testing.T) {
	body := []byte("worker_processes 4;\n")
	sum := sha256.Sum256(body)
	good := "sha256:" + hex.EncodeToString(sum[:])

	for _, tc := range []struct {
		name, digest, refusal string
	}{
		{"none", "", "published no digest"},
		{"no algorithm", hex.EncodeToString(sum[:]), "not a digest"},
		{"an algorithm this build does not verify", "md5:" + hex.EncodeToString(sum[:16]), "not a hash"},
		{"the wrong digest", "sha256:" + strings.Repeat("0", 64), "arrived with digest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRemote(cannedFetch{body, tc.digest}, t.TempDir(), []string{"base"})
			if _, err := r.cacheFile("base", "web/nginx.conf"); err == nil {
				t.Fatalf("a body published as %q was cached", tc.digest)
			} else if !strings.Contains(err.Error(), tc.refusal) {
				t.Errorf("the refusal does not say why: %v", err)
			}
		})
	}

	r := NewRemote(cannedFetch{body, good}, t.TempDir(), []string{"base"})
	if _, err := r.cacheFile("base", "web/nginx.conf"); err != nil {
		t.Fatalf("a body with its own digest was refused: %v", err)
	}
}

// The hub's digest and the node's check are one list of algorithms,
// because they were two and disagreed with the documentation: SPEC
// 13.5 offers sha3-256, and neither side knew it. Every algorithm the
// hub will publish must be one the node verifies, and must give the
// digest the standard library gives. DIVERGENCE 5.469.
func TestEveryPublishedHashIsOneTheNodeVerifies(t *testing.T) {
	for _, algorithm := range []string{"sha256", "sha384", "sha512", "sha3-256"} {
		if err := CheckHashType(algorithm); err != nil {
			t.Errorf("SPEC 13.5 offers %s: %v", algorithm, err)
			continue
		}
		if _, err := digestOf([]byte("x"), algorithm); err != nil {
			t.Errorf("the node does not verify %s: %v", algorithm, err)
		}
	}
	// sha3-256("abc"), FIPS 202's own example.
	got, err := digestOf([]byte("abc"), "sha3-256")
	if err != nil || got != "3a985da74fe225b2045c172d6bd390bd855f086e3e9d525b46bfe24511431532" {
		t.Errorf("sha3-256(abc) = %s, %v", got, err)
	}
	for _, algorithm := range []string{"md5", "sha1", "sha3-512", "nonsense"} {
		if CheckHashType(algorithm) == nil {
			t.Errorf("%s was accepted as a hash_type", algorithm)
		}
	}
}
