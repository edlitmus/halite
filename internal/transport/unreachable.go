package transport

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
)

// Rejected reports whether the hub refused this node's certificate during the
// handshake, which is how a revoked or deleted node hears about it when it was
// not connected at the moment of revocation.
//
// TLS 1.3 sends that alert after the hub has proved its identity, so a party
// who cannot pass verification cannot produce this from a verified handshake.
func Rejected(err error) bool {
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "remote error" {
		return false
	}
	msg := op.Err.Error()
	return strings.Contains(msg, "bad certificate") ||
		strings.Contains(msg, "certificate revoked") ||
		strings.Contains(msg, "unknown certificate")
}

// CertificateRefused reports whether the hub refused this node's certificate
// in a way no retry can change: the certificate is revoked, or is one the hub
// will never accept. It is narrower than Rejected, which decides whether to
// drop a cached pillar and can afford to be cautious; this decides whether the
// agent exits, and so whether its supervisor stops restarting it (issue 839).
//
// It is read from the TLS alert, because that is all a hub that refused the
// certificate sends: Go's server answers a failed VerifyPeerCertificate -- the
// hub's denylist -- with `bad certificate` and no reason, and there is no
// later answer to read. What else a Go server sends `bad certificate` for is
// also a property of the certificate rather than of the moment: a chain that
// verifies but not for client authentication, one the FIPS module refuses, a
// failed VerifyConnection (which the hub does not set; connlimit.go says why).
// None of them is fixed by asking again. `revoked certificate` is the explicit
// alert, which Go never sends but another TLS stack in front of a hub may.
//
// Two alerts are deliberately not here, because each can be put right on the
// hub or by a clock without touching the node: `unknown certificate
// authority`, a hub that does not hold the CA that issued this certificate (a
// rebuilt or wrongly restored hub), and `expired certificate`, which a clock
// that is wrong on either side also produces. A hub whose own certificate
// this node cannot verify -- its CA changed -- sends no alert at all: that is
// this node refusing the hub, and it is not here either. Nor is an outage: a
// refused dial, a reset, a timeout.
//
// TLS 1.3 sends the alert after the hub has proved its identity, so only a
// hub this node trusts can make it exit.
func CertificateRefused(err error) bool {
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "remote error" {
		return false
	}
	switch op.Err.Error() {
	case "tls: bad certificate", "tls: revoked certificate":
		return true
	}
	return false
}

// Unreachable reports whether a request failed because no hub answered.
//
// A node acts on this by running from what it last received, so the
// question is deliberately narrower than "did the request fail". A hub
// that answered and said no, a hub whose certificate did not verify, and
// a hub that rejected this node's certificate are all answers: running on
// from a cached pillar after any of them would let a revocation or an
// impostor look like an outage. So this is a list of what an outage looks
// like, and anything not on it is false, which leaves the caller failing
// out loud as it would have.
func Unreachable(err error) bool {
	if err == nil {
		return false
	}
	var refused *RefusedError
	if errors.As(err, &refused) {
		return false
	}
	var status *StatusError
	if errors.As(err, &status) {
		// What a load balancer in front of a stopped hub says. A 500 is
		// not here: that is the hub, up, failing to compile something,
		// and an operator needs to see it rather than a stale success.
		switch status.Status {
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}

	var op *net.OpError
	if errors.As(err, &op) {
		// The peer sent a TLS alert: something is there, and it refused
		// this node. That is how a certificate the hub will not accept
		// reaches a client.
		return op.Op != "remote error"
	}

	if errors.Is(err, context.Canceled) {
		// The caller gave up, usually `jobs kill`. Nothing is known about
		// the hub, and a killed job must not carry on from a cache.
		return false
	}
	var dns *net.DNSError
	var timeout net.Error
	if errors.As(err, &dns) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	// net/http's HTTP/2 client reports a hub going away only as text: the
	// types are unexported.
	msg := err.Error()
	return strings.Contains(msg, "http2: client connection lost") ||
		strings.Contains(msg, "http2: server sent GOAWAY") ||
		strings.Contains(msg, "connection reset by peer")
}
