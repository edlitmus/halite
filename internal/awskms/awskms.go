// Package awskms signs digests with an asymmetric key held in AWS KMS.
//
// SPEC 4.3 asks for a detached signature per release artifact and
// deferred it on one question: where a signing key lives, given that a
// long-lived key does not belong on a hosted runner. A KMS key answers
// that by not living anywhere a process can read. The private half is
// generated inside the service's HSMs and never leaves them; what a
// caller holds is permission to ask for a signature over a digest, and
// every such request is a CloudTrail record naming who asked. The same
// call is what SPEC 25.6's "signer backed by a KMS" needs for jobs, so
// this package is written to serve both and is tied to neither.
//
// # Two operations, no SDK
//
// Sign and GetPublicKey are each one JSON 1.1 POST with a target header,
// signed with SigV4 by internal/awsauth. That is the whole protocol, and
// the reasoning is cmd/halite-ext-aws-secrets's: one operation is not a
// reason to link an SDK, and SPEC 4.2's allowlist would not admit one.
//
// # The signature is checked before it is returned
//
// Sign verifies what KMS answered against the key's public half before
// handing it back. A signature is a claim, and this project's rule is
// that a claim nobody demonstrated is a defect: a wrong key id, a key
// whose algorithm does not match, or a response that was altered on the
// way all produce bytes that look like a signature and are not one. The
// cost is one GetPublicKey per client and one ECDSA verification per
// signature, which is nothing beside the HTTPS round trip.
//
// # SHA-256 digests and P-256 keys only, for now
//
// KMS pairs each elliptic curve with exactly one signing algorithm: an
// ECC_NIST_P256 key signs ECDSA_SHA_256 and an ECC_NIST_P384 key signs
// ECDSA_SHA_384, and the digest handed to it must be the one that
// algorithm names. Everything this package is asked to sign is a
// SHA-256 digest -- a SHA256SUMS line, a jobsign.Digest -- so a P-384
// key is refused by name rather than producing a request the service
// would reject with a message about message length.
//
// # What has and has not been demonstrated
//
// The request and response shapes here are from the KMS API reference.
// The unit tests drive a fake that was written from the same reference,
// so they hold this code to the documentation and not to the service.
// TestLiveKMS* in live_test.go is what holds it to the service, and it
// needs a real key; until somebody has run it, this package is in the
// state CLAUDE.md calls "assumed", and says so here rather than in a
// note nobody reads.
package awskms

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/edlitmus/halite/internal/awsauth"
)

// maxBody bounds one KMS response. A public key and a signature are
// each under a kilobyte; a megabyte leaves room for the service's own
// error messages and stops a wrong endpoint from being read without
// bound.
const maxBody = 1 << 20

// Algorithm is the one signing algorithm this package asks for. See the
// package comment for why it is not chosen per key.
const Algorithm = "ECDSA_SHA_256"

// Client signs with one KMS key.
//
// Region is required: KMS keys are regional, and a request signed for
// the wrong region is refused by the service with a message about the
// signature rather than the region. Partition decides the endpoint's
// domain the way every AWS client in this project does.
type Client struct {
	// KeyID is a key id, an alias (`alias/halite-release`), or either
	// one's ARN. The service accepts all four spellings.
	KeyID     string
	Region    string
	Partition string
	Provider  *awsauth.Provider
	// Endpoint overrides the host, for the tests.
	Endpoint string
	HTTP     *http.Client
	Timeout  time.Duration
	// Now is the clock, for the tests.
	Now func() time.Time

	mu  sync.Mutex
	pub *PublicKey
}

// PublicKey is the public half of a KMS key, with what the service said
// about it.
type PublicKey struct {
	// ARN is the key's full ARN, which the service answers even when it
	// was asked by alias. It names the account and region, which is
	// what an operator wants in a log line.
	ARN string
	// Spec is the service's name for the curve, `ECC_NIST_P256`.
	Spec string
	// Usage is `SIGN_VERIFY` for a signing key. Anything else cannot
	// sign and is refused up front.
	Usage string
	Key   *ecdsa.PublicKey
	// DER is the SubjectPublicKeyInfo exactly as the service returned
	// it, so a committed public key file can be compared byte for byte
	// rather than through a parse that might normalise something.
	DER []byte
}

// PEM renders the key as `openssl` and every other tool reads it.
func (p *PublicKey) PEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: p.DER})
}

// Fingerprint is the SHA-256 of the DER, hex, for a release note or a
// log line to name the key without printing it.
func (p *PublicKey) Fingerprint() string {
	sum := sha256.Sum256(p.DER)
	return "SHA256:" + hex.EncodeToString(sum[:])
}

// ParsePEM reads a public key file written by PEM, or by
// `openssl ec -pubout`, into the same shape GetPublicKey answers with,
// so the two can be compared.
func ParsePEM(data []byte) (*PublicKey, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, errors.New("not a PEM public key")
	}
	if block.Type != "PUBLIC KEY" {
		return nil, fmt.Errorf("a PEM block of type %q, not PUBLIC KEY", block.Type)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("more than one PEM block; a release public key file holds exactly one")
	}
	pub, err := parseSPKI(block.Bytes)
	if err != nil {
		return nil, err
	}
	return &PublicKey{Key: pub, DER: block.Bytes, Spec: "ECC_NIST_P256", Usage: "SIGN_VERIFY"}, nil
}

func parseSPKI(der []byte) (*ecdsa.PublicKey, error) {
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("not a public key in DER SubjectPublicKeyInfo form: %w", err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("the key is %T and this signs with ECDSA", parsed)
	}
	if pub.Curve != elliptic.P256() {
		name := "an unnamed curve"
		if pub.Curve != nil && pub.Curve.Params() != nil {
			name = pub.Curve.Params().Name
		}
		return nil, fmt.Errorf("the key is on %s; this signs SHA-256 digests, which KMS pairs with P-256 only", name)
	}
	return pub, nil
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

func (c *Client) provider() *awsauth.Provider {
	if c.Provider != nil {
		return c.Provider
	}
	c.Provider = &awsauth.Provider{Partition: c.Partition, Region: c.Region, Now: c.Now}
	return c.Provider
}

// endpoint is the KMS host for the region.
//
// Built from the partition rather than hardcoded, for the reason
// internal/awsauth gives: an endpoint for the commercial partition is
// wrong in GovCloud and in China. GovCloud's KMS hosts are under
// `amazonaws.com` like the commercial ones; only China differs.
func (c *Client) endpoint() string {
	if c.Endpoint != "" {
		return strings.TrimSuffix(c.Endpoint, "/")
	}
	suffix := "amazonaws.com"
	if c.Partition == "aws-cn" {
		suffix = "amazonaws.com.cn"
	}
	return "https://kms." + c.Region + "." + suffix
}

// apiError is how KMS reports a refusal: HTTP 400 with a JSON body
// naming the exception. The message's key is spelled both ways across
// AWS services, so both are read.
type apiError struct {
	Type     string `json:"__type"`
	Message  string `json:"message"`
	Message2 string `json:"Message"`
}

// call makes one signed KMS request and answers with the body.
func (c *Client) call(ctx context.Context, target string, request any) ([]byte, error) {
	if c.KeyID == "" {
		return nil, errors.New("no KMS key id")
	}
	if c.Region == "" {
		return nil, errors.New("no region; KMS keys are regional, so a key id alone does not name one")
	}
	creds, err := c.provider().Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving AWS credentials: %w", err)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	endpoint := c.endpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "TrentService."+target)
	req.ContentLength = int64(len(body))

	sum := sha256.Sum256(body)
	signer := awsauth.Signer{Region: c.Region, Service: "kms"}
	if err := signer.Sign(req, creds, hex.EncodeToString(sum[:]), c.now()); err != nil {
		return nil, err
	}

	res, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching %s: %w", endpoint, err)
	}
	defer res.Body.Close()
	resBody, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		// The service's own message is the useful part -- it says
		// which of "no such key", "not allowed", "wrong region" and
		// "wrong algorithm for this key" happened -- and it carries no
		// key material.
		var apiErr apiError
		_ = json.Unmarshal(resBody, &apiErr)
		msg := apiErr.Message
		if msg == "" {
			msg = apiErr.Message2
		}
		if apiErr.Type != "" || msg != "" {
			return nil, fmt.Errorf("kms %s: %s answered %d: %s %s",
				target, endpoint, res.StatusCode, apiErr.Type, msg)
		}
		return nil, fmt.Errorf("kms %s: %s answered %d", target, endpoint, res.StatusCode)
	}
	return resBody, nil
}

// getPublicKeyResponse is the part of GetPublicKey's answer that is used.
type getPublicKeyResponse struct {
	KeyID             string   `json:"KeyId"`
	PublicKey         string   `json:"PublicKey"`
	KeySpec           string   `json:"KeySpec"`
	KeyUsage          string   `json:"KeyUsage"`
	SigningAlgorithms []string `json:"SigningAlgorithms"`
}

// PublicKey fetches the key's public half, once per client.
//
// Cached, because Sign needs it for every signature and the key does
// not change under a client: KMS does not rotate asymmetric keys in
// place, and a rotation is a new key with a new id.
func (c *Client) PublicKey(ctx context.Context) (*PublicKey, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pub != nil {
		return c.pub, nil
	}
	body, err := c.call(ctx, "GetPublicKey", map[string]string{"KeyId": c.KeyID})
	if err != nil {
		return nil, err
	}
	var parsed getPublicKeyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("kms GetPublicKey: the response is not readable: %w", err)
	}
	der, err := base64.StdEncoding.DecodeString(parsed.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("kms GetPublicKey: the public key is not base64: %w", err)
	}
	if parsed.KeyUsage != "SIGN_VERIFY" {
		return nil, fmt.Errorf("kms key %s has usage %q and cannot sign; a signing key is SIGN_VERIFY",
			c.KeyID, parsed.KeyUsage)
	}
	pub, err := parseSPKI(der)
	if err != nil {
		return nil, fmt.Errorf("kms key %s (%s): %w", c.KeyID, parsed.KeySpec, err)
	}
	// Belt and braces: the curve check above already implies this, but
	// the service states which algorithms the key signs and a key that
	// does not list the one about to be asked for will refuse it.
	found := false
	for _, a := range parsed.SigningAlgorithms {
		if a == Algorithm {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("kms key %s signs %v, not %s", c.KeyID, parsed.SigningAlgorithms, Algorithm)
	}
	c.pub = &PublicKey{ARN: parsed.KeyID, Spec: parsed.KeySpec, Usage: parsed.KeyUsage, Key: pub, DER: der}
	return c.pub, nil
}

// signResponse is the part of Sign's answer that is used.
type signResponse struct {
	KeyID            string `json:"KeyId"`
	Signature        string `json:"Signature"`
	SigningAlgorithm string `json:"SigningAlgorithm"`
}

// Sign asks KMS for an ECDSA signature over a SHA-256 digest and answers
// with it in ASN.1 DER -- the form `ecdsa.VerifyASN1`, `openssl dgst
// -verify` and internal/jobsign all take.
//
// MessageType is DIGEST: the caller hashed the artifact and KMS signs
// the hash, so the bytes of a release archive never go to the service.
// The returned signature is verified against the key's public half
// before it is returned; see the package comment.
func (c *Client) Sign(ctx context.Context, digest [sha256.Size]byte) ([]byte, error) {
	pub, err := c.PublicKey(ctx)
	if err != nil {
		return nil, err
	}
	body, err := c.call(ctx, "Sign", map[string]string{
		"KeyId":            c.KeyID,
		"Message":          base64.StdEncoding.EncodeToString(digest[:]),
		"MessageType":      "DIGEST",
		"SigningAlgorithm": Algorithm,
	})
	if err != nil {
		return nil, err
	}
	var parsed signResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("kms Sign: the response is not readable: %w", err)
	}
	if parsed.SigningAlgorithm != "" && parsed.SigningAlgorithm != Algorithm {
		return nil, fmt.Errorf("kms Sign: asked for %s and the service answered with %s",
			Algorithm, parsed.SigningAlgorithm)
	}
	der, err := base64.StdEncoding.DecodeString(parsed.Signature)
	if err != nil {
		return nil, fmt.Errorf("kms Sign: the signature is not base64: %w", err)
	}
	if !ecdsa.VerifyASN1(pub.Key, digest[:], der) {
		return nil, fmt.Errorf("kms Sign: the signature from %s does not verify against that key's own public half; refusing to return it",
			pub.ARN)
	}
	return der, nil
}
