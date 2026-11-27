package transport

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
)

// issueFrom issues a node certificate from ca with the given lifetime, which
// may be negative, for a certificate that has already expired.
func issueFrom(t *testing.T, ca *pki.CA, nodeID string, lifetime time.Duration) tls.Certificate {
	t.Helper()
	key, _ := pki.GenerateKey(pki.ECDSAP256)
	csrDER, _ := pki.NewNodeCSR(key, nodeID)
	csr, err := pki.DecodeCSR(pki.EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}
	der, err := ca.IssueNode(csr, nodeID, lifetime)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, _ := pki.EncodeKey(key)
	pair, err := tls.X509KeyPair(pki.EncodeCert(der), keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

// lostAlert is what Go's HTTP/2 client says, with no cause attached, when
// the alert arrives after the client's half of a TLS 1.3 handshake has
// finished and the connection dies before the client reads it. Measured: 40
// of 1,200 refused handshakes in 300 runs of the test below, mostly the expired
// case, once the revoked one. A node that gets it retries, and the next
// attempt reads the alert.
const lostAlert = "http2: client conn could not be established"

// lostTheAlert reports an error that carries no word of why the handshake
// ended. Besides HTTP/2's own message, the client's next write can meet the
// server's close before the alert is read, and that write fails in one of
// two ways depending on how far the close has got:
//
//   - "connection reset by peer", seen once for the expired-certificate case
//     on CI's fips-test leg (PR 846);
//   - "write: broken pipe" (EPIPE), seen for the same case on ubuntu-24.04
//     in PR 863's CI, and once in 1,000 runs under -race on macOS. The
//     helper knew the reset and not this, so the case failed on an error
//     that says no more about the certificate than the reset does.
//     DIVERGENCE 5.740.
//
// A node retries all three, because CertificateRefused reads only a remote
// alert and none of them is one, and so does this helper, until the alert
// itself comes. The test below holds that CertificateRefused says no to
// each, so that retrying here is what a node does rather than a kindness
// the test does itself.
//
// On Windows the same two arrive as WSAECONNRESET ("An existing connection
// was forcibly closed by the remote host") and WSAECONNABORTED ("An
// established connection was aborted by the software in your host
// machine"), and this did not know them, so on Windows the case failed on
// the first lost alert instead of retrying it (issue 875). They are matched
// by number, because Windows words them in the machine's own language.
func lostTheAlert(err error) bool {
	return strings.Contains(err.Error(), lostAlert) ||
		strings.Contains(err.Error(), "connection reset by peer") ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, wsaeConnAborted) || errors.Is(err, wsaeConnReset)
}

// Winsock's numbers for the two, as syscall.Errno so that they compare with
// what a Windows read or write returns. On unix nothing carries them.
const (
	wsaeConnAborted = syscall.Errno(10053)
	wsaeConnReset   = syscall.Errno(10054)
)

// getWith makes one request on a fresh client, again while the alert was
// lost, so that each case is checked against the alert it gets rather than
// against the race.
func getWith(cfg *tls.Config, url string) error {
	var err error
	for attempt := 0; attempt < 20; attempt++ {
		client := &http.Client{Transport: newHubTransport(cfg)}
		var res *http.Response
		res, err = client.Get(url)
		if err == nil {
			res.Body.Close()
			return nil
		}
		if !lostTheAlert(err) {
			return err
		}
	}
	return err
}

// A node exits on CertificateRefused and retries on everything else, so the
// line between them is drawn here against what a real Go TLS server with the
// hub's own configuration sends in each case, rather than against alerts
// written out by hand (issue 839). Each case also checks the error text it
// expects, so that a case which stopped producing the failure it is named for
// fails rather than passing on some other error.
func TestOnlyARevokedOrUnacceptableCertificateIsRefusedForGood(t *testing.T) {
	hub := newFixture(t, http.HandlerFunc(echoIdentity))
	other := newFixture(t, http.HandlerFunc(echoIdentity))

	revoked, serial := hub.enrolledPair(t, "revoked.example")
	hub.denied.Revoke(serial, "decommissioned")

	stopped, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	stoppedURL := "https://" + stopped.Addr().String()
	stopped.Close()

	cases := []struct {
		name    string
		cfg     *tls.Config
		url     string
		text    string
		refused bool
	}{
		{"a revoked certificate", ClientConfig(revoked, hub.ca.Cert, "localhost"), hub.url,
			"remote error: tls: bad certificate", true},
		{"a certificate from a CA the hub does not hold",
			ClientConfig(issueFrom(t, other.ca, "stranger.example", time.Hour), hub.ca.Cert, "localhost"), hub.url,
			"remote error: tls: unknown certificate authority", false},
		{"an expired certificate",
			ClientConfig(issueFrom(t, hub.ca, "late.example", -30*time.Second), hub.ca.Cert, "localhost"), hub.url,
			"remote error: tls: expired certificate", false},
		{"a hub whose CA is not the one this node pinned",
			ClientConfig(issueFrom(t, other.ca, "moved.example", time.Hour), other.ca.Cert, "localhost"), hub.url,
			"certificate signed by unknown authority", false},
		// "refused", not "connection refused": Windows words the same
		// failure "No connection could be made because the target machine
		// actively refused it".
		{"a hub that is down",
			ClientConfig(issueFrom(t, hub.ca, "patient.example", time.Hour), hub.ca.Cert, "localhost"), stoppedURL,
			"refused", false},
	}
	// The lost alert is not a refusal: it carries nothing that says which
	// failure it was, so the node must retry it. Each shape of it is built as
	// the client returns it, and lostTheAlert must recognise each too, or
	// getWith would hand the case a race instead of an alert.
	for name, lost := range map[string]error{
		"http2":           errors.New(lostAlert),
		"reset":           &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("read", syscall.ECONNRESET)},
		"pipe":            &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)},
		"wsaeconnreset":   &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("wsarecv", wsaeConnReset)},
		"wsaeconnaborted": &net.OpError{Op: "read", Net: "tcp", Err: os.NewSyscallError("wsarecv", wsaeConnAborted)},
	} {
		err := &url.Error{Op: "Get", URL: hub.url, Err: lost}
		if CertificateRefused(err) {
			t.Errorf("a lost alert (%s: %v) was read as a refusal", name, err)
		}
		if !lostTheAlert(err) {
			t.Errorf("getWith would not retry a lost alert (%s: %v)", name, err)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := getWith(c.cfg, c.url)
			if err == nil {
				t.Fatal("the request succeeded")
			}
			if !strings.Contains(err.Error(), c.text) {
				t.Fatalf("the error is %q, which is not the failure this case is about (%q)", err, c.text)
			}
			if got := CertificateRefused(err); got != c.refused {
				t.Errorf("CertificateRefused(%q) = %v, want %v", err, got, c.refused)
			}
		})
	}

	// The control: the same hub accepts a certificate it issued, so the
	// failures above are each case's and not the fixture's.
	if err := getWith(ClientConfig(issueFrom(t, hub.ca, "fine.example", time.Hour), hub.ca.Cert, "localhost"), hub.url); err != nil {
		t.Fatalf("a valid certificate was refused: %v", err)
	}
}

// Issue 875: the hub refused a certificate and the node did not hear why.
// The alert was lost to a reset when the hub closed with the client's
// HTTP/2 preface unread, or was read and then thrown away by net/http's
// HTTP/2 client. A node retries both, so the test above, which retries too,
// passed over the loss rather than showing it. This one takes each attempt
// as a node's first: every refused handshake must report its own alert, on
// a fresh client each time, with no retry.
//
// Before the fix, on Windows 11, the expired case lost 329 of 1,500 and the
// other two about 1% each (DIVERGENCE 5.755).
func TestEveryRefusedHandshakeReportsItsAlertTheFirstTime(t *testing.T) {
	hub := newFixture(t, http.HandlerFunc(echoIdentity))
	other := newFixture(t, http.HandlerFunc(echoIdentity))
	revoked, serial := hub.enrolledPair(t, "revoked.example")
	hub.denied.Revoke(serial, "decommissioned")

	cases := []struct {
		name string
		cfg  *tls.Config
		text string
	}{
		{"a revoked certificate", ClientConfig(revoked, hub.ca.Cert, "localhost"),
			"remote error: tls: bad certificate"},
		{"a certificate from a CA the hub does not hold",
			ClientConfig(issueFrom(t, other.ca, "stranger.example", time.Hour), hub.ca.Cert, "localhost"),
			"remote error: tls: unknown certificate authority"},
		{"an expired certificate",
			ClientConfig(issueFrom(t, hub.ca, "late.example", -30*time.Second), hub.ca.Cert, "localhost"),
			"remote error: tls: expired certificate"},
	}
	const attempts = 300
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lost := map[string]int{}
			for i := 0; i < attempts; i++ {
				err := refusedOnce(c.cfg, hub.url)
				if err == nil {
					t.Fatal("the request succeeded")
				}
				if msg := err.Error(); !strings.Contains(msg, c.text) {
					// The operation and the part after the addresses, so that
					// one cause is one line however many ports it was seen on.
					op := "?"
					var oe *net.OpError
					if errors.As(err, &oe) {
						op = oe.Op
					}
					lost[op+": "+msg[strings.LastIndex(msg, ": ")+2:]]++
				}
			}
			n := 0
			for _, k := range lost {
				n += k
			}
			if n > 0 {
				t.Errorf("%d of %d refused handshakes did not report %q:", n, attempts, c.text)
				for msg, k := range lost {
					t.Errorf("  %d x %s", k, msg)
				}
			}
		})
	}
}

// refusedOnce makes one request on a fresh client, and makes it again only
// when the dial failed, which is an attempt that never reached the hub.
// Draining leaves each refused connection in TIME_WAIT, and on Windows 11,
// with this test run back to back beside a full suite, the machine ran out
// of local ports: WSAENOBUFS, 2 of 29,700 attempts. That is not a lost alert,
// and anything that fails after the dial is still returned.
func refusedOnce(cfg *tls.Config, url string) error {
	var err error
	for try := 0; try < 5; try++ {
		client := &http.Client{Transport: newHubTransport(cfg)}
		var res *http.Response
		res, err = client.Get(url)
		if err == nil {
			res.Body.Close()
			return nil
		}
		var op *net.OpError
		if !errors.As(err, &op) || op.Op != "dial" {
			return err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}
