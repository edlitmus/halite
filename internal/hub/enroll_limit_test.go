package hub

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"net/http"
	"sync"
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

// slowAuthority makes every enrollment take d inside Authority.Enroll,
// which is where the charge taken on entry is held: on AWS on
// 2026-11-11 an issue took about 345 ms, and that is what turned a
// token-mode bootstrap into 1,910 refusals out of 2,000.
func slowAuthority(l *lab, d time.Duration) {
	l.server.Authority.Now = func() time.Time {
		time.Sleep(d)
		return time.Now()
	}
}

// tokenStorm sends n token enrollments at once from the lab's one
// address, a distinct name each, and counts what came back.
func tokenStorm(t *testing.T, l *lab, n int, tok string) (issued, limited, refused int) {
	t.Helper()
	type asker struct {
		client *transport.Client
		key    crypto.Signer
	}
	askers := make([]asker, n)
	for i := range askers {
		askers[i].client, askers[i].key = l.node(t)
	}
	results := make(chan error, n)
	for i, a := range askers {
		go func() {
			_, err := a.client.Enroll(context.Background(), a.key, fmt.Sprintf("s%d.example", i), tok)
			results <- err
		}()
	}
	for range n {
		err := <-results
		var r *transport.RefusedError
		switch _, held := transport.RateLimited(err); {
		case err == nil:
			issued++
		case held:
			limited++
		case errors.As(err, &r):
			refused++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	return issued, limited, refused
}

// **A token-mode bootstrap is not metered when it arrives all at once**,
// which is what the comment on enrollLimit always said and what was
// only true one at a time. DIVERGENCE 5.279 measured 90 issued and
// 1,910 refused with 429 out of 2,000 from one address on a 2,000-use
// token: every request was charged on entry and refunded only once it
// was issued, so more of them in flight than the burst filled the
// budget with charges not yet given back. Here sixteen at once against
// a burst of four, each held 100 ms inside the key store.
func TestConcurrentTokenEnrollmentsAreNotCharged(t *testing.T) {
	l := newLab(t)
	l.server.Authority.Mode = keystore.ModeToken
	l.server.EnrollSourceBurst = 4
	l.server.EnrollSourceRate = 0.001
	slowAuthority(l, 100*time.Millisecond)
	_, tok, err := l.server.Authority.Store.MintToken(keystore.TokenOptions{TTL: time.Hour, MaxUses: 16}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	issued, limited, refused := tokenStorm(t, l, 16, tok)
	if issued != 16 || limited != 0 || refused != 0 {
		t.Errorf("%d issued, %d held, %d refused; want all 16 issued", issued, limited, refused)
	}
	// And the budget is untouched: four requests with no token still fit.
	for i := range 4 {
		if err := l.ask(t, fmt.Sprintf("after%d.example", i)); !errors.Is(err, transport.ErrPending) {
			t.Fatalf("request %d after the storm: %v", i, err)
		}
	}
}

// **A live token admits no more uncharged requests than it has uses.**
// The obvious fix for the test above -- check that a request's token
// would spend, and if so do not charge it -- opens the hole the limiter
// exists to close: a token with one use left, sent by sixteen requests
// at once, passes the check sixteen times, spends once, and the fifteen
// that lose the race each leave a pending record (a token that does not
// spend still records the request for an operator), none of them
// charged. Here a two-use token sixteen times at once against a burst
// of four: two issue, and no more than the burst's four are left pending.
func TestAContendedTokenCannotFillPendingUncharged(t *testing.T) {
	l := newLab(t)
	l.server.Authority.Mode = keystore.ModeToken
	l.server.EnrollSourceBurst = 4
	l.server.EnrollSourceRate = 0.001
	slowAuthority(l, 100*time.Millisecond)
	_, tok, err := l.server.Authority.Store.MintToken(keystore.TokenOptions{TTL: time.Hour, MaxUses: 2}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	issued, _, _ := tokenStorm(t, l, 16, tok)
	if issued != 2 {
		t.Errorf("%d issued on a two-use token", issued)
	}
	recs, err := l.server.Authority.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	pending := 0
	for _, rec := range recs {
		if rec.State == keystore.Pending {
			pending++
		}
	}
	if pending > 4 {
		t.Errorf("%d pending records from one address, and its burst is 4", pending)
	}
}

// pendingCount is how many pending records the lab's key store holds.
func pendingCount(t *testing.T, l *lab) int {
	t.Helper()
	recs, err := l.server.Authority.Store.List()
	if err != nil {
		t.Fatal(err)
	}
	pending := 0
	for _, rec := range recs {
		if rec.State == keystore.Pending {
			pending++
		}
	}
	return pending
}

// **A request let through uncharged on its token, that then loses the
// token, is charged before it can leave a pending record** (DIVERGENCE
// 5.672). The test above sends sixteen at once and counts what is left,
// which finds the windows only when the scheduler happens to open them:
// it passed forty times on a Mac and failed once on Windows CI, with
// five pending against a burst of four. Here one request is stopped in
// each window in turn while others go past it.
//
//   - "admitted": the request has read the token's uses left (two) and
//     not yet asked hold. Two others spend both uses and leave. hold
//     counts requests in flight, not uses spent, so it then lets the
//     first through against the two it read.
//   - "held": the request holds one of two uses. A second takes the
//     other; a third, refused a hold, is charged on entry -- and spends
//     the use the first was holding, because a charged request carrying
//     the token spends it like any other.
//
// Either way the first request reaches the key store uncharged with a
// spent token, and a token that does not spend still records the
// request for an operator. Before the fix it was charged only once that
// record was written, so four requests with no token, which the burst
// of four allows, plus this one made five pending. It must instead be
// told to wait, and write nothing.
func TestALostTokenIsChargedBeforeItRecords(t *testing.T) {
	for _, step := range []string{"admitted", "held"} {
		t.Run(step, func(t *testing.T) {
			arrived, release := make(chan struct{}), make(chan struct{})
			var first, released sync.Once
			l := newLabWith(t, func(s *Server) {
				s.Authority.Mode = keystore.ModeToken
				s.EnrollSourceBurst = 4
				s.EnrollSourceRate = 0.001
				s.enrollStep = func(got string) {
					if got != step {
						return
					}
					stop := false
					first.Do(func() { stop = true })
					if stop {
						close(arrived)
						<-release
					}
				}
			})
			// Let the stopped request go however the test ends, so the
			// lab's server can shut down.
			defer released.Do(func() { close(release) })
			_, tok, err := l.server.Authority.Store.MintToken(keystore.TokenOptions{TTL: time.Hour, MaxUses: 2}, time.Now())
			if err != nil {
				t.Fatal(err)
			}

			client, key := l.node(t)
			stopped := make(chan error, 1)
			go func() {
				_, err := client.Enroll(context.Background(), key, "stopped.example", tok)
				stopped <- err
			}()
			<-arrived

			// Two others spend the token's two uses, one after the other.
			for _, name := range []string{"b.example", "c.example"} {
				c, k := l.node(t)
				if _, err := c.Enroll(context.Background(), k, name, tok); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
			}
			// Four with no token, which the burst allows.
			for i := range 4 {
				if err := l.ask(t, fmt.Sprintf("manual%d.example", i)); !errors.Is(err, transport.ErrPending) {
					t.Fatalf("request %d with no token: %v", i, err)
				}
			}

			released.Do(func() { close(release) })
			err = <-stopped
			if _, held := transport.RateLimited(err); !held {
				t.Errorf("the request whose token was spent while it waited: %v; want 429", err)
			}
			if n := pendingCount(t, l); n != 4 {
				t.Errorf("%d pending records from one address, and its burst is 4", n)
			}
			if _, err := l.server.Authority.Store.Get("stopped.example"); !errors.Is(err, keystore.ErrNotFound) {
				t.Errorf("the request told to wait left a record: %v", err)
			}
		})
	}
}
