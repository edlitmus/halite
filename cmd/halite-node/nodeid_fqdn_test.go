package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/config"
)

// fakeResolver says what the network answers, so these assert the rule
// rather than the machine the test runs on.
type fakeResolver struct {
	cname  string
	cerr   error
	hosts  []string
	herr   error
	addrs  map[string][]string
	addErr error
}

func (f fakeResolver) LookupCNAME(context.Context, string) (string, error) {
	return f.cname, f.cerr
}
func (f fakeResolver) LookupHost(context.Context, string) ([]string, error) {
	return f.hosts, f.herr
}
func (f fakeResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	if f.addErr != nil {
		return nil, f.addErr
	}
	return f.addrs[addr], nil
}

// SPEC 7.2 puts the fully qualified domain name at step 5 and the bare
// hostname at step 6. This resolver used to skip step 5 entirely, so a
// Linux node named itself with the short hostname -- and a tree carried
// over from Salt, whose minion id is the FQDN, then failed on // lexicon:allow — Salt's own term
// `id.split('.', 1)`.
func TestResolveFQDN(t *testing.T) {
	notFound := errors.New("no such host")

	for _, tc := range []struct {
		name string
		r    fakeResolver
		want string
	}{
		{
			name: "the canonical name answers it",
			r:    fakeResolver{cname: "web1.prod.example.com."},
			want: "web1.prod.example.com",
		},
		{
			name: "a canonical name with no domain is not an answer",
			r: fakeResolver{
				cname: "web1.",
				hosts: []string{"10.0.0.1"},
				addrs: map[string][]string{"10.0.0.1": {"web1.prod.example.com."}},
			},
			want: "web1.prod.example.com",
		},
		{
			name: "reverse resolution, which is how Salt's getfqdn gets there",
			r: fakeResolver{
				cerr:  notFound,
				hosts: []string{"10.0.0.1"},
				addrs: map[string][]string{"10.0.0.1": {"web1.prod.example.com."}},
			},
			want: "web1.prod.example.com",
		},
		{
			name: "a loopback name is not an identity",
			r: fakeResolver{
				cerr:  notFound,
				hosts: []string{"127.0.0.1", "10.0.0.1"},
				addrs: map[string][]string{
					"127.0.0.1": {"localhost.localdomain."},
					"10.0.0.1":  {"web1.prod.example.com."},
				},
			},
			want: "web1.prod.example.com",
		},
		{
			name: "nothing qualifies it, so the hostname stands",
			r:    fakeResolver{cerr: notFound, herr: notFound},
			want: "web1",
		},
		{
			name: "reverse gives only short names",
			r: fakeResolver{
				cerr:  notFound,
				hosts: []string{"10.0.0.1"},
				addrs: map[string][]string{"10.0.0.1": {"web1"}},
			},
			want: "web1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveFQDN(context.Background(), tc.r, "web1"); got != tc.want {
				t.Errorf("resolveFQDN = %q, want %q", got, tc.want)
			}
		})
	}
}

// A hostname that already carries a domain is the answer, and asking the
// network about it would only be a chance to get a different one.
func TestNodeFQDNLeavesAQualifiedHostnameAlone(t *testing.T) {
	const qualified = "web1.prod.example.com"
	if got := nodeFQDN(qualified); got != qualified {
		t.Errorf("nodeFQDN(%q) = %q", qualified, got)
	}
	if got := nodeFQDN(""); got != "" {
		t.Errorf("nodeFQDN(\"\") = %q", got)
	}
}

// And the resolver has to actually use it. Testing nodeFQDN on its own
// leaves the wiring unasserted: deleting the call from resolveNodeID
// passed every test above, which is the defect this file exists to stop.
//
// This one asks the machine it runs on, and skips where that machine has
// no qualified name to find -- the assertion is that resolveNodeID
// returns what nodeFQDN found, not that any particular name exists.
func TestResolveNodeIDUsesTheFQDN(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname on this machine")
	}
	qualified := nodeFQDN(host)
	if qualified == host {
		t.Skip("this machine has no fully qualified name, so there is nothing to tell apart")
	}

	// An empty root, so no pinned node_id is read, and no config or
	// environment to override the detected identity.
	t.Setenv("HALITE_NODE_ID", "")
	root := t.TempDir()
	args, err := cli.Parse([]string{"--root", root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node.yaml"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.Node, config.LoadOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	got := resolveNodeID(args, cfg)
	if got != qualified {
		t.Errorf("resolveNodeID = %q, want the qualified name %q; step 5 of SPEC 7.2 is being skipped", got, qualified)
	}
	if !strings.Contains(got, ".") {
		t.Errorf("resolveNodeID = %q, which a tree's `id.split('.', 1)` cannot unpack", got)
	}
}
