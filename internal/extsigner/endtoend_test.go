package extsigner

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/bridge"
	"github.com/edlitmus/halite/internal/jobsign"
)

// The whole chain, with nothing faked: the real reference extension
// binary, built here; the real bridge protocol over a real pipe to a
// real process; a real digest signed and a real signature verified
// against the public key the same extension reports.
//
// Package extsigner and internal/jobsign each have their own tests
// against a fake Caller and a fake key respectively. This one exists
// because a signature that verifies against the code that produced it
// is not yet a signature format anything else can produce or check --
// DIVERGENCE 5.128 makes the identical point about jobsign's own ASN.1
// DER, driven against `openssl dgst -verify`. Here the thing being
// crossed is a process boundary rather than another program, and the
// boundary is where a request or a response gets reshaped wrong.
func TestSignerExtensionSignsAndVerifiesForReal(t *testing.T) {
	exe := buildSignerExtension(t)

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "signer.key")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, err := jobsign.EncodePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	proc, err := bridge.Start(ctx, bridge.Options{
		Path:    exe,
		Kind:    ext.KindSigner,
		WorkDir: t.TempDir(),
		Env:     append(os.Environ(), "HALITE_EXT_SIGNER_KEY_FILE="+keyPath),
	})
	if err != nil {
		t.Fatalf("starting the extension: %v", err)
	}
	defer proc.Close()

	signer := &Bridged{Name: exe, Ext: proc}

	pub, err := signer.PublicKey(ctx)
	if err != nil {
		t.Fatalf("public_key: %v", err)
	}
	if !pub.Equal(&key.PublicKey) {
		t.Error("the reported public key does not match the private key the extension was given")
	}

	payload := jobsign.Payload{JID: "20260929000000000000", Target: "web*", TargetKind: "glob",
		Fun: "test.ping", Env: "base"}
	digest := jobsign.Digest(payload)

	sig, err := signer.Sign(ctx, digest)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		t.Error("the extension's signature does not verify against its own reported public key")
	}

	// The same round trip through jobsign.Verify, which is what a node
	// actually calls -- proving the bridge output is not merely valid
	// ASN.1 DER but the exact thing require_job_signature checks.
	name, err := jobsign.Verify(
		[]jobsign.SignerKey{{Name: "bridged", Key: pub}}, payload,
		base64.StdEncoding.EncodeToString(sig))
	if err != nil {
		t.Fatalf("jobsign.Verify: %v", err)
	}
	if name != "bridged" {
		t.Errorf("verified against %q, want %q", name, "bridged")
	}

	// A digest signed once must not verify against a different payload
	// -- the ordinary property of a signature, checked here because it
	// is the property the whole bridge exists to carry across a process
	// boundary intact.
	other := jobsign.Payload{JID: payload.JID, Target: "db*", TargetKind: "glob",
		Fun: "test.ping", Env: "base"}
	if _, err := jobsign.Verify([]jobsign.SignerKey{{Name: "bridged", Key: pub}}, other,
		base64.StdEncoding.EncodeToString(sig)); err == nil {
		t.Error("a signature over one target verified against a different one")
	}
}

func TestSignerExtensionRefusesWithoutAKeyFile(t *testing.T) {
	exe := buildSignerExtension(t)
	ctx := context.Background()
	_, err := bridge.Start(ctx, bridge.Options{
		Path:    exe,
		Kind:    ext.KindSigner,
		WorkDir: t.TempDir(),
		// HALITE_EXT_SIGNER_KEY_FILE deliberately absent.
		Env: os.Environ(),
	})
	if err == nil {
		t.Fatal("the extension started with no key file configured")
	}
}

var (
	buildOnce     sync.Once
	signerExePath string
	buildErr      error
)

// buildSignerExtension compiles the reference extension once per test
// binary run.
func buildSignerExtension(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "halite-ext-signer-*")
		if err != nil {
			buildErr = err
			return
		}
		name := "signer-local"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		signerExePath = filepath.Join(dir, name)
		build := exec.Command("go", "build", "-o", signerExePath, "../../cmd/halite-ext-signer-local")
		build.Stderr = os.Stderr
		buildErr = build.Run()
	})
	if buildErr != nil {
		t.Fatalf("building the reference extension: %v", buildErr)
	}
	return signerExePath
}
