package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/transport"
)

// Issue 321, M-06's other half: how fast one address may spend the
// enrollment bounds. DIVERGENCE 5.398.

// ask makes one enrollment request for name with a fresh key.
func (l *lab) ask(t *testing.T, name string) error {
	t.Helper()
	client, key := l.node(t)
	_, err := client.Enroll(context.Background(), key, name, "")
	return err
}

// **One address is held to its budget**, and the request over it is
// answered before it is read: no record, a 429, a Retry-After, and the
// stable code. Then the budget drains and the address is admitted again.
func TestOneAddressIsHeldToItsEnrollmentBudget(t *testing.T) {
	l := newLab(t)
	l.server.EnrollSourceBurst = 3
	l.server.EnrollSourceRate = 0.1

	for i := range 3 {
		if err := l.ask(t, fmt.Sprintf("n%d.example", i)); !errors.Is(err, transport.ErrPending) {
			t.Fatalf("request %d, inside the budget: %v", i, err)
		}
	}
	err := l.ask(t, "over.example")
	after, limited := transport.RateLimited(err)
	if !limited {
		t.Fatalf("the request over the budget: %v", err)
	}
	if after != 10*time.Second {
		t.Errorf("Retry-After said %s, want 10s at one every ten seconds", after)
	}
	if transport.CodeOf(err) != transport.CodeRateLimited {
		t.Errorf("code %q, want %q", transport.CodeOf(err), transport.CodeRateLimited)
	}
	if _, err := l.server.Authority.Store.Get("over.example"); !errors.Is(err, keystore.ErrNotFound) {
		t.Errorf("the refused request left a record: %v", err)
	}

	l.advance(10 * time.Second)
	if err := l.ask(t, "over.example"); !errors.Is(err, transport.ErrPending) {
		t.Errorf("ten seconds later: %v", err)
	}
	if _, limited := transport.RateLimited(l.ask(t, "again.example")); !limited {
		t.Error("the drained budget admitted two")
	}
}

// **What an honest fleet repeats is not charged.** A node asking again
// about its own pending request, which `enroll --wait` does every five
// seconds, and a certificate being collected, are both given back. A
// refusal, which is what a caller with no business here produces, is
// charged.
func TestWaitingAndCollectingAreNotCharged(t *testing.T) {
	l := newLab(t)
	l.server.EnrollSourceBurst = 2
	l.server.EnrollSourceRate = 0.001
	ctx := context.Background()

	client, key := l.node(t)
	if _, err := client.Enroll(ctx, key, "web1.example", ""); !errors.Is(err, transport.ErrPending) {
		t.Fatal(err)
	}
	for i := range 10 {
		if _, err := client.Enroll(ctx, key, "web1.example", ""); !errors.Is(err, transport.ErrPending) {
			t.Fatalf("asking again, %d: %v", i, err)
		}
	}
	if _, err := l.server.Authority.Accept("web1.example"); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		if _, err := client.Enroll(ctx, key, "web1.example", ""); err != nil {
			t.Fatalf("collecting, %d: %v", i, err)
		}
	}

	// One charge spent of two: a refusal takes the other.
	err := l.ask(t, "web1.example")
	var refused *transport.RefusedError
	if !errors.As(err, &refused) || refused.Status != http.StatusForbidden {
		t.Fatalf("another key for an accepted name: %v", err)
	}
	if _, limited := transport.RateLimited(l.ask(t, "web2.example")); !limited {
		t.Error("a refusal was not charged")
	}
}

// **A token that spends is not metered**, so a token-mode bootstrap of
// many machines behind one address is not held; a bad token is.
func TestASpentTokenIsNotCharged(t *testing.T) {
	l := newLab(t)
	l.server.Authority.Mode = keystore.ModeToken
	l.server.EnrollSourceBurst = 1
	l.server.EnrollSourceRate = 0.001
	ctx := context.Background()

	for i := range 5 {
		name := fmt.Sprintf("t%d.example", i)
		_, tok, err := l.server.Authority.Store.MintToken(keystore.TokenOptions{NodeGlob: name, TTL: time.Hour}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		client, key := l.node(t)
		if _, err := client.Enroll(ctx, key, name, tok); err != nil {
			t.Fatalf("token enrollment %d: %v", i, err)
		}
	}
	client, key := l.node(t)
	if _, err := client.Enroll(ctx, key, "bad.example", "not-a-token"); err == nil {
		t.Fatal("a made-up token was admitted")
	}
	if _, limited := transport.RateLimited(l.ask(t, "after.example")); !limited {
		t.Error("a bad token was not charged")
	}
}

// Concurrent requests from one address cannot all pass a check made
// before any of them is counted: the charge is taken on the way in.
func TestConcurrentRequestsCannotOverrunTheBudget(t *testing.T) {
	l := newLab(t)
	l.server.EnrollSourceBurst = 4
	l.server.EnrollSourceRate = 0.001
	results := make(chan error, 20)
	for i := range 20 {
		go func() { results <- l.ask(t, fmt.Sprintf("c%d.example", i)) }()
	}
	var pending, limited int
	for range 20 {
		err := <-results
		switch _, held := transport.RateLimited(err); {
		case errors.Is(err, transport.ErrPending):
			pending++
		case held:
			limited++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if pending != 4 || limited != 16 {
		t.Errorf("%d pending and %d held, want 4 and 16", pending, limited)
	}
}

func TestAnIPv6SourceIsHeldByItsSlash64(t *testing.T) {
	for addr, want := range map[string]string{
		"[2001:db8:1:2:3:4:5:6]:4510": "2001:db8:1:2::/64",
		"[2001:db8:1:2:ffff::1]:1":    "2001:db8:1:2::/64",
		"[fe80::1%em0]:4510":          "fe80::/64",
		"192.0.2.7:4510":              "192.0.2.7",
		"[::ffff:192.0.2.7]:4510":     "192.0.2.7",
		"not an address":              "not an address",
	} {
		r := &http.Request{RemoteAddr: addr}
		if got := enrollSource(r); got != want {
			t.Errorf("%s: %s, want %s", addr, got, want)
		}
	}
}

// The table of sources is bounded, and a full one gives up a source
// that has drained before one that is spending.
func TestTheEnrollSourceTableIsBounded(t *testing.T) {
	l := newEnrollLimit()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	// The one being watched: its budget is full.
	for range 3 {
		l.admit("attacker", now, 0.001, 3)
	}
	for i := range enrollSources + 100 {
		l.admit(fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255), now, 0.001, 3)
	}
	if len(l.sources) > enrollSources {
		t.Errorf("%d sources held, bound %d", len(l.sources), enrollSources)
	}
	if l.admit("attacker", now, 0.001, 3) == 0 {
		t.Error("a flood of new sources evicted the one at its limit")
	}
}
