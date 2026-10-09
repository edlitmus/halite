package grains

import (
	"context"
	"testing"
	"time"
)

type silentResolver struct{ t *testing.T }

func (r silentResolver) LookupHost(ctx context.Context, _ string) ([]string, error) {
	if _, ok := ctx.Deadline(); !ok {
		r.t.Error("the lookup was given no deadline")
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r silentResolver) LookupAddr(ctx context.Context, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// The fqdn grain does not wait on a resolver that never answers: past
// fqdnLookupTimeout it is the hostname, as when the lookups fail. Every
// node command collects grains, and the lookups had no deadline.
// DIVERGENCE 5.263.
func TestTheFQDNGrainDoesNotWaitForDNS(t *testing.T) {
	defer func(orig hostResolver) { fqdnResolver = orig }(fqdnResolver)
	fqdnResolver = silentResolver{t}

	start := time.Now()
	got := resolveFQDN("web1")
	took := time.Since(start)

	if got != "web1" {
		t.Errorf("fqdn = %q without DNS, want the hostname", got)
	}
	if took > fqdnLookupTimeout+time.Second {
		t.Errorf("resolveFQDN took %v against a resolver that never answers; the bound is %v", took, fqdnLookupTimeout)
	}
}
