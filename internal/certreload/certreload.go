// Package certreload serves a TLS certificate from disk and picks up a
// replacement without a restart.
//
// A certificate managed by the tree is renewed in place: a state writes a
// new key and certificate to the same paths before the old one expires. A
// process that loaded the pair once at startup goes on presenting the old
// certificate until it expires, and then every client fails. The node's
// metrics listener had that defect (DIVERGENCE 5.248) and so did
// halite-api's serving certificate (DIVERGENCE 5.250), and a relay's
// client certificate for its upstream had it on the other side of the
// handshake; this is the one implementation all three use, so that they do
// not drift apart -- the first version of it had a Windows-only bug that
// only one copy would have been fixed in.
package certreload

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"os"
	"sync"
	"time"
)

// Reloader serves a TLS certificate from disk and reads it again when
// either file changes.
//
// Without it the listener loaded the pair once, at startup, and served it
// for the life of the agent. The certificate state renews the files 30
// days before they expire (docs/metrics.md), so a renewal wrote a new
// certificate that a running agent never served, and the endpoint went on
// presenting the old one until it expired and every scrape failed -- on
// every node, about ninety days after the state first ran, unless the
// agent happened to be restarted in between. DIVERGENCE 5.248.
//
// It reads both files on every handshake and compares them with the bytes
// of the pair it last loaded; for a scrape target that is two reads of
// about a kilobyte each interval. The contents and not the file's metadata,
// because the metadata was not enough: the first version compared
// identity, modification time and size, and on Windows a key replaced by
// rename passed all three -- os.SameFile resolves a stored os.Stat result
// from its path when it is asked, so the old and new files are "the same
// file", and a key of the same length written a few milliseconds later can
// carry the same timestamp. Linux and FreeBSD caught it through the inode;
// the CI's Windows leg did not.
//
// A pair that will not load -- the key replaced and its certificate not
// yet, or a file somebody truncated -- is not served. The previous pair
// is, and the failure is said once until it changes or clears, and the
// files are tried again on the next connection. A listener that went
// dark because a renewal was half written would turn a routine renewal
// into an outage.
type Reloader struct {
	certFile, keyFile string
	info, warn        func(msg string, kv ...any)
	// what and verb are how the log lines name the certificate and what
	// is done with it: a server serves one, a client presents one.
	what, verb string

	mu              sync.Mutex
	pair            *tls.Certificate
	certPEM, keyPEM []byte
	lastProblem     string
}

// New is a Reloader for a server's certificate: GetCertificate.
func New(certFile, keyFile string, info, warn func(string, ...any)) (*Reloader, error) {
	return load(&Reloader{certFile: certFile, keyFile: keyFile, info: info, warn: warn,
		what: "serving certificate", verb: "served"})
}

// NewClient is a Reloader for the certificate a client presents: Load,
// whose caller decides when to ask, because a client that has a
// connection open goes on being the identity it opened it with whatever
// is on disk (see transport.Client.CertFiles).
func NewClient(certFile, keyFile string, info, warn func(string, ...any)) (*Reloader, error) {
	return load(&Reloader{certFile: certFile, keyFile: keyFile, info: info, warn: warn,
		what: "client certificate", verb: "presented"})
}

func load(r *Reloader) (*Reloader, error) {
	certPEM, keyPEM, err := r.read()
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	r.pair, r.certPEM, r.keyPEM = &pair, certPEM, keyPEM
	return r, nil
}

func (r *Reloader) read() ([]byte, []byte, error) {
	certPEM, err := os.ReadFile(r.certFile)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(r.keyFile)
	if err != nil {
		return nil, nil, err
	}
	return certPEM, keyPEM, nil
}

// GetCertificate is tls.Config.GetCertificate.
func (r *Reloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.Load(), nil
}

// Load is the pair on disk if it has changed and loads, and the previous
// one otherwise. The pointer changes exactly when the pair does, so a
// caller can tell a renewal by comparing it with the last one it had.
func (r *Reloader) Load() *tls.Certificate {
	r.mu.Lock()
	defer r.mu.Unlock()
	certPEM, keyPEM, err := r.read()
	if err != nil {
		r.problem(err)
		return r.pair
	}
	if bytes.Equal(certPEM, r.certPEM) && bytes.Equal(keyPEM, r.keyPEM) {
		return r.pair
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		r.problem(err)
		return r.pair
	}
	r.pair, r.certPEM, r.keyPEM, r.lastProblem = &pair, certPEM, keyPEM, ""
	if r.info != nil {
		kv := []any{"cert", r.certFile}
		if leaf, err := x509.ParseCertificate(pair.Certificate[0]); err == nil {
			kv = append(kv, "not_after", leaf.NotAfter.UTC().Format(time.RFC3339))
		}
		r.info("the "+r.what+" changed on disk and is now being "+r.verb, kv...)
	}
	return r.pair
}

// problem says a failure once, until it changes or clears.
func (r *Reloader) problem(err error) {
	if err.Error() == r.lastProblem {
		return
	}
	r.lastProblem = err.Error()
	if r.warn != nil {
		r.warn("the "+r.what+" on disk cannot be loaded; the previous one is still being "+r.verb,
			"cert", r.certFile, "error", err.Error())
	}
}
