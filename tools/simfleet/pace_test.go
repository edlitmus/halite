package main

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/pki"
	"github.com/edlitmus/halite/internal/transport"
)

// A simulated node that cannot reach the hub backs off as a node does,
// rather than trying again every second.
//
// The "hub" here accepts and closes, so every attempt fails at once. With
// the node's pacer the attempts come at 0, about 1 s, and about 3 s (1 s
// and 2 s, each within a fifth either way), and the next not before
// 5.6 s; a fixed second gives one at 0, 1, 2, 3 and 4 s. So 4.5 s holds
// at most three attempts with the node's pacer and five without it.
func TestASimulatedNodeBacksOffAsANodeDoes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	ca, err := pki.NewCA(pki.ECDSAP256, "pace test CA", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	f := &fleet{hubURL: "https://" + ln.Addr().String(), serverName: "localhost", ca: ca.Cert, jobs: newJobs(1)}
	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()
	f.connect(ctx, &sim{id: "sim-1"})
	if got := accepted.Load(); got < 2 || got > 3 {
		t.Errorf("%d attempts in 4.5 s, want 2 or 3 (the node's backoff); a fixed second gives 5", got)
	}
}

// flaky fails its first post with an error a retry may fix.
type flaky struct{ calls atomic.Int64 }

func (p *flaky) Return(context.Context, any) error {
	if p.calls.Add(1) == 1 {
		return errors.New("connection reset by peer")
	}
	return nil
}

// A simulated node retries a return that failed, as a node does, rather
// than counting it lost at the first failure. Near saturation that retry
// is load a fleet gives the hub, and simfleet used to take it away.
func TestASimulatedNodeRetriesAReturn(t *testing.T) {
	f := &fleet{jobs: newJobs(1)}
	p := &flaky{}
	f.answer(context.Background(), p, &sim{id: "sim-1"}, transport.Message{T: transport.MsgJob, JID: "J1", Fun: "test.ping"})
	rec := f.jobs.get("J1", "test.ping")
	if p.calls.Load() != 2 || rec.returnErrs.Load() != 0 || rec.returned[0].Load() == 0 {
		t.Errorf("posted %d times, %d return errors, returned=%d; want 2 posts, no error, and a return time",
			p.calls.Load(), rec.returnErrs.Load(), rec.returned[0].Load())
	}
}
