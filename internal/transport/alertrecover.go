package transport

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"
)

// newHubTransport is the round tripper a node reaches the hub through. It is
// a function of its own so that the refusal tests use the same one, rather
// than an http.Transport of their own that would not show what a node sees.
func newHubTransport(cfg *tls.Config) http.RoundTripper {
	return alertRecovering{next: &http.Transport{
		TLSClientConfig:     cfg,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: HandshakeTimeout,
		DialContext:         dialDraining,
		// The subscribe stream is long-lived by design, so there
		// is no response header timeout here; the per-request
		// deadline comes from the context instead.
		IdleConnTimeout: IdleStreamTimeout,
	}}
}

// http2NotEstablished is the text of net/http's
// http2errClientConnNotEstablished (net/http/h2_bundle.go), which is
// unexported, so the text is all there is to match.
const http2NotEstablished = "http2: client conn could not be established"

// alertRecovering puts back the TLS alert that net/http's HTTP/2 client
// throws away (issue 875).
//
// When the hub refuses a node's certificate, the alert arrives after the
// client's half of a TLS 1.3 handshake has finished, so it is the HTTP/2 read
// loop that reads it. The loop closes the connection, and if that happens
// before the first request goes out, RoundTrip returns
// http2errClientConnNotEstablished -- "could not be established", with the
// alert gone. CertificateRefused and Rejected read only the alert, so a
// revoked node saw neither. With Go's HTTP/2 debug log on, 8 of 15 such
// errors in 1,500 refused handshakes had the alert behind them (DIVERGENCE
// 5.755); the other 7 were the reset DrainingListener stops.
//
// The alert is still there to be had. tls.Conn keeps the first error a read
// met and returns it from every later Read without touching the socket
// (halfConn.setErrorLocked), and httptrace's GotConn names the connection
// before the request is attempted. So on that one error, this reads the
// connection once and, if what it kept is a remote alert, returns that,
// with net/http's text beside it. Anything else is left as net/http said it.
type alertRecovering struct{ next http.RoundTripper }

func (t alertRecovering) RoundTrip(req *http.Request) (*http.Response, error) {
	var conn atomic.Pointer[tls.Conn]
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if tc, ok := info.Conn.(*tls.Conn); ok {
			conn.Store(tc)
		}
	}}
	res, err := t.next.RoundTrip(req.WithContext(httptrace.WithClientTrace(req.Context(), trace)))
	if err == nil || !strings.Contains(err.Error(), http2NotEstablished) {
		return res, err
	}
	if alert := keptAlert(conn.Load()); alert != nil {
		return nil, fmt.Errorf("%w (net/http said only %q)", alert, err.Error())
	}
	return res, err
}

// CloseIdleConnections passes through, for the reason tracePropagating's does.
func (t alertRecovering) CloseIdleConnections() {
	if c, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// keptAlert returns the remote alert tc's reads ended on, or nil. The
// deadline is so that a connection that kept no error, and is somehow still
// open, answers at once instead of waiting for data.
func keptAlert(tc *tls.Conn) error {
	if tc == nil {
		return nil
	}
	_ = tc.SetReadDeadline(time.Now())
	var one [1]byte
	_, err := tc.Read(one[:])
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "remote error" {
		return err
	}
	return nil
}
