package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/awskms"
	"github.com/edlitmus/halite/internal/bridge"
	"github.com/edlitmus/halite/internal/extsigner"
	"github.com/edlitmus/halite/internal/jobsign"
)

// localKey stands in for KMS in the handler's unit tests.
type localKey struct{ key *ecdsa.PrivateKey }

func (l localKey) Sign(_ context.Context, digest [sha256.Size]byte) ([]byte, error) {
	return ecdsa.SignASN1(rand.Reader, l.key, digest[:])
}

func (l localKey) PublicKey(context.Context) (*awskms.PublicKey, error) {
	der, err := x509.MarshalPKIXPublicKey(&l.key.PublicKey)
	if err != nil {
		return nil, err
	}
	return &awskms.PublicKey{Key: &l.key.PublicKey, DER: der}, nil
}

func call(t *testing.T, h *handler, function string, kwargs any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(kwargs)
	out, err := h.handle(ext.Call{Function: function, Kwargs: raw})
	if err != nil {
		return nil, err
	}
	return out.(map[string]any), nil
}

// The handler answers in the protocol halite-ext-signer-local does, and
// a signature it returns verifies against the public key it reports.
func TestTheHandlerSignsAndReportsItsKey(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h := &handler{key: localKey{key}}
	digest := sha256.Sum256([]byte("a job"))

	got, err := call(t, h, "sign", map[string]string{"digest": base64.StdEncoding.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := base64.StdEncoding.DecodeString(got["signature"].(string))
	if !ecdsa.VerifyASN1(&key.PublicKey, digest[:], sig) {
		t.Error("the signature does not verify")
	}
	got, err = call(t, h, "public_key", nil)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := base64.StdEncoding.DecodeString(got["public_key"].(string))
	pub, err := x509.ParsePKIXPublicKey(der)
	if err != nil || !pub.(*ecdsa.PublicKey).Equal(&key.PublicKey) {
		t.Errorf("public_key answered %v, %v", pub, err)
	}
}

// A digest that is not 32 bytes is refused before it reaches KMS, and so
// is an unknown function.
func TestTheHandlerRefusesWhatItCannotSign(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h := &handler{key: localKey{key}}
	if _, err := call(t, h, "sign", map[string]string{"digest": base64.StdEncoding.EncodeToString(make([]byte, 48))}); err == nil ||
		!strings.Contains(err.Error(), "48 bytes") {
		t.Errorf("a 48-byte digest gave %v", err)
	}
	if _, err := call(t, h, "sign", map[string]string{}); err == nil {
		t.Error("a sign with no digest was answered")
	}
	if _, err := call(t, h, "decrypt", nil); err == nil {
		t.Error("an unknown function was answered")
	}
}

// fakeKMS answers GetPublicKey and Sign for one key: enough of the
// service for the binary to start and sign. internal/awskms's own tests
// hold the client to the API reference; this is a stand-in for the
// service, and TestLiveKMSJobSignature holds the binary to the real one.
func fakeKMS(t *testing.T, curve elliptic.Curve) (*httptest.Server, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(curve, rand.Reader)
	spec, algo := "ECC_NIST_P256", "ECDSA_SHA_256"
	if curve == elliptic.P384() {
		spec, algo = "ECC_NIST_P384", "ECDSA_SHA_384"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Message string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch r.Header.Get("X-Amz-Target") {
		case "TrentService.GetPublicKey":
			der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"KeyId": "arn:aws:kms:us-west-2:111122223333:key/test", "PublicKey": base64.StdEncoding.EncodeToString(der),
				"KeySpec": spec, "KeyUsage": "SIGN_VERIFY", "SigningAlgorithms": []string{algo},
			})
		case "TrentService.Sign":
			msg, _ := base64.StdEncoding.DecodeString(req.Message)
			sig, _ := ecdsa.SignASN1(rand.Reader, key, msg)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"KeyId": "arn:aws:kms:us-west-2:111122223333:key/test", "Signature": base64.StdEncoding.EncodeToString(sig),
				"SigningAlgorithm": algo,
			})
		default:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"__type":"UnknownOperationException"}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, key
}

func buildExtension(t *testing.T) string {
	t.Helper()
	name := "halite-ext-signer-kms"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	exe := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", exe, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("building the extension: %v", err)
	}
	return exe
}

func kmsEnv(endpoint string) []string {
	return append(os.Environ(),
		"HALITE_EXT_SIGNER_KMS_KEY=alias/halite-jobs", "AWS_REGION=us-west-2",
		"AWS_ENDPOINT_URL_KMS="+endpoint,
		"AWS_ACCESS_KEY_ID=AKIDEXAMPLE", "AWS_SECRET_ACCESS_KEY=secret", "AWS_SESSION_TOKEN=")
}

// The real binary, over the real bridge, against a stand-in for KMS: the
// public key it reports is the service's, and a job digest it signs
// passes jobsign.Verify, which is what a node with
// require_job_signature runs.
func TestTheExtensionSignsAJobThroughTheBridge(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	srv, key := fakeKMS(t, elliptic.P256())
	ctx := context.Background()
	proc, err := bridge.Start(ctx, bridge.Options{
		Path: buildExtension(t), Kind: ext.KindSigner, WorkDir: t.TempDir(), Env: kmsEnv(srv.URL),
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
	if !pub.Equal(&key.PublicKey) {
		t.Error("the reported public key is not the service's")
	}
	payload := jobsign.Payload{JID: "20261010000000000000", Target: "web*", TargetKind: "glob",
		Fun: "test.ping", Env: "base", Expires: time.Now().Add(time.Minute)}
	sig, err := signer.Sign(ctx, jobsign.Digest(payload))
	if err != nil {
		t.Fatal(err)
	}
	name, err := jobsign.Verify([]jobsign.SignerKey{{Name: "kms", Key: pub}}, payload,
		base64.StdEncoding.EncodeToString(sig))
	if err != nil || name != "kms" {
		t.Errorf("a node would refuse it: %q, %v", name, err)
	}
}

// A key that cannot sign a SHA-256 digest, and no key at all, stop the
// extension at startup, naming why, rather than at the first job.
func TestTheExtensionRefusesAtStartup(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	exe := buildExtension(t)
	srv, _ := fakeKMS(t, elliptic.P384())
	for name, env := range map[string][]string{
		"a P-384 key": kmsEnv(srv.URL),
		"no key":      append(kmsEnv(srv.URL), "HALITE_EXT_SIGNER_KMS_KEY="),
	} {
		cmd := exec.Command(exe)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("%s: the extension started", name)
			continue
		}
		if !strings.Contains(string(out), "halite-ext-signer-kms:") {
			t.Errorf("%s: the refusal does not say who refused: %s", name, out)
		}
		t.Logf("%s: %s", name, strings.TrimSpace(string(out)))
	}
}
