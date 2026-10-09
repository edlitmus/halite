package awskms

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/awsauth"
)

// fakeKMS answers GetPublicKey and Sign for one key, the way the API
// reference describes the service doing it.
//
// Written from the reference and not from a capture: see the package
// comment, and TestLiveKMSSignsADigestThisBuildVerifies for the test
// that holds the client to the service rather than to this.
type fakeKMS struct {
	key    *ecdsa.PrivateKey
	arn    string
	spec   string
	usage  string
	algos  []string
	server *httptest.Server

	mu       sync.Mutex
	signs    int
	unsigned bool
	targets  []string
	// tamper, when set, rewrites the signature before it is returned.
	tamper func([]byte) []byte
	// Message records what the last Sign was asked to sign.
	message []byte
	msgType string
	algo    string
}

func newFakeKMS(t *testing.T, curve elliptic.Curve) *fakeKMS {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeKMS{
		key:   key,
		arn:   "arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab",
		spec:  "ECC_NIST_P256",
		usage: "SIGN_VERIFY",
		algos: []string{"ECDSA_SHA_256"},
	}
	if curve == elliptic.P384() {
		f.spec, f.algos = "ECC_NIST_P384", []string{"ECDSA_SHA_384"}
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeKMS) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), awsauth.Algorithm+" ") {
		f.unsigned = true
		w.WriteHeader(http.StatusForbidden)
		return
	}
	target := r.Header.Get("X-Amz-Target")
	f.targets = append(f.targets, target)
	if r.Header.Get("Content-Type") != "application/x-amz-json-1.1" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var req struct {
		KeyID            string `json:"KeyId"`
		Message          string `json:"Message"`
		MessageType      string `json:"MessageType"`
		SigningAlgorithm string `json:"SigningAlgorithm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.KeyID != "alias/halite-release" && req.KeyID != f.arn {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"NotFoundException","message":"Alias ` + req.KeyID + ` is not found."}`))
		return
	}
	switch target {
	case "TrentService.GetPublicKey":
		der, _ := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"KeyId":             f.arn,
			"PublicKey":         base64.StdEncoding.EncodeToString(der),
			"KeySpec":           f.spec,
			"KeyUsage":          f.usage,
			"SigningAlgorithms": f.algos,
		})
	case "TrentService.Sign":
		msg, _ := base64.StdEncoding.DecodeString(req.Message)
		f.message, f.msgType, f.algo = msg, req.MessageType, req.SigningAlgorithm
		f.signs++
		sig, err := ecdsa.SignASN1(rand.Reader, f.key, msg)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if f.tamper != nil {
			sig = f.tamper(sig)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"KeyId":            f.arn,
			"Signature":        base64.StdEncoding.EncodeToString(sig),
			"SigningAlgorithm": req.SigningAlgorithm,
		})
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"UnknownOperationException"}`))
	}
}

func (f *fakeKMS) client(t *testing.T) *Client {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "AKID")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	return &Client{KeyID: "alias/halite-release", Region: "us-east-1", Endpoint: f.server.URL}
}

func TestSignProducesADERSignatureOverTheDigestItWasGiven(t *testing.T) {
	f := newFakeKMS(t, elliptic.P256())
	c := f.client(t)
	digest := sha256.Sum256([]byte("an artifact"))

	der, err := c.Sign(context.Background(), digest)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.VerifyASN1(&f.key.PublicKey, digest[:], der) {
		t.Fatal("the signature does not verify against the key the fake signed with")
	}
	if f.unsigned {
		t.Error("a request reached the service without a SigV4 signature")
	}
	if string(f.message) != string(digest[:]) || f.msgType != "DIGEST" || f.algo != "ECDSA_SHA_256" {
		t.Errorf("the service was asked to sign %x as %s with %s; want the digest as DIGEST with ECDSA_SHA_256",
			f.message, f.msgType, f.algo)
	}
	// The public key is fetched once and the signature once; a second
	// signature is one more call, not two.
	if _, err := c.Sign(context.Background(), digest); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(f.targets, " "), "TrentService.GetPublicKey TrentService.Sign TrentService.Sign"; got != want {
		t.Errorf("calls were %q, want %q", got, want)
	}
}

// The property the package comment promises: a signature the service
// returns is checked before it is handed back, so a wrong answer is an
// error here and not a .sig file that nothing verifies.
func TestSignRefusesASignatureThatDoesNotVerify(t *testing.T) {
	f := newFakeKMS(t, elliptic.P256())
	f.tamper = func(sig []byte) []byte {
		// Flip a bit in the middle of the DER, which stays well-formed
		// often enough to reach the verification rather than the parse.
		out := append([]byte(nil), sig...)
		out[len(out)/2] ^= 0x01
		return out
	}
	c := f.client(t)
	digest := sha256.Sum256([]byte("an artifact"))
	_, err := c.Sign(context.Background(), digest)
	if err == nil || !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("a tampered signature was returned; err = %v", err)
	}
}

func TestPublicKeyCarriesTheARNSpecAndDER(t *testing.T) {
	f := newFakeKMS(t, elliptic.P256())
	c := f.client(t)
	pub, err := c.PublicKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pub.ARN != f.arn || pub.Spec != "ECC_NIST_P256" || pub.Usage != "SIGN_VERIFY" {
		t.Errorf("got %+v", pub)
	}
	want, _ := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	if string(pub.DER) != string(want) {
		t.Error("the DER is not what the service returned")
	}
	if !pub.Key.Equal(&f.key.PublicKey) {
		t.Error("the parsed key is not the fake's")
	}
	if !strings.HasPrefix(pub.Fingerprint(), "SHA256:") || len(pub.Fingerprint()) != 7+64 {
		t.Errorf("fingerprint %q", pub.Fingerprint())
	}

	// The PEM round-trips through ParsePEM to the identical DER, which
	// is what lets a committed public key file be held to the service's
	// answer byte for byte.
	back, err := ParsePEM(pub.PEM())
	if err != nil {
		t.Fatal(err)
	}
	if string(back.DER) != string(pub.DER) {
		t.Error("PEM did not round-trip to the same DER")
	}
}

// A P-384 key signs only SHA-384 digests at KMS, and everything this
// package signs is SHA-256. Refused by name, up front, rather than by
// the service's message about message length.
func TestAP384KeyIsRefusedByName(t *testing.T) {
	f := newFakeKMS(t, elliptic.P384())
	c := f.client(t)
	_, err := c.PublicKey(context.Background())
	if err == nil || !strings.Contains(err.Error(), "P-384") || !strings.Contains(err.Error(), "P-256 only") {
		t.Fatalf("err = %v", err)
	}
	if f.signs != 0 {
		t.Error("a signature was requested from a key that cannot produce the right one")
	}
}

func TestAKeyThatCannotSignIsRefusedUpFront(t *testing.T) {
	f := newFakeKMS(t, elliptic.P256())
	f.usage = "ENCRYPT_DECRYPT"
	c := f.client(t)
	_, err := c.Sign(context.Background(), sha256.Sum256(nil))
	if err == nil || !strings.Contains(err.Error(), "ENCRYPT_DECRYPT") {
		t.Fatalf("err = %v", err)
	}
	if f.signs != 0 {
		t.Error("Sign was called on a key the service said cannot sign")
	}
}

// The service's own message is what an operator debugging a wrong
// alias, a wrong account or a missing permission needs, so it is
// carried through rather than flattened to a status code.
func TestTheServicesErrorMessageIsReported(t *testing.T) {
	f := newFakeKMS(t, elliptic.P256())
	c := f.client(t)
	c.KeyID = "alias/no-such-key"
	_, err := c.PublicKey(context.Background())
	if err == nil {
		t.Fatal("no error for a key the service does not have")
	}
	for _, want := range []string{"GetPublicKey", "400", "NotFoundException", "alias/no-such-key is not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestTheEndpointFollowsTheRegionAndPartition(t *testing.T) {
	cases := []struct {
		region, partition, want string
	}{
		{"us-east-1", "", "https://kms.us-east-1.amazonaws.com"},
		{"us-gov-west-1", "aws-us-gov", "https://kms.us-gov-west-1.amazonaws.com"},
		{"cn-north-1", "aws-cn", "https://kms.cn-north-1.amazonaws.com.cn"},
	}
	for _, tc := range cases {
		c := &Client{Region: tc.region, Partition: tc.partition}
		if got := c.endpoint(); got != tc.want {
			t.Errorf("%s/%s: %s, want %s", tc.partition, tc.region, got, tc.want)
		}
	}
}

func TestAMissingRegionOrKeyIsRefusedBeforeAnyRequest(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKID")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	c := &Client{KeyID: "alias/halite-release", Endpoint: "http://127.0.0.1:1"}
	if _, err := c.PublicKey(context.Background()); err == nil || !strings.Contains(err.Error(), "regional") {
		t.Errorf("no region: err = %v", err)
	}
	c = &Client{Region: "us-east-1", Endpoint: "http://127.0.0.1:1"}
	if _, err := c.PublicKey(context.Background()); err == nil || !strings.Contains(err.Error(), "key id") {
		t.Errorf("no key: err = %v", err)
	}
}

func TestParsePEMRefusesWhatIsNotOneP256PublicKey(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	good := (&PublicKey{DER: der}).PEM()

	if _, err := ParsePEM([]byte("not pem")); err == nil {
		t.Error("accepted text that is not PEM")
	}
	if _, err := ParsePEM(append(append([]byte(nil), good...), good...)); err == nil {
		t.Error("accepted a file holding two keys")
	}
	priv, _ := x509.MarshalECPrivateKey(key)
	if _, err := ParsePEM((&PublicKey{DER: priv}).PEM()); err == nil {
		t.Error("accepted a private key labelled as public")
	}
	if _, err := ParsePEM(good); err != nil {
		t.Errorf("refused a good key: %v", err)
	}
}
