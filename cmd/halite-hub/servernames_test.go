package main

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/cli"
)

// A resolver that never answers holds the hub up for hostLookupTimeout
// and no longer, and the hub then issues for the names it already has.
// The lookup had no deadline, and an unresolvable hostname cost 9.3
// seconds on the development Mac before the hub could listen.
// DIVERGENCE 5.263.
func TestServerNamesDoNotWaitForDNS(t *testing.T) {
	asked := false
	defer func(orig func(context.Context, string) (string, error)) { lookupCNAME = orig }(lookupCNAME)
	lookupCNAME = func(ctx context.Context, host string) (string, error) {
		asked = true
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the lookup was given no deadline")
		}
		<-ctx.Done() // a resolver that never answers
		return "", ctx.Err()
	}
	args, err := cli.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	names := serverNames(args, ":4510")
	took := time.Since(start)

	if !asked {
		t.Fatal("serverNames did not ask the resolver, so this test shows nothing")
	}
	if took > hostLookupTimeout+time.Second {
		t.Errorf("serverNames took %v against a resolver that never answers; the bound is %v", took, hostLookupTimeout)
	}
	host, _ := os.Hostname()
	for _, want := range []string{"localhost", "127.0.0.1", "::1", host} {
		if !slices.Contains(names, want) {
			t.Errorf("without DNS the names are %v, missing %q", names, want)
		}
	}
}
