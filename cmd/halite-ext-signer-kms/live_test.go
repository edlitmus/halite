package main

import (
	"context"
	"encoding/base64"
	"os"
	"testing"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/bridge"
	"github.com/edlitmus/halite/internal/extsigner"
	"github.com/edlitmus/halite/internal/jobsign"
)

// TestLiveKMSJobSignature runs the binary against a real KMS key: started
// through the bridge as `--sign-extension` starts it, asked for the key's
// public half and for a signature over a real job digest, and the
// signature checked by jobsign.Verify, which is what a node runs.
//
//	HALITE_KMS_LIVE=1 HALITE_EXT_SIGNER_KMS_KEY=<key> AWS_REGION=<region> \
//	    go test -count=1 -v -run TestLiveKMSJobSignature ./cmd/halite-ext-signer-kms
//
// It makes one billed Sign call, with credentials from the environment.
func TestLiveKMSJobSignature(t *testing.T) {
	if os.Getenv("HALITE_KMS_LIVE") != "1" || os.Getenv("HALITE_EXT_SIGNER_KMS_KEY") == "" {
		t.Skip("set HALITE_KMS_LIVE=1, HALITE_EXT_SIGNER_KMS_KEY and AWS_REGION, with credentials, to sign with a real KMS key")
	}
	ctx := context.Background()
	proc, err := bridge.Start(ctx, bridge.Options{
		Path: buildExtension(t), Kind: ext.KindSigner, WorkDir: t.TempDir(), Env: os.Environ(),
	})
	if err != nil {
		t.Fatalf("starting the extension: %v", err)
	}
	defer proc.Close()
	signer := &extsigner.Bridged{Name: "halite-ext-signer-kms", Ext: proc}

	pub, err := signer.PublicKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payload := jobsign.Payload{JID: "20261010000000000000", Target: "*", TargetKind: "glob",
		Fun: "test.ping", Env: "base"}
	sig, err := signer.Sign(ctx, jobsign.Digest(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobsign.Verify([]jobsign.SignerKey{{Name: "kms", Key: pub}}, payload,
		base64.StdEncoding.EncodeToString(sig)); err != nil {
		t.Fatalf("a node would refuse the signature: %v", err)
	}
	t.Logf("KMS signed a job digest with %s; jobsign.Verify accepted the %d-byte signature",
		os.Getenv("HALITE_EXT_SIGNER_KMS_KEY"), len(sig))
}
