// Command halite-ext-signer-local is a reference `signer`-kind
// extension (SPEC 24.2/25.6): it signs whatever digest it is handed
// with an ECDSA key held in a local PEM file, over the same
// JSON-over-stdio protocol `halite-ext-aws-secrets` uses for `pillar`.
//
// # What this is, and is not
//
// SPEC 25.6 exists for estates "where hub compromise must not equal
// fleet compromise": the signing key must not be something the hub, or
// the machine invoking `halite-hub run`, holds directly. A file on disk
// next to the CLI is exactly the thing that property rules out, so this
// extension does not close that gap by itself -- it is the reference
// implementation of the *bridge*, the same role `halite-ext-aws-secrets`
// plays for external pillar, proving the protocol carries a real
// signature end to end. Backing it with a hardware token or a cloud
// KMS is a different `handle`, not a different protocol, and is left
// to whoever has one to write against.
//
// # Configuration
//
// One environment variable, because this process is started directly
// by `halite-hub run --sign-extension` rather than loaded from the
// fleet's signed `_ext/` cache -- there is no `ext_pillar`-shaped
// config block to read it from.
//
//	HALITE_EXT_SIGNER_KEY_FILE=/path/to/signer.key halite-hub run \
//	    --sign-extension ./halite-ext-signer-local 'web*' test.ping
//
// The key is PKCS#8 or SEC 1 PEM, P-256 or P-384 -- the same file
// `halite-hub keys signer create` writes and `--sign-key` reads.
package main

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/jobsign"
)

// Version is what the handshake and `sys.list_extensions` report.
const Version = "1.0.0"

func main() {
	// SPEC 24.3's self-applied limits, the way every extension in this
	// tree does it (see halite-ext-aws-secrets's own comment on why
	// this is not optional).
	ext.Confine()

	key, err := loadKey(os.Getenv("HALITE_EXT_SIGNER_KEY_FILE"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "halite-ext-signer-local:", err)
		os.Exit(1)
	}

	e := &ext.Extension{
		Name:    "signer_local",
		Version: Version,
		Kind:    ext.KindSigner,
		// Neither root nor the network declared: the key is a file
		// this process already has open, and signing a digest touches
		// nothing else.
		Functions: functions(),
		Handler:   (&handler{key: key}).handle,
	}
	if err := e.Serve(); err != nil {
		fmt.Fprintln(os.Stderr, "halite-ext-signer-local:", err)
		os.Exit(1)
	}
}

// loadKey reads the PEM file HALITE_EXT_SIGNER_KEY_FILE names.
//
// Read once at startup rather than per call: a KMS-backed signer would
// hold a client and a key identifier instead, and this extension's
// job is to prove the protocol, not to model every way a real one
// might manage its key's lifetime.
func loadKey(path string) (*ecdsa.PrivateKey, error) {
	if path == "" {
		return nil, fmt.Errorf("HALITE_EXT_SIGNER_KEY_FILE is not set; this extension has nothing to sign with")
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	key, err := jobsign.DecodePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return key, nil
}

func functions() []ext.Signature {
	return []ext.Signature{
		{
			Module: "signer_local", Function: "sign",
			Doc: "Sign a base64 SHA-256 digest, answering with the base64 ASN.1 DER signature.",
			Params: []ext.Param{
				{Name: "digest", Type: ext.TypeString, Required: true,
					Doc: "The base64-encoded digest to sign."},
			},
		},
		{
			Module: "signer_local", Function: "public_key",
			Doc: "Return the base64 DER SubjectPublicKeyInfo of this signer's public key.",
		},
	}
}

type handler struct {
	key *ecdsa.PrivateKey
}

type signRequest struct {
	Digest string `json:"digest"`
}

// handle answers `sign` and `public_key`. Neither reads call.Args: both
// take a single named field, and a bridged signer's whole point is that
// nothing about the job it is signing for reaches it -- the digest
// already stands for that, which is why `internal/extsigner` sends it
// as the request and nothing else.
func (h *handler) handle(call ext.Call) (any, error) {
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
		digest, err := base64.StdEncoding.DecodeString(req.Digest)
		if err != nil {
			return nil, fmt.Errorf("the digest is not base64: %w", err)
		}
		der, err := ecdsa.SignASN1(rand.Reader, h.key, digest)
		if err != nil {
			return nil, fmt.Errorf("signing: %w", err)
		}
		return map[string]any{"signature": base64.StdEncoding.EncodeToString(der)}, nil
	case "public_key":
		der, err := x509.MarshalPKIXPublicKey(&h.key.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("encoding the public key: %w", err)
		}
		return map[string]any{"public_key": base64.StdEncoding.EncodeToString(der)}, nil
	}
	return nil, fmt.Errorf("this extension provides sign and public_key, not %q", call.Function)
}
