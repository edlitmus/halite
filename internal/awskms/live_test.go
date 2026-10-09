package awskms

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveKMSSignsADigestThisBuildVerifies is the one test here that
// talks to AWS, and the only evidence that the request and response
// shapes in this package match the service rather than the reference
// they were written from.
//
// It needs a real asymmetric key -- ECC_NIST_P256, SIGN_VERIFY -- that
// the credentials in the environment may call GetPublicKey and Sign on,
// named by HALITE_KMS_KEY (an id, an alias or an ARN) in AWS_REGION. Each
// run makes one Sign call, which the account is billed for and
// CloudTrail records. It is not in fleet.yml: the fleet runners carry
// no AWS credentials, and a key an ephemeral runner could sign with is
// a key SPEC 4.3 says not to have.
//
// Two things are checked. First, that what KMS signs verifies against
// what KMS says the public key is, in this process. Second, when an
// `openssl` is on the path, that `openssl dgst -sha256 -verify` accepts
// the same signature over the original bytes -- because that command is
// what the release notes will tell an operator to run, and a claim in a
// release note is held to the same standard as one in code.
func TestLiveKMSSignsADigestThisBuildVerifies(t *testing.T) {
	if os.Getenv("HALITE_KMS_LIVE") != "1" {
		t.Skip("set HALITE_KMS_LIVE=1, HALITE_KMS_KEY and AWS_REGION, with credentials that may Sign with that key; each run makes one billed KMS call")
	}
	keyID, region := os.Getenv("HALITE_KMS_KEY"), os.Getenv("AWS_REGION")
	if keyID == "" || region == "" {
		t.Fatal("HALITE_KMS_LIVE=1 but HALITE_KMS_KEY or AWS_REGION is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	c := &Client{KeyID: keyID, Region: region, Partition: os.Getenv("AWS_PARTITION")}
	pub, err := c.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("key %s, %s, %s, fingerprint %s", pub.ARN, pub.Spec, pub.Usage, pub.Fingerprint())

	var artifact [64]byte
	if _, err := rand.Read(artifact[:]); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact[:])
	der, err := c.Sign(ctx, digest)
	if err != nil {
		t.Fatal(err)
	}
	// Sign already verified before returning; checked again here so the
	// test's own claim does not rest on the code it is testing.
	if !ecdsa.VerifyASN1(pub.Key, digest[:], der) {
		t.Fatal("the signature does not verify against the public key the service returned")
	}
	t.Logf("KMS signed a 32-byte digest and the %d-byte DER signature verifies", len(der))

	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Log("no openssl on the path: the claim that `openssl dgst -sha256 -verify` accepts these signatures is NOT checked by this run")
		return
	}
	dir := t.TempDir()
	pubFile, sigFile, dataFile := filepath.Join(dir, "k.pub"), filepath.Join(dir, "a.sig"), filepath.Join(dir, "a")
	for _, w := range []struct {
		name string
		data []byte
	}{{pubFile, pub.PEM()}, {sigFile, der}, {dataFile, artifact[:]}} {
		if err := os.WriteFile(w.name, w.data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.CommandContext(ctx, openssl, "dgst", "-sha256", "-verify", pubFile, "-signature", sigFile, dataFile).CombinedOutput()
	if err != nil {
		t.Fatalf("openssl refused the signature: %v\n%s", err, out)
	}
	ver, _ := exec.CommandContext(ctx, openssl, "version").Output()
	t.Logf("%s accepted it: %s", string(ver), string(out))
}
