package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/cli"
	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/pki"
)

// A node posts its returns on one connection to the hub, and opens a new
// one only when what it reads from disk has changed.
//
// postReturns built a new client for each return, and so a new
// connection, which nothing closed: the hub held it, idle, for 90 s.
// simfleet copied the node, and on a dedicated hub with 5,000 simulated
// nodes two jobs left 10,000 of them open beside the 5,000 streams
// (DIVERGENCE 5.279, step 1). Counted here at the hub's listener, which
// is the hub's side of it, against a real hub.
func TestReturnsShareOneConnectionUntilTheCertificateChanges(t *testing.T) {
	l := newAnchorLab(t, "web1.example")
	n := nodeForEvidence(t, "")
	args, err := cli.Parse([]string{"--hub", l.addr, "--pki-dir", l.pkiDir})
	if err != nil {
		t.Fatal(err)
	}
	returns := make(chan *job.Return)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); n.postReturns(ctx, args, returns) }()
	t.Cleanup(func() { cancel(); <-done })

	// Each send is taken only once the previous return has been posted:
	// the channel is unbuffered and postReturns posts in its own loop.
	// So after the last send every return before it has been answered.
	// The hub refuses each, final, because it never sent these jobs;
	// what is counted is the connection, which a refusal does not end.
	seq := 0
	post := func(count int) {
		t.Helper()
		for range count {
			seq++
			select {
			case returns <- &job.Return{JID: job.ID(fmt.Sprintf("20261110T0000000000%02d", seq)),
				NodeID: "web1.example", Fun: "test.ping", Success: true, Schema: job.ReturnSchema}:
			case <-time.After(30 * time.Second):
				t.Fatalf("return %d was not taken in 30 s", seq)
			}
		}
	}

	post(20)
	if got := l.accepted.Load(); got != 1 {
		t.Fatalf("20 returns opened %d connections to the hub, want 1", got)
	}

	// A new certificate where the node reads it, issued for the key it
	// has: what changes is the certificate presented, and the next
	// return must present it, which takes a new connection.
	files := pki.Files{Dir: l.pkiDir}
	key, err := files.ReadKey(pki.NodeKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := pki.NewNodeCSR(key, "web1.example")
	if err != nil {
		t.Fatal(err)
	}
	csr, err := pki.DecodeCSR(pki.EncodeCSR(csrDER))
	if err != nil {
		t.Fatal(err)
	}
	der, err := l.ca.IssueNode(csr, "web1.example", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.WriteCert(pki.NodeCertFile, der); err != nil {
		t.Fatal(err)
	}
	post(5)
	if got := l.accepted.Load(); got != 2 {
		t.Fatalf("after a renewal, %d connections in all, want 2: one for the old certificate and one for the new", got)
	}
}
