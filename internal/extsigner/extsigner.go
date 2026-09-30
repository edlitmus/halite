// Package extsigner connects SPEC 25.6's optional detached job signing
// to the extension model of SPEC 24: the bridged `signer` extension
// that section 25.6 names as an alternative to a key held directly on
// the machine invoking `halite-hub run`.
//
// # Why signing needs a kind of its own
//
// `internal/extpillar` bridges a `pillar`-kind extension the same way,
// and the shape here is deliberately the same: a small `Caller`
// interface a test can satisfy without a real subprocess, and a
// `Bridged` type that turns one operation into one call. What differs is
// what is being delegated. A pillar source is handed the compiling
// node's grains and the tree's pillar so far and answers with a mapping;
// a signer is handed a digest it did not choose the shape of and answers
// with a signature over exactly that digest, because a hardware token or
// a KMS signs bytes, not a `jobsign.Payload`. Package jobsign computes
// the digest so both an in-process key and a bridged one sign the
// identical thing.
package extsigner

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/edlitmus/halite/ext"
)

// Caller is the part of a loaded extension this needs.
//
// An interface rather than *bridge.Process or *extension.Loaded, so a
// test can drive a signer without a subprocess -- the same reason
// internal/extpillar.Caller exists.
type Caller interface {
	Call(ctx context.Context, function string, args, kwargs any, callCtx *ext.CallContext) (json.RawMessage, error)
}

// Bridged is a signer backed by an external process.
type Bridged struct {
	// Name identifies it in an error message -- the extension's
	// executable path, since a signer extension is invoked directly
	// rather than loaded from the fleet's trusted `_ext/` cache.
	Name string
	Ext  Caller
}

// signRequest and signResponse are the "sign" function's request and
// answer. The digest is base64 rather than raw bytes because the
// protocol is JSON, and named rather than positional so a signer
// extension in any language reads it the same way `aws_secrets_manager`
// reads its own request.
type signRequest struct {
	Digest string `json:"digest"`
}

type signResponse struct {
	Signature string `json:"signature"`
}

type publicKeyResponse struct {
	PublicKey string `json:"public_key"`
}

// Sign asks the extension to sign a digest and returns the ASN.1 DER
// signature -- `jobsign.Sign`'s own return shape, decoded rather than
// left base64, so a caller treats a bridged and a local key alike from
// here on.
func (b *Bridged) Sign(ctx context.Context, digest [32]byte) ([]byte, error) {
	if b.Ext == nil {
		return nil, fmt.Errorf("the %q signer extension is not loaded", b.Name)
	}
	raw, err := b.Ext.Call(ctx, "sign", nil, signRequest{
		Digest: base64.StdEncoding.EncodeToString(digest[:]),
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("the %q signer extension: %w", b.Name, err)
	}
	var resp signResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("the %q signer extension answered with something that is not readable: %w",
			b.Name, err)
	}
	if resp.Signature == "" {
		return nil, fmt.Errorf("the %q signer extension answered with no signature", b.Name)
	}
	der, err := base64.StdEncoding.DecodeString(resp.Signature)
	if err != nil {
		return nil, fmt.Errorf("the %q signer extension's signature is not base64: %w", b.Name, err)
	}
	return der, nil
}

// PublicKey asks the extension for the public half of the key it signs
// with, so an operator can put it in a node's `job_signer_keys` without
// ever having the private key leave wherever the extension keeps it.
func (b *Bridged) PublicKey(ctx context.Context) (*ecdsa.PublicKey, error) {
	if b.Ext == nil {
		return nil, fmt.Errorf("the %q signer extension is not loaded", b.Name)
	}
	raw, err := b.Ext.Call(ctx, "public_key", nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("the %q signer extension: %w", b.Name, err)
	}
	var resp publicKeyResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("the %q signer extension answered with something that is not readable: %w",
			b.Name, err)
	}
	der, err := base64.StdEncoding.DecodeString(resp.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("the %q signer extension's public key is not base64: %w", b.Name, err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("the %q signer extension's public key is not DER SubjectPublicKeyInfo: %w",
			b.Name, err)
	}
	pub, ok := parsed.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("the %q signer extension's public key is %T and job signing is ECDSA",
			b.Name, parsed)
	}
	return pub, nil
}
