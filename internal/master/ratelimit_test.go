package master

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/ca"
	"github.com/edlitmus/halite/internal/transport"
)

// clock is a hand-wound time source, so the limiter's behaviour over a
// minute can be tested in microseconds.
type clock struct{ at time.Time }

func (c *clock) now() time.Time       { return c.at }
func (c *clock) tick(d time.Duration) { c.at = c.at.Add(d) }

func TestRateLimiterSpendsAndRefills(t *testing.T) {
	c := &clock{at: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	l := newRateLimiter(60)
	l.now = c.now

	// A full bucket is one minute's worth, so a fleet coming up together
	// is not turned away for arriving at once.
	for i := 0; i < 60; i++ {
		if !l.allow("10.0.0.1:5000") {
			t.Fatalf("request %d of the first minute was refused", i+1)
		}
	}
	if l.allow("10.0.0.1:5000") {
		t.Fatal("the 61st request in a minute must be refused")
	}

	// One token a second comes back.
	c.tick(time.Second)
	if !l.allow("10.0.0.1:5000") {
		t.Error("a second later there should be a token")
	}
	if l.allow("10.0.0.1:5000") {
		t.Error("but only one")
	}
	c.tick(time.Hour)
	if !l.allow("10.0.0.1:5000") {
		t.Error("an idle bucket refills")
	}
}

func TestRateLimiterCountsPerSourceAddress(t *testing.T) {
	c := &clock{at: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	l := newRateLimiter(2)
	l.now = c.now

	for i := 0; i < 2; i++ {
		if !l.allow("10.0.0.1:5000") {
			t.Fatal("its own budget")
		}
	}
	if l.allow("10.0.0.1:6000") {
		// A different port is the same host: the budget is per address,
		// or a flood from one machine would just open more connections.
		t.Error("a second connection from the same host shares the budget")
	}
	if !l.allow("10.0.0.2:5000") {
		t.Error("another host has its own budget")
	}
}

func TestRateLimiterForgetsIdleSources(t *testing.T) {
	c := &clock{at: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)}
	l := newRateLimiter(60)
	l.now = c.now

	for i := 0; i < 100; i++ {
		l.allow(netAddr(i))
	}
	if len(l.buckets) != 100 {
		t.Fatalf("holding %d buckets, want 100", len(l.buckets))
	}
	c.tick(sweepAfter + time.Minute)
	l.allow("10.0.0.1:5000")
	if len(l.buckets) != 1 {
		t.Errorf("holding %d buckets after a sweep, want 1", len(l.buckets))
	}
}

func netAddr(i int) string {
	return fmt.Sprintf("10.0.%d.%d:5000", i/256, i%256)
}

// TestEnrollmentIsPacedPerSource is the same rule over the wire: the one
// route that answers before anyone has authenticated cannot be used to
// spend the control plane's time without limit.
func TestEnrollmentIsPacedPerSource(t *testing.T) {
	f := newFleet(t, Config{EnrollRate: 3})
	ctx := context.Background()
	client := f.anonymousClient(t)

	var lastErr error
	for i := 0; i < 4; i++ {
		var resp transport.EnrollResponse
		lastErr = client.Post(ctx, transport.PathEnroll,
			transport.EnrollRequest{ID: fmt.Sprintf("web%d", i), CSR: string(csrFor(t, fmt.Sprintf("web%d", i)))}, &resp)
	}
	if lastErr == nil {
		t.Fatal("the fourth enrollment in a minute should have been throttled")
	}
	if got := lastErr.Error(); !strings.Contains(got, "429") || !strings.Contains(got, "too many") {
		t.Errorf("throttling should say so: %v", lastErr)
	}
}

// csrFor mirrors the CA package's helper: a request for a fresh key.
func csrFor(t *testing.T, id string) []byte {
	t.Helper()
	key, _, err := ca.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	csrPEM, err := ca.NewCSR(key, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return csrPEM
}
