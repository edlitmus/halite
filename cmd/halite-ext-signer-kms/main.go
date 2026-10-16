// Command halite-ext-signer-kms is a `signer`-kind extension (SPEC
// 24.2/25.6) that signs with an asymmetric key held in AWS KMS: the
// signer section 25.6 names, "a bridged `signer` extension backed by a
// KMS". The private key never leaves the service, so neither the hub nor
// the machine running `halite-hub run` holds anything that can sign a
// job; what an operator holds is permission to ask KMS for a signature,
// and every signature is a CloudTrail record.
//
// It speaks the protocol `halite-ext-signer-local` does -- `sign` a
// base64 SHA-256 digest, `public_key` as base64 DER -- so
// internal/extsigner and `--sign-extension` take it without change. The
// KMS half is internal/awskms, the client release signing uses, which
// verifies every signature against the key's own public half before it
// is returned.
//
// # Configuration
//
// Environment variables, because this process is started directly by
// `halite-hub run --sign-extension`, which hands it the operator's own
// environment:
//
//	HALITE_EXT_SIGNER_KMS_KEY  the key: an id, alias/<name>, or an ARN
//	AWS_REGION                 the key's region, unless the key is an ARN
//	AWS_ACCESS_KEY_ID, ...     credentials, as awsauth reads them: the
//	                           environment, a container's, or an instance role
//
//	HALITE_EXT_SIGNER_KMS_KEY=alias/halite-jobs AWS_REGION=us-west-2 \
//	    halite-hub run --sign-extension ./halite-ext-signer-kms 'web*' test.ping
//
// The key is ECC_NIST_P256 with usage SIGN_VERIFY. KMS signs a SHA-256
// digest only with that curve, and a job's digest is SHA-256; a P-384
// key is refused by name when the extension starts.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/awskms"
)

// Version is what the handshake and `sys.list_extensions` report. It is
// also what a pin names, so it changes when behaviour does -- including
// behaviour that arrives through a package this extension links, which
// is how 1.0.1 came about: internal/awsauth began refusing a `region`
// that is not a region name before it builds the STS host (DIVERGENCE
// 5.324), so a configuration that used to send a signed AssumeRole call
// somewhere now fails before it is sent.
const Version = "1.0.1"

// callTimeout bounds one call to KMS. A `run` waits on it, so a service
// that does not answer should fail the submission in seconds rather
// than hold the operator's terminal.
const callTimeout = 30 * time.Second

// kmsKey is what the handler needs from the client: a seam for the unit
// tests, which sign with a local key instead of a service.
type kmsKey interface {
	Sign(ctx context.Context, digest [sha256.Size]byte) ([]byte, error)
	PublicKey(ctx context.Context) (*awskms.PublicKey, error)
}

func main() {
	// SPEC 24.3's self-applied limits, as every extension in this tree
	// applies them.
	ext.Confine()

	client, err := newKey(os.Getenv("HALITE_EXT_SIGNER_KMS_KEY"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "halite-ext-signer-kms:", err)
		os.Exit(1)
	}

	e := &ext.Extension{
		Name:      "signer_kms",
		Version:   Version,
		Kind:      ext.KindSigner,
		Declares:  []string{"network"},
		Functions: functions(),
		Handler:   (&handler{key: client}).handle,
	}
	if err := e.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "halite-ext-signer-kms:", err)
		os.Exit(1)
	}
}

// newKey builds the client and asks KMS for the key's public half before
// serving anything, so a key that does not exist, cannot sign, or is on
// the wrong curve is an error at startup, naming the key, rather than a
// failed signature after the operator has typed a whole command.
func newKey(name string) (*awskms.Client, error) {
	if name == "" {
		return nil, fmt.Errorf("HALITE_EXT_SIGNER_KMS_KEY is not set; this extension has no key to sign with")
	}
	client, err := awskms.NewClient(name, "", "")
	if err != nil {
		return nil, fmt.Errorf("HALITE_EXT_SIGNER_KMS_KEY=%s: %w", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if _, err := client.PublicKey(ctx); err != nil {
		return nil, err
	}
	return client, nil
}

func functions() []ext.Signature {
	return []ext.Signature{
		{
			Module: "signer_kms", Function: "sign",
			Doc: "Sign a base64 SHA-256 digest with the KMS key, answering with the base64 ASN.1 DER signature.",
			Params: []ext.Param{
				{Name: "digest", Type: ext.TypeString, Required: true,
					Doc: "The base64-encoded SHA-256 digest to sign."},
			},
		},
		{
			Module: "signer_kms", Function: "public_key",
			Doc: "Return the base64 DER SubjectPublicKeyInfo of the KMS key's public half.",
		},
	}
}

type handler struct {
	key kmsKey
}

type signRequest struct {
	Digest string `json:"digest"`
}

// handle answers `sign` and `public_key`, as halite-ext-signer-local
// does. A digest that is not 32 bytes is refused here rather than sent:
// KMS would refuse it too, but with a message about the service's API
// rather than about the job.
func (h *handler) handle(call ext.Call) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	switch call.Function {
	case "sign":
		var req signRequest
		if len(call.Kwargs) > 0 {
			if err := json.Unmarshal(call.Kwargs, &req); err != nil {
				return nil, fmt.Errorf("the request is not readable: %w", err)
			}
		}
		if req.Digest == "" {
			return nil, fmt.Errorf("sign needs a digest")
		}
		raw, err := base64.StdEncoding.DecodeString(req.Digest)
		if err != nil {
			return nil, fmt.Errorf("the digest is not base64: %w", err)
		}
		if len(raw) != sha256.Size {
			return nil, fmt.Errorf("the digest is %d bytes; this signer signs a %d-byte SHA-256 digest",
				len(raw), sha256.Size)
		}
		var digest [sha256.Size]byte
		copy(digest[:], raw)
		der, err := h.key.Sign(ctx, digest)
		if err != nil {
			return nil, err
		}
		return map[string]any{"signature": base64.StdEncoding.EncodeToString(der)}, nil
	case "public_key":
		pub, err := h.key.PublicKey(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"public_key": base64.StdEncoding.EncodeToString(pub.DER)}, nil
	}
	return nil, fmt.Errorf("this extension provides sign and public_key, not %q", call.Function)
}
