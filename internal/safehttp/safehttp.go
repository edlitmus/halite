// Package safehttp is the outbound HTTP client SPEC section 15.2 gives
// the `http` module, and the contract it has to keep.
//
// The contract is four clauses and one sentence of reasoning:
// "`query`, with mandatory certificate verification, a default timeout,
// a maximum response size, a redirect limit, and a denylist for
// link-local and metadata addresses unless explicitly permitted. Salt's
// `http.query` will happily fetch 169.254.169.254 on request from a
// templated state."
//
// That last sentence is the threat. A state file is rendered with data a
// node supplies — its grains, its pillar — and a template that builds a
// URL from any of it can be made to build one pointing at the cloud
// instance metadata service, which will hand back the machine's own
// credentials. The node then puts the answer wherever the state told it
// to.
//
// # Why the check is on the address and not the URL
//
// The obvious guard reads the URL and refuses `169.254.169.254`. It
// stops nothing:
//
//   - a name resolves to it — `metadata.google.internal` is the
//     documented way to reach it, and any attacker-controlled domain can
//     have an A record pointing there;
//   - a redirect goes to it, and the first URL was innocent;
//   - the name resolves twice, answering harmlessly for the check and
//     with the metadata address for the connection a moment later, which
//     is DNS rebinding and is a technique rather than a coincidence.
//
// So the check is in the dialer, on the address actually being connected
// to, and it therefore runs once per hop and once per address the
// resolver returns. There is no window between the check and the
// connection because they are the same operation.
package safehttp

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// The defaults SPEC 15.2 asks for by name.
const (
	// DefaultTimeout bounds one request including its redirects. Long
	// enough for an artifact server on a slow link, short enough that a
	// state run does not hang on a host that is not answering.
	DefaultTimeout = 30 * time.Second

	// DefaultMaxBody bounds what a response may return. A body this size
	// is already far past anything a state should be reading into
	// pillar; the limit exists so that a misconfigured URL cannot take
	// the node's memory with it.
	DefaultMaxBody int64 = 10 << 20

	// MaxRedirects bounds a redirect chain. Every hop is dialled, so
	// every hop is checked; the limit is what stops a loop.
	MaxRedirects = 5
)

// deniedNets are the ranges a request may not reach unless the caller
// says otherwise.
//
// Link-local carries every cloud's metadata service — 169.254.169.254 on
// AWS, Azure and GCP, and 100.100.100.200 on Alibaba, which is not
// link-local and is listed separately below. fe80::/10 is its IPv6
// counterpart, and fd00:ec2::254 is AWS's IPv6 metadata address, which
// sits in a unique-local range that is otherwise legitimate.
//
// Private ranges are deliberately *not* here. A state fetching a
// checksum from an internal artefact server is the normal case, and
// denying 10.0.0.0/8 would break more than it protected. What is denied
// is the set that hands out credentials.
var deniedNets = mustParseCIDRs(
	"169.254.0.0/16",     // IPv4 link-local, and every cloud's metadata service
	"fe80::/10",          // IPv6 link-local
	"100.100.100.200/32", // Alibaba Cloud metadata
	"fd00:ec2::254/128",  // AWS IPv6 metadata
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			// A literal in this file, so a failure here is a typo the
			// build should not survive.
			panic("safehttp: " + c + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}

// DeniedError is returned when a request would reach an address the
// contract keeps it away from.
//
// Its own type so a caller can tell "this was refused on purpose" from
// "the network is down", and so the message can say what to set.
type DeniedError struct {
	Host string
	IP   net.IP
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf(
		"%s resolves to %s, which is a link-local or cloud metadata address; "+
			"a state that can build a URL can be made to build this one, and the answer "+
			"is the machine's own credentials. Set allow_metadata: true if this node "+
			"really does need to read it",
		e.Host, e.IP)
}

// Denied reports whether an address is one the contract refuses.
func Denied(ip net.IP) bool {
	for _, n := range deniedNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Options are what a caller may vary. The zero value is the contract's
// defaults, which is the point: a caller that states nothing gets the
// safe behaviour rather than the permissive one.
type Options struct {
	// Timeout bounds the whole request. Zero is DefaultTimeout.
	Timeout time.Duration
	// MaxBody bounds the response. Zero is DefaultMaxBody.
	MaxBody int64
	// AllowMetadata lifts the address denylist. It exists because a node
	// that genuinely reads its own instance metadata is a real case —
	// the cloud grains do it — and refusing it outright would mean that
	// code could not use this client.
	AllowMetadata bool
}

func (o Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

func (o Options) maxBody() int64 {
	if o.MaxBody > 0 {
		return o.MaxBody
	}
	return DefaultMaxBody
}

// Client builds an HTTP client that keeps the contract.
//
// Certificate verification is mandatory and there is no option to turn
// it off. Salt has `verify_ssl: False` and it is reached for whenever a
// certificate is inconvenient, which is exactly when it is doing its
// job; a caller here that wants to trust a private authority adds it to
// the system store, where every other program on the machine can see the
// decision too.
func Client(opts Options) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialChecked(ctx, dialer, network, address, opts.AllowMetadata)
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// InsecureSkipVerify is deliberately not set and there is no
			// option that sets it.
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   opts.timeout(),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", MaxRedirects)
			}
			return nil
		},
	}
}

// dialChecked resolves an address and refuses the ones the contract
// denies, then connects to one it allowed.
//
// It dials the address it checked rather than handing the name back to
// the resolver, which is what closes the rebinding window: there is no
// second lookup for a different answer to arrive in.
func dialChecked(ctx context.Context, dialer *net.Dialer, network, address string, allow bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	ips, err := (&net.Resolver{}).LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%s resolves to no address", host)
	}

	var lastErr error
	for _, ip := range ips {
		if !allow && Denied(ip.IP) {
			// Refused rather than skipped. A host with one denied
			// address and one permitted one is a host somebody has
			// arranged, and quietly using the second would defeat the
			// check by making it depend on resolver ordering.
			return nil, &DeniedError{Host: host, IP: ip.IP}
		}
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// Body reads a response, refusing one that is larger than the limit.
//
// The limit is enforced by reading one byte past it rather than by
// trusting Content-Length, which a server sets and may set wrongly. The
// refusal names the limit, because the caller's next question is what to
// raise it to.
func Body(r io.Reader, max int64) ([]byte, error) {
	if max <= 0 {
		max = DefaultMaxBody
	}
	body, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > max {
		return nil, fmt.Errorf(
			"the response is larger than the %d-byte limit; raise max_body if that is what was wanted",
			max)
	}
	return body, nil
}

// CheckURL refuses a URL this client will not fetch, before a request is
// built.
//
// Only the scheme: everything else about the destination is decided at
// dial time, where it cannot be lied to. A `file://` URL is refused here
// because it names no host to dial and would otherwise reach the
// transport and fail with a message about an unsupported protocol, which
// tells an operator nothing about why a configuration management system
// declines to read local files over a network client.
func CheckURL(raw string) error {
	scheme, _, found := strings.Cut(raw, ":")
	if !found {
		return fmt.Errorf("%q is not a URL", raw)
	}
	switch strings.ToLower(scheme) {
	case "http", "https":
		return nil
	case "file":
		return fmt.Errorf("http.query does not read local files; use file.read for %q", raw)
	}
	return fmt.Errorf("http.query speaks http and https, not %q", scheme)
}
