package main

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/grains"
)

// The hub issues its certificate for this host's qualified name as
// grains.FQDN finds it -- the name a node on this machine enrols as and
// its fqdn grain reports -- beside the names it always has. It asked for
// the canonical name alone, by a third rule (DIVERGENCE 5.271). That the
// lookup does not wait on DNS is TestServerNamesDoNotWaitForDNS below.
func TestServerNamesUseTheSharedFQDN(t *testing.T) {
	host, _ := os.Hostname()
	defer func(orig func(string) string) { hostFQDN = orig }(hostFQDN)
	asked := ""
	hostFQDN = func(h string) string { asked = h; return "ip-10-0-0-5.ec2.internal" }

	args, err := cli.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	names := serverNames(args, ":4510")
	if asked != host {
		t.Errorf("serverNames asked about %q, want this host, %q", asked, host)
	}
	for _, want := range []string{"localhost", "127.0.0.1", "::1", host, "ip-10-0-0-5.ec2.internal"} {
		if !slices.Contains(names, want) {
			t.Errorf("the names are %v, missing %q", names, want)
		}
	}
}

// A resolver that never answers holds the hub up for the lookup's bound
// and no longer, and the hub then issues for the names it already has.
// The lookup had no deadline, and an unresolvable hostname cost 9.3
// seconds on the development Mac before the hub could listen. Through
// the real lookup, grains.FQDN, with DNS replaced. DIVERGENCE 5.264.
func TestServerNamesDoNotWaitForDNS(t *testing.T) {
	r := &silentResolver{}
	defer grains.UseResolver(r)()
	args, err := cli.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	names := serverNames(args, ":4510")
	took := time.Since(start)

	host, _ := os.Hostname()
	if strings.Contains(host, ".") {
		t.Skip("this machine's hostname is already qualified, so the hub never asks DNS")
	}
	if !r.asked {
		t.Fatal("serverNames did not ask the resolver, so this test shows nothing")
	}
	if took > grains.FQDNLookupTimeout+time.Second {
		t.Errorf("serverNames took %v against a resolver that never answers; the bound is %v",
			took, grains.FQDNLookupTimeout)
	}
	for _, want := range []string{"localhost", "127.0.0.1", "::1", host} {
		if !slices.Contains(names, want) {
			t.Errorf("without DNS the names are %v, missing %q", names, want)
		}
	}
}

// silentResolver never answers, until its context ends.
type silentResolver struct{ asked bool }

func (r *silentResolver) LookupCNAME(ctx context.Context, _ string) (string, error) {
	r.asked = true
	<-ctx.Done()
	return "", ctx.Err()
}
func (r *silentResolver) LookupHost(ctx context.Context, _ string) ([]string, error) {
	r.asked = true
	<-ctx.Done()
	return nil, ctx.Err()
}
func (r *silentResolver) LookupAddr(ctx context.Context, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
