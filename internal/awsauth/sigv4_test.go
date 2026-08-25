package awsauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The credentials AWS uses throughout its SigV4 documentation and test
// suite. They are published examples, not secrets.
var docCreds = Credentials{
	AccessKeyID:     "AKIDEXAMPLE",
	SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
}

// docTime is the timestamp every vector in AWS's suite is signed at.
var docTime = time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)

// AWS publishes the derived signing key for its example credentials.
// If this is wrong, nothing else can be right.
func TestTheSigningKeyMatchesTheDocumentedDerivation(t *testing.T) {
	key := signingKey(docCreds.SecretAccessKey, "20150830", "us-east-1", "iam")
	got := hexOf(key)
	const want = "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9"
	if got != want {
		t.Errorf("the signing key is %s, want %s", got, want)
	}
}

// AWS's `get-vanilla` vector, signed end to end.
func TestTheVanillaVectorSigns(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "example.amazonaws.com"

	signer := Signer{Region: "us-east-1", Service: "service"}
	if err := signer.Sign(req, docCreds, EmptyPayloadHash, docTime); err != nil {
		t.Fatal(err)
	}
	const want = "AWS4-HMAC-SHA256 " +
		"Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request, " +
		"SignedHeaders=host;x-amz-content-sha256;x-amz-date, " +
		"Signature="
	got := req.Header.Get("Authorization")
	if !strings.HasPrefix(got, want) {
		t.Fatalf("the header is\n%s\nwant it to start\n%s", got, want)
	}
	if req.Header.Get("X-Amz-Date") != "20150830T123600Z" {
		t.Errorf("the date is %q", req.Header.Get("X-Amz-Date"))
	}
}

// The canonical request is the part with the sharp edges, so it is
// checked against AWS's documented output directly rather than only
// through the signature.
func TestTheCanonicalRequestMatchesTheDocumentedForm(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "vanilla",
			url:  "https://example.amazonaws.com/",
			want: "GET\n/\n\n",
		},
		{
			// A query is sorted by name, and each part is encoded.
			name: "sorted query",
			url:  "https://example.amazonaws.com/?Param2=value2&Param1=value1",
			want: "GET\n/\nParam1=value1&Param2=value2\n",
		},
		{
			// A space in a value is `%20`, never `+`. Go's
			// `QueryEscape` produces `+`, which SigV4 refuses.
			name: "space in a value",
			url:  "https://example.amazonaws.com/?Param1=value%201",
			want: "GET\n/\nParam1=value%201\n",
		},
		{
			// The path is not re-encoded: a key holding a literal
			// `%2F` and a key holding `/` are different objects.
			name: "encoded slash in the path",
			url:  "https://example.amazonaws.com/a%2Fb",
			want: "GET\n/a%2Fb\n\n",
		},
		{
			name: "unreserved characters are not encoded",
			url:  "https://example.amazonaws.com/?a=-_.~AZaz09",
			want: "GET\n/\na=-_.~AZaz09\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, c.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = "example.amazonaws.com"
			canonical, _ := canonicalRequest(req, EmptyPayloadHash)
			if !strings.HasPrefix(canonical, c.want) {
				t.Errorf("the canonical request is\n%q\nwant it to start\n%q", canonical, c.want)
			}
		})
	}
}

// A session token that is not signed can be stripped in flight, and the
// signature still verifies.
func TestASessionTokenIsSigned(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "example.amazonaws.com"
	creds := docCreds
	creds.SessionToken = "a-temporary-token"

	signer := Signer{Region: "us-east-1", Service: "s3"}
	if err := signer.Sign(req, creds, EmptyPayloadHash, docTime); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.Header.Get("Authorization"), "x-amz-security-token") {
		t.Errorf("the token is not in the signed headers: %s", req.Header.Get("Authorization"))
	}
	if req.Header.Get("X-Amz-Security-Token") != "a-temporary-token" {
		t.Errorf("the token header is %q", req.Header.Get("X-Amz-Security-Token"))
	}
}

// The payload hash is signed, which is what stops a proxy altering a
// body in flight.
func TestThePayloadHashChangesTheSignature(t *testing.T) {
	sign := func(hash string) string {
		req, _ := http.NewRequest(http.MethodPut, "https://example.amazonaws.com/key", nil)
		req.Host = "example.amazonaws.com"
		signer := Signer{Region: "us-east-1", Service: "s3"}
		if err := signer.Sign(req, docCreds, hash, docTime); err != nil {
			t.Fatal(err)
		}
		return req.Header.Get("Authorization")
	}
	if sign(HexSHA256([]byte("one"))) == sign(HexSHA256([]byte("two"))) {
		t.Error("two different bodies produced the same signature")
	}
}

// A key derived for one day, region, or service cannot sign for
// another. That is what makes a leaked signature bounded rather than a
// credential.
func TestTheSigningKeyIsScoped(t *testing.T) {
	base := hexOf(signingKey("secret", "20260825", "us-east-1", "s3"))
	for _, other := range []string{
		hexOf(signingKey("secret", "20260826", "us-east-1", "s3")),
		hexOf(signingKey("secret", "20260825", "eu-west-1", "s3")),
		hexOf(signingKey("secret", "20260825", "us-east-1", "sts")),
		hexOf(signingKey("other", "20260825", "us-east-1", "s3")),
	} {
		if base == other {
			t.Error("two different scopes derived the same signing key")
		}
	}
}

// Every header on the request is signed. A header left out is one a
// proxy can add without breaking the signature.
func TestEveryHeaderIsSigned(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	req.Host = "example.amazonaws.com"
	req.Header.Set("X-Custom-Thing", "value")
	req.Header.Set("Content-Type", "text/plain")

	signer := Signer{Region: "us-east-1", Service: "s3"}
	if err := signer.Sign(req, docCreds, EmptyPayloadHash, docTime); err != nil {
		t.Fatal(err)
	}
	signed := req.Header.Get("Authorization")
	for _, want := range []string{"content-type", "x-custom-thing", "host"} {
		if !strings.Contains(signed, want) {
			t.Errorf("%s is not in the signed headers: %s", want, signed)
		}
	}
}

// Header values are canonicalised: runs of spaces collapse.
func TestHeaderValuesAreCanonicalised(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	req.Host = "example.amazonaws.com"
	req.Header.Set("X-Spaced", "  a   b  ")
	_, headers := canonicalHeaders(req)
	if !strings.Contains(headers, "x-spaced:a b\n") {
		t.Errorf("the header block is %q", headers)
	}
}

// Credentials that expire during the request they were fetched for are
// the failure a refresh exists to avoid.
func TestExpiringCredentialsAreRefusedEarly(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		expires time.Time
		valid   bool
	}{
		{"no expiry", time.Time{}, true},
		{"an hour away", now.Add(time.Hour), true},
		{"thirty seconds away", now.Add(30 * time.Second), false},
		{"already expired", now.Add(-time.Minute), false},
	}
	for _, c := range cases {
		creds := docCreds
		creds.Expires = c.expires
		if got := creds.Valid(now); got != c.valid {
			t.Errorf("%s: valid = %v, want %v", c.name, got, c.valid)
		}
	}
	var empty Credentials
	if empty.Valid(now) {
		t.Error("credentials with no key are valid")
	}
}

func TestSigningWithoutCredentialsIsRefused(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	if err := (Signer{Region: "us-east-1", Service: "s3"}).Sign(req, Credentials{}, "", docTime); err == nil {
		t.Error("a request was signed with no credentials")
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = digits[c>>4]
		out[i*2+1] = digits[c&0x0f]
	}
	return string(out)
}

// IMDSv1 is a plain GET on a link-local address, which any process on
// the instance — and any server-side request forgery in an application
// on it — can make. Falling back to it when v2 does not answer gives
// the hardening away for a convenience nobody asked for.
func TestIMDSv1IsNotUsedWhenV2Refuses(t *testing.T) {
	var sawToken, sawV1 bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/api/token") {
			sawToken = true
			// A service with IMDSv2 disabled answers 404 here.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sawV1 = true
		_, _ = w.Write([]byte("some-role"))
	}))
	defer server.Close()

	p := &Provider{
		Client:  server.Client(),
		Environ: func(string) string { return "" },
	}
	// Point the provider at the fake by overriding the transport's
	// destination: the address is a constant, so this drives the same
	// code path through a client whose transport rewrites the host.
	p.Client = &http.Client{Transport: rewriteTo(server.URL)}

	if _, err := p.Retrieve(context.Background()); err == nil {
		t.Fatal("credentials were retrieved with IMDSv2 refused")
	}
	if !sawToken {
		t.Error("no token request was made")
	}
	if sawV1 {
		t.Error("it fell back to IMDSv1 after v2 refused")
	}
}

// Explicit configuration wins over the environment, so an operator who
// sets a key is never surprised by an instance role.
func TestExplicitCredentialsWinOverTheEnvironment(t *testing.T) {
	p := &Provider{
		Explicit: Credentials{AccessKeyID: "explicit", SecretAccessKey: "s"},
		Environ:  func(string) string { return "from-environment" },
	}
	creds, err := p.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "explicit" {
		t.Errorf("it used %q", creds.AccessKeyID)
	}
}

func TestTheEnvironmentIsUsedWhenNothingIsConfigured(t *testing.T) {
	env := map[string]string{
		"AWS_ACCESS_KEY_ID":     "from-env",
		"AWS_SECRET_ACCESS_KEY": "secret",
		"AWS_SESSION_TOKEN":     "token",
	}
	p := &Provider{Environ: func(k string) string { return env[k] }}
	creds, err := p.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "from-env" || creds.SessionToken != "token" {
		t.Errorf("it resolved %+v", creds)
	}
}

// The STS endpoint is built from the partition rather than hardcoded:
// one built for the commercial partition is wrong in China.
func TestTheSTSEndpointComesFromThePartition(t *testing.T) {
	cases := map[string]string{
		"":           "https://sts.eu-west-1.amazonaws.com",
		"aws":        "https://sts.eu-west-1.amazonaws.com",
		"aws-cn":     "https://sts.eu-west-1.amazonaws.com.cn",
		"aws-us-gov": "https://sts.eu-west-1.amazonaws.com",
	}
	for partition, want := range cases {
		p := &Provider{Partition: partition, Region: "eu-west-1"}
		if got := p.stsEndpoint(); got != want {
			t.Errorf("%q gave %s, want %s", partition, got, want)
		}
	}
}

// rewriteTo sends every request to one host, so a constant address can
// be driven against a test server.
func rewriteTo(target string) http.RoundTripper {
	parsed, _ := url.Parse(target)
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		clone.URL.Scheme = parsed.Scheme
		clone.URL.Host = parsed.Host
		clone.Host = parsed.Host
		return http.DefaultTransport.RoundTrip(clone)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
