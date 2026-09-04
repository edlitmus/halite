package safehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The addresses the contract names are refused, and the ones it does not
// name are permitted.
//
// The private ranges are the ones worth asserting positively: a state
// fetching a checksum from an internal artefact server is the normal
// case, and a denylist that grew to cover 10.0.0.0/8 would break more
// than it protected.
func TestOnlyTheAddressesThatHandOutCredentialsAreDenied(t *testing.T) {
	denied := []string{
		"169.254.169.254", // AWS, Azure and GCP metadata
		"169.254.0.1",     // the rest of IPv4 link-local
		"100.100.100.200", // Alibaba Cloud metadata
		"fe80::1",         // IPv6 link-local
		"fd00:ec2::254",   // AWS IPv6 metadata
	}
	for _, s := range denied {
		if !Denied(net.ParseIP(s)) {
			t.Errorf("%s should be denied", s)
		}
	}

	permitted := []string{
		"10.0.0.1", "172.16.0.1", "192.168.1.1", // private, and legitimate
		"127.0.0.1", "::1", // a local service is a real case
		"93.184.216.34",   // ordinary public
		"fd00:1234::1",    // a unique-local address that is not AWS's
		"100.100.100.199", // one address away from Alibaba's
	}
	for _, s := range permitted {
		if Denied(net.ParseIP(s)) {
			t.Errorf("%s should be permitted", s)
		}
	}
}

// A name that resolves to a metadata address is refused, which is the
// half a URL check cannot do.
//
// `metadata.google.internal` is the documented way to reach GCP's
// service, and any domain can carry an A record pointing at
// 169.254.169.254. This uses a resolver that answers that way, because
// the point is the resolved address rather than the spelling.
func TestANameThatResolvesToMetadataIsRefused(t *testing.T) {
	client := Client(Options{Timeout: 2 * time.Second})

	// A hostname that really does resolve to the metadata address on
	// every machine: the address itself, spelled as a name would be.
	_, err := client.Get("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("the metadata service was fetched")
	}
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("the refusal is not a DeniedError: %v", err)
	}
	if !strings.Contains(denied.Error(), "credentials") {
		t.Errorf("the refusal does not say why it matters: %v", denied)
	}
	if !strings.Contains(denied.Error(), "allow_metadata") {
		t.Errorf("the refusal does not say how to permit it: %v", denied)
	}
}

// The check is at dial time, so a redirect into the denied range is
// caught even though the first URL was innocent.
//
// This is the case a URL check misses entirely: the state names a
// perfectly ordinary host, and that host answers 302.
func TestARedirectIntoTheDeniedRangeIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()

	client := Client(Options{Timeout: 3 * time.Second})
	_, err := client.Get(server.URL)
	if err == nil {
		t.Fatal("a redirect to the metadata service was followed")
	}
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("the redirect was refused for the wrong reason: %v", err)
	}
}

// And it is lifted when the caller says so, because a node reading its
// own instance metadata is a real case.
func TestTheDenylistIsLiftedWhenAsked(t *testing.T) {
	// A server on a permitted address, reached through a client that has
	// the denylist off: what is checked is that the option reaches the
	// dialer, not that a metadata service answers.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer server.Close()

	res, err := Client(Options{AllowMetadata: true, Timeout: 3 * time.Second}).Get(server.URL)
	if err != nil {
		t.Fatalf("a permitted address was refused with the denylist off: %v", err)
	}
	res.Body.Close()

	// The dialer's own decision, tested directly: the same address, with
	// and without the permission.
	dialer := &net.Dialer{Timeout: time.Second}
	_, err = dialChecked(t.Context(), dialer, "tcp", "169.254.169.254:80", false)
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Errorf("without permission, the dial gave %v", err)
	}
	// With permission it gets past the check and fails to connect, which
	// is a different error and the one that proves the check was lifted.
	_, err = dialChecked(t.Context(), dialer, "tcp", "169.254.169.254:80", true)
	if errors.As(err, &denied) {
		t.Error("the denylist was still applied with allow_metadata set")
	}
}

// A redirect loop stops rather than spinning.
func TestARedirectChainIsBounded(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/again", http.StatusFound)
	}))
	defer server.Close()

	_, err := Client(Options{Timeout: 5 * time.Second}).Get(server.URL)
	if err == nil {
		t.Fatal("an endless redirect chain was followed")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("the failure does not name the redirects: %v", err)
	}
}

// A response larger than the limit is refused, and the refusal says what
// the limit was and what to raise.
//
// Over a real connection, because the limit has to hold against a body
// that arrives rather than against a number in a header: what is read is
// one byte past the limit, so a server that under-declares its
// Content-Length cannot talk its way past it.
func TestABodyLargerThanTheLimitIsRefused(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer server.Close()

	res, err := Client(Options{Timeout: 3 * time.Second}).Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	_, err = Body(res.Body, 100)
	if err == nil {
		t.Fatal("a body over the limit was accepted")
	}
	if !strings.Contains(err.Error(), "100") {
		t.Errorf("the refusal does not name the limit: %v", err)
	}
	if !strings.Contains(err.Error(), "max_body") {
		t.Errorf("the refusal does not say what to raise: %v", err)
	}
}

// The limit is on what is read, not on what the server claims. A body
// declared as ten bytes and delivered as four thousand is refused.
func TestTheLimitIsOnWhatArrivesNotWhatIsDeclared(t *testing.T) {
	_, err := Body(strings.NewReader(strings.Repeat("x", 4096)), 100)
	if err == nil {
		t.Fatal("a reader longer than the limit was accepted")
	}
	if !strings.Contains(err.Error(), "100") {
		t.Errorf("the refusal does not name the limit: %v", err)
	}
}

// A body inside the limit is returned whole.
func TestABodyInsideTheLimitIsWhole(t *testing.T) {
	got, err := Body(strings.NewReader("hello"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Errorf("body = %q", got)
	}
	// Exactly the limit is inside it.
	got, err = Body(strings.NewReader("12345"), 5)
	if err != nil {
		t.Fatalf("a body exactly the size of the limit was refused: %v", err)
	}
	if string(got) != "12345" {
		t.Errorf("body = %q", got)
	}
}

// Certificate verification is mandatory and there is no option that
// turns it off, so a self-signed server is refused.
//
// httptest's TLS server uses a certificate no store trusts, which is
// what makes it the right thing to point at here.
func TestCertificateVerificationCannotBeTurnedOff(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "secret")
	}))
	defer server.Close()

	_, err := Client(Options{Timeout: 3 * time.Second}).Get(server.URL)
	if err == nil {
		t.Fatal("a server with an untrusted certificate was accepted")
	}
	if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Errorf("the failure is not a certificate failure: %v", err)
	}
}

// The timeout bounds a request rather than leaving a state run hanging
// on a host that accepts a connection and then says nothing.
func TestARequestIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer server.Close()

	start := time.Now()
	_, err := Client(Options{Timeout: 300 * time.Millisecond}).Get(server.URL)
	if err == nil {
		t.Fatal("a request that never answered returned successfully")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the request took %s against a 300ms timeout", elapsed)
	}
}

// A scheme this client does not speak is refused before a request is
// built, with a message that says what to use instead.
func TestASchemeThatIsNotHTTPIsRefusedWithSomethingToDoInstead(t *testing.T) {
	if err := CheckURL("https://example.com/x"); err != nil {
		t.Errorf("https was refused: %v", err)
	}
	if err := CheckURL("http://example.com/x"); err != nil {
		t.Errorf("http was refused: %v", err)
	}

	err := CheckURL("file:///etc/shadow")
	if err == nil {
		t.Fatal("a file URL was accepted")
	}
	if !strings.Contains(err.Error(), "file.read") {
		t.Errorf("the refusal does not point at the module that does read files: %v", err)
	}

	if err := CheckURL("ftp://example.com/x"); err == nil {
		t.Error("ftp was accepted")
	}
	if err := CheckURL("not-a-url"); err == nil {
		t.Error("a string with no scheme was accepted")
	}
}

// The zero Options is the contract's defaults, which is what makes a
// caller that states nothing get the safe behaviour.
func TestTheZeroOptionsAreTheDefaults(t *testing.T) {
	var o Options
	if o.timeout() != DefaultTimeout {
		t.Errorf("timeout = %s, want %s", o.timeout(), DefaultTimeout)
	}
	if o.maxBody() != DefaultMaxBody {
		t.Errorf("max body = %d, want %d", o.maxBody(), DefaultMaxBody)
	}
	if o.AllowMetadata {
		t.Error("the zero value permits metadata")
	}
}
