package grains

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

type silentResolver struct{ t *testing.T }

func (r silentResolver) wait(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		r.t.Error("the lookup was given no deadline")
	}
	<-ctx.Done()
	return ctx.Err()
}

func (r silentResolver) LookupCNAME(ctx context.Context, _ string) (string, error) {
	return "", r.wait(ctx)
}
func (r silentResolver) LookupHost(ctx context.Context, _ string) ([]string, error) {
	return nil, r.wait(ctx)
}
func (r silentResolver) LookupAddr(ctx context.Context, _ string) ([]string, error) {
	return nil, r.wait(ctx)
}

// FQDN does not wait on a resolver that never answers: past
// FQDNLookupTimeout it is the hostname, as when the lookups fail. Every
// node command collects grains, and the hub asks before it listens.
// DIVERGENCE 5.264.
func TestTheFQDNGrainDoesNotWaitForDNS(t *testing.T) {
	defer func(orig Resolver) { fqdnResolver = orig }(fqdnResolver)
	fqdnResolver = silentResolver{t}

	start := time.Now()
	got := FQDN("web1")
	took := time.Since(start)

	if got != "web1" {
		t.Errorf("fqdn = %q without DNS, want the hostname", got)
	}
	if took > FQDNLookupTimeout+time.Second {
		t.Errorf("FQDN took %v against a resolver that never answers; the bound is %v", took, FQDNLookupTimeout)
	}
}

// fakeResolver answers from tables.
type fakeResolver struct {
	cname map[string]string
	hosts map[string][]string
	addrs map[string][]string
}

var errNoSuchHost = errors.New("no such host")

func (f fakeResolver) LookupCNAME(_ context.Context, h string) (string, error) {
	if v, ok := f.cname[h]; ok {
		return v, nil
	}
	return "", errNoSuchHost
}
func (f fakeResolver) LookupHost(_ context.Context, h string) ([]string, error) {
	if v, ok := f.hosts[h]; ok {
		return v, nil
	}
	return nil, errNoSuchHost
}
func (f fakeResolver) LookupAddr(_ context.Context, a string) ([]string, error) {
	if v, ok := f.addrs[a]; ok {
		return v, nil
	}
	return nil, errNoSuchHost
}

// The fqdn grain follows Salt's rule, which the node's identity already
// did: a host whose address reverses to a name that does not begin with
// its hostname is called by that name, as socket.getfqdn calls it. The
// grain took only a reverse name beginning with the hostname, so on such
// a host it said web1 while the node enrolled as the PTR name.
// DIVERGENCE 5.271.
func TestTheFQDNGrainFollowsSaltsRule(t *testing.T) {
	defer func(orig Resolver) { fqdnResolver = orig }(fqdnResolver)
	fqdnResolver = fakeResolver{
		hosts: map[string][]string{"web1": {"10.0.0.5"}},
		addrs: map[string][]string{"10.0.0.5": {"ip-10-0-0-5.ec2.internal."}},
	}
	if got := FQDN("web1"); got != "ip-10-0-0-5.ec2.internal" {
		t.Errorf("fqdn = %q, want the PTR name, as Salt's socket.getfqdn gives", got)
	}

	// And the grain is that answer, with the domain after the first dot:
	// the resolver is told about this machine's own hostname, which is
	// what the grain asks for.
	host, _ := os.Hostname()
	if host == "" || strings.Contains(host, ".") {
		t.Skip("this machine's hostname is already qualified, so the grain never asks")
	}
	fqdnResolver = fakeResolver{
		hosts: map[string][]string{host: {"10.0.0.5"}},
		addrs: map[string][]string{"10.0.0.5": {"ip-10-0-0-5.ec2.internal."}},
	}
	g := value.NewMap(8)
	collectIdentity(g, Options{NodeID: "n"})
	if fqdn, _ := g.Get("fqdn"); fqdn != "ip-10-0-0-5.ec2.internal" {
		t.Errorf("the fqdn grain = %v, want the PTR name", fqdn)
	}
	if domain, _ := g.Get("domain"); domain != "ec2.internal" {
		t.Errorf("the domain grain = %v", domain)
	}
}
