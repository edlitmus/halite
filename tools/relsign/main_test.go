package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/awskms"
)

// localSigner signs with a key in memory, standing in for KMS so that
// `sign` and `verify` are held to each other here. The KMS client's own
// tests hold it to the service's shapes; this holds the tool to the
// files it writes and reads.
type localSigner struct {
	key   *ecdsa.PrivateKey
	calls int
	// lie, when set, returns a signature from a different key.
	lie *ecdsa.PrivateKey
}

func newLocalSigner(t *testing.T) *localSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &localSigner{key: key}
}

func (s *localSigner) PublicKey(context.Context) (*awskms.PublicKey, error) {
	der, err := x509.MarshalPKIXPublicKey(&s.key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &awskms.PublicKey{ARN: "arn:aws:kms:us-east-1:111122223333:key/local", Spec: "ECC_NIST_P256",
		Usage: "SIGN_VERIFY", Key: &s.key.PublicKey, DER: der}, nil
}

func (s *localSigner) Sign(_ context.Context, digest [sha256.Size]byte) ([]byte, error) {
	s.calls++
	key := s.key
	if s.lie != nil {
		key = s.lie
	}
	return ecdsa.SignASN1(rand.Reader, key, digest[:])
}

func (s *localSigner) pubFile(t *testing.T, dir string) string {
	t.Helper()
	pub, _ := s.PublicKey(context.Background())
	path := filepath.Join(dir, "halite-release.pub")
	if err := os.WriteFile(path, pub.PEM(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// makeDist writes a dist/ with the named files and a SHA256SUMS over
// them in the form `make dist` writes: two spaces, sorted.
func makeDist(t *testing.T, files map[string]string) string {
	t.Helper()
	dist := t.TempDir()
	var names []string
	for name := range files {
		names = append(names, name)
	}
	var manifest bytes.Buffer
	for _, name := range sortedStrings(names) {
		if err := os.WriteFile(filepath.Join(dist, name), []byte(files[name]), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(files[name]))
		fmt.Fprintf(&manifest, "%x  %s\n", sum, name)
	}
	if err := os.WriteFile(filepath.Join(dist, manifestName), manifest.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return dist
}

func sortedStrings(s []string) []string {
	out := append([]string(nil), s...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

var threeFiles = map[string]string{
	"halite-node-linux-amd64":         "node",
	"halite-0.1.0-linux-amd64.tar.gz": "archive",
	"halite-0.1.0-windows-amd64.zip":  "zip",
}

func TestSignWritesASignaturePerArtifactAndOneForTheManifestThatVerifyAccepts(t *testing.T) {
	s := newLocalSigner(t)
	dist := makeDist(t, threeFiles)
	pub := s.pubFile(t, dist)

	var out bytes.Buffer
	if err := sign(context.Background(), dist, pub, s, &out); err != nil {
		t.Fatal(err)
	}
	if s.calls != 4 {
		t.Errorf("%d signatures requested, want 3 artifacts + 1 manifest", s.calls)
	}
	for name := range threeFiles {
		if _, err := os.Stat(filepath.Join(dist, name+".sig")); err != nil {
			t.Error(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dist, "SHA256SUMS.sig")); err != nil {
		t.Error(err)
	}
	if !strings.Contains(out.String(), "signing 3 artifacts and SHA256SUMS") {
		t.Errorf("output:\n%s", out.String())
	}

	out.Reset()
	if err := verify(dist, pub, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SHA256SUMS and 3 artifacts") {
		t.Errorf("output:\n%s", out.String())
	}

	// The promise in the release notes: openssl, with nothing from this
	// repository but the public key, accepts the manifest's signature
	// over the manifest's bytes. Checked where an openssl is present,
	// and said so where it is not, rather than silently passing.
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Log("no openssl on the path; the openssl verification of the manifest signature was NOT run")
		return
	}
	cmd := exec.Command(openssl, "dgst", "-sha256", "-verify", pub,
		"-signature", filepath.Join(dist, "SHA256SUMS.sig"), filepath.Join(dist, "SHA256SUMS"))
	if res, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("openssl refused SHA256SUMS.sig: %v\n%s", err, res)
	}
	cmd = exec.Command(openssl, "dgst", "-sha256", "-verify", pub,
		"-signature", filepath.Join(dist, "halite-node-linux-amd64.sig"), filepath.Join(dist, "halite-node-linux-amd64"))
	if res, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("openssl refused an artifact's .sig: %v\n%s", err, res)
	}
}

func TestVerifyReportsEveryProblemNotTheFirst(t *testing.T) {
	s := newLocalSigner(t)
	dist := makeDist(t, threeFiles)
	pub := s.pubFile(t, dist)
	if err := sign(context.Background(), dist, pub, s, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	// One file altered after signing, one signature altered, one
	// signature missing.
	if err := os.WriteFile(filepath.Join(dist, "halite-node-linux-amd64"), []byte("not the node"), 0o644); err != nil {
		t.Fatal(err)
	}
	sig := filepath.Join(dist, "halite-0.1.0-linux-amd64.tar.gz.sig")
	raw, _ := os.ReadFile(sig)
	raw[len(raw)/2] ^= 0x01
	if err := os.WriteFile(sig, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dist, "halite-0.1.0-windows-amd64.zip.sig")); err != nil {
		t.Fatal(err)
	}

	err := verify(dist, pub, &bytes.Buffer{})
	if err == nil {
		t.Fatal("verify passed a set with three problems")
	}
	for _, want := range []string{"3 problem(s)",
		"halite-node-linux-amd64: digest",
		"halite-0.1.0-linux-amd64.tar.gz: the signature does not verify",
		"halite-0.1.0-windows-amd64.zip: no signature"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestVerifyRefusesAManifestWhoseOwnSignatureIsWrong(t *testing.T) {
	s := newLocalSigner(t)
	dist := makeDist(t, threeFiles)
	pub := s.pubFile(t, dist)
	if err := sign(context.Background(), dist, pub, s, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	// A manifest edited after signing -- a line added for a file that
	// was never built -- with every per-file signature still good.
	extra := []byte("extra")
	if err := os.WriteFile(filepath.Join(dist, "extra"), extra, 0o644); err != nil {
		t.Fatal(err)
	}
	m := filepath.Join(dist, manifestName)
	f, _ := os.OpenFile(m, os.O_APPEND|os.O_WRONLY, 0)
	fmt.Fprintf(f, "%x  extra\n", sha256.Sum256(extra))
	f.Close()

	err := verify(dist, pub, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS: the signature does not verify") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "extra: no signature") {
		t.Errorf("the unsigned extra file was not reported:\n%v", err)
	}
}

func TestSignRefusesBeforeTheFirstCallWhenAFileDoesNotMatchItsLine(t *testing.T) {
	s := newLocalSigner(t)
	dist := makeDist(t, threeFiles)
	pub := s.pubFile(t, dist)
	if err := os.WriteFile(filepath.Join(dist, "halite-node-linux-amd64"), []byte("rebuilt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dist, "halite-0.1.0-windows-amd64.zip")); err != nil {
		t.Fatal(err)
	}
	err := sign(context.Background(), dist, pub, s, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "refusing to sign: 2 file(s)") {
		t.Fatalf("err = %v", err)
	}
	if s.calls != 0 {
		t.Errorf("%d signatures were requested for a set that does not match its manifest", s.calls)
	}
	if matches, _ := filepath.Glob(filepath.Join(dist, "*.sig")); len(matches) != 0 {
		t.Errorf("signatures were written: %v", matches)
	}
}

func TestSignRefusesAKeyThatIsNotTheCommittedOne(t *testing.T) {
	s := newLocalSigner(t)
	other := newLocalSigner(t)
	dist := makeDist(t, threeFiles)
	pub := other.pubFile(t, dist)
	err := sign(context.Background(), dist, pub, s, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "is not the key in") {
		t.Fatalf("err = %v", err)
	}
	if s.calls != 0 {
		t.Error("a signature was requested from the wrong key")
	}
}

// awskms.Client checks each signature itself; `sign` takes any signer,
// so it checks again before a file is written. A signer that answers
// with another key's signature produces no .sig.
func TestSignDoesNotWriteASignatureThatDoesNotVerify(t *testing.T) {
	s := newLocalSigner(t)
	s.lie = newLocalSigner(t).key
	dist := makeDist(t, threeFiles)
	pub := s.pubFile(t, dist)
	err := sign(context.Background(), dist, pub, s, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "does not verify against the public key; not written") {
		t.Fatalf("err = %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dist, "*.sig")); len(matches) != 0 {
		t.Errorf("signatures were written: %v", matches)
	}
}

func TestSignRefusesToSignASetTwice(t *testing.T) {
	s := newLocalSigner(t)
	dist := makeDist(t, threeFiles)
	pub := s.pubFile(t, dist)
	if err := sign(context.Background(), dist, pub, s, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	calls := s.calls
	err := sign(context.Background(), dist, pub, s, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v", err)
	}
	if s.calls != calls {
		t.Error("a second signing requested signatures")
	}
}

func TestTheManifestIsReadStrictly(t *testing.T) {
	good := strings.Repeat("ab", 32)
	cases := []struct {
		name, manifest, want string
	}{
		{"one space", good + " halite-node\n", "not `<sha256>  <name>`"},
		{"short digest", "abcd  halite-node\n", "not `<sha256>  <name>`"},
		{"not hex", strings.Repeat("zz", 32) + "  halite-node\n", "not hex"},
		{"a path", good + "  ../halite-node\n", "not a bare file name"},
		{"backslash", good + "  dir\\halite-node\n", "not a bare file name"},
		{"itself", good + "  SHA256SUMS\n", "written after it"},
		{"a signature", good + "  halite-node.sig\n", "written after it"},
		{"twice", good + "  halite-node\n" + good + "  halite-node\n", "listed twice"},
		{"blank line", good + "  halite-node\n\n", "empty line"},
		{"empty", "", "lists nothing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dist := t.TempDir()
			if err := os.WriteFile(filepath.Join(dist, manifestName), []byte(tc.manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := readManifest(dist)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	// And the shape `make dist` writes is read, with the file's bytes
	// returned unchanged for the manifest's own signature.
	dist := makeDist(t, threeFiles)
	entries, raw, err := readManifest(dist)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[0].name != "halite-0.1.0-linux-amd64.tar.gz" {
		t.Errorf("entries %+v", entries)
	}
	onDisk, _ := os.ReadFile(filepath.Join(dist, manifestName))
	if !bytes.Equal(raw, onDisk) {
		t.Error("the manifest bytes returned are not the file's")
	}
}

func TestTheRegionAndPartitionComeFromTheFlagTheEnvironmentOrTheARN(t *testing.T) {
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_PARTITION", "")
	k := kmsFlags{key: "alias/halite-release"}
	if _, err := k.client(); err == nil || !strings.Contains(err.Error(), "no region") {
		t.Errorf("alias with no region: err = %v", err)
	}
	k = kmsFlags{key: "arn:aws-us-gov:kms:us-gov-west-1:111122223333:key/abc"}
	c, err := k.client()
	if err != nil || c.Region != "us-gov-west-1" || c.Partition != "aws-us-gov" {
		t.Errorf("from ARN: %+v, %v", c, err)
	}
	t.Setenv("AWS_REGION", "eu-west-1")
	k = kmsFlags{key: "alias/halite-release"}
	c, err = k.client()
	if err != nil || c.Region != "eu-west-1" {
		t.Errorf("from environment: %+v, %v", c, err)
	}
	k = kmsFlags{key: "alias/halite-release", region: "ap-south-1"}
	c, err = k.client()
	if err != nil || c.Region != "ap-south-1" {
		t.Errorf("from flag: %+v, %v", c, err)
	}
	if _, err := (&kmsFlags{}).client(); err == nil {
		t.Error("no key accepted")
	}
}

func TestSignNeedsThePublicKeyFile(t *testing.T) {
	err := signCmd(context.Background(), []string{"-key", "alias/x", "-region", "us-east-1"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "-pub is required") {
		t.Fatalf("err = %v", err)
	}
}
