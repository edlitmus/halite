package grains

import (
	"context"
	"net"
	"strings"
	"time"
)

// This host's fully qualified name, the one answer the node's identity,
// the fqdn grain and the hub's certificate names all use.
//
// There were three. The node's identity (SPEC 7.2 step 5) asked for the
// canonical name, then forward and reverse, and took any name with a
// domain; the fqdn grain asked forward and reverse only, and took a
// reverse name only if it began with the hostname; the hub asked for the
// canonical name alone. On a host whose address reverses to a name that
// is not its own -- a cloud machine called web1 whose PTR is
// ip-10-0-0-5.ec2.internal -- the node was named one thing and its fqdn
// grain said another. The owner chose the node's rule, which is Salt's:
// socket.getfqdn, which Salt uses for both the fqdn grain and the
// default minion id, takes the canonical name and then the first name // lexicon:allow — Salt's own term
// with a domain. DIVERGENCE 5.273.

// Resolver is the part of *net.Resolver the lookup needs, so a test can
// say what the network answers instead of depending on the machine it
// runs on.
type Resolver interface {
	LookupCNAME(ctx context.Context, host string) (string, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// fqdnResolver is what FQDN asks; a test replaces it.
var fqdnResolver Resolver = net.DefaultResolver

// UseResolver makes FQDN ask r and returns what puts the previous one
// back. It is for tests in the packages that call FQDN -- the hub's, for
// one, which holds its own startup to the lookup's bound -- and nothing
// else calls it.
func UseResolver(r Resolver) (restore func()) {
	prev := fqdnResolver
	fqdnResolver = r
	return func() { fqdnResolver = prev }
}

// FQDNLookupTimeout bounds every lookup FQDN makes, together. Grains are
// collected by every node command and the hub resolves its name before
// it listens, and with no bound both waited as long as the resolver did:
// 9.3 seconds on a host whose name does not resolve (DIVERGENCE 5.264).
// Past it the answer is the hostname, as when the lookups fail.
const FQDNLookupTimeout = 2 * time.Second

// FQDN is host's fully qualified name, or host itself when it already
// has a domain, when nothing qualifies it, or when DNS does not answer
// within FQDNLookupTimeout.
func FQDN(host string) string {
	if host == "" || strings.Contains(host, ".") {
		return host
	}
	ctx, cancel := context.WithTimeout(context.Background(), FQDNLookupTimeout)
	defer cancel()
	return ResolveFQDN(ctx, fqdnResolver, host)
}

// ResolveFQDN is FQDN against a given resolver and deadline.
func ResolveFQDN(ctx context.Context, r Resolver, host string) string {
	// The canonical name is the direct question and usually answers it.
	if cname, err := r.LookupCNAME(ctx, host); err == nil {
		if name := QualifiedName(cname); name != "" {
			return name
		}
	}
	// Otherwise the way Salt gets there: forward, then back. Reverse
	// resolution is what socket.getfqdn uses, and it answers on hosts
	// where the canonical name does not.
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return host
	}
	for _, addr := range addrs {
		names, err := r.LookupAddr(ctx, addr)
		if err != nil {
			continue
		}
		for _, n := range names {
			if name := QualifiedName(n); name != "" {
				return name
			}
		}
	}
	return host
}

// QualifiedName returns a name that carries a domain, or "". The
// trailing dot DNS writes is dropped, and the names a resolver gives the
// loopback and IPv6 placeholders are not names of this host.
func QualifiedName(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), ".")
	if !strings.Contains(name, ".") {
		return ""
	}
	lower := strings.ToLower(name)
	for _, bad := range []string{"localhost", "ip6-", "ipv6-"} {
		if strings.HasPrefix(lower, bad) {
			return ""
		}
	}
	return name
}
