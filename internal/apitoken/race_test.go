package apitoken

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Two Stores on one directory are what the API and `halite-api token
// revoke` are: two processes with nothing in common but the files. The
// tests below use a second Store rather than a second goroutine on the
// same one, so that nothing in-process -- a mutex added later, say --
// can make them pass while the cross-process race is still open.
func twoStores(t *testing.T) (*Store, *Store) {
	t.Helper()
	dir := t.TempDir()
	a, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return a, b
}

// A revocation that lands while a request is being authenticated --
// after Redeem read the record and before it wrote the idle clock --
// must still be in force afterwards. Before DIVERGENCE 5.303, Redeem
// wrote back the whole record it had read, with Revoked false, and the
// revoked token worked again.
func TestARevocationDuringARedeemIsNotUndone(t *testing.T) {
	api, cli := twoStores(t)
	tok, secret, err := api.Issue(Options{Principal: "local:ed"})
	if err != nil {
		t.Fatal(err)
	}

	fired := false
	api.between = func(op, id string) {
		if op != "redeem" || id != tok.ID {
			return
		}
		fired = true
		if _, err := cli.Revoke(id); err != nil {
			t.Fatalf("revoking in the window: %v", err)
		}
	}
	// The redeem that was already under way when the revocation landed
	// may succeed: it began before the token was withdrawn.
	if _, err := api.Redeem(secret, "10.0.0.1:1"); err != nil {
		t.Fatalf("the redeem in flight: %v", err)
	}
	if !fired {
		t.Fatal("the window was never entered, so this test proved nothing")
	}
	api.between = nil

	back, err := cli.Get(tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Revoked {
		t.Fatal("the redeem in flight wrote the token back unrevoked")
	}
	// And every request after it is refused.
	if _, err := api.Redeem(secret, "10.0.0.1:1"); err == nil {
		t.Fatal("a revoked token was redeemed")
	}
}

// The other order: the revocation has read the record, and a request
// is authenticated before it writes. The revocation's write must win,
// and the request's idle clock must not be lost to it either.
func TestARedeemDuringARevocationDoesNotUndoIt(t *testing.T) {
	api, cli := twoStores(t)
	base := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	api.Now = func() time.Time { return base.Add(time.Hour) }
	cli.Now = func() time.Time { return base.Add(2 * time.Hour) }
	issuer := &Store{dir: api.dir, Now: func() time.Time { return base }}
	tok, secret, err := issuer.Issue(Options{Principal: "local:ed"})
	if err != nil {
		t.Fatal(err)
	}

	fired := false
	cli.between = func(op, id string) {
		if op != "revoke" || id != tok.ID {
			return
		}
		fired = true
		if _, err := api.Redeem(secret, "10.0.0.1:1"); err != nil {
			t.Fatalf("redeeming in the window: %v", err)
		}
	}
	if _, err := cli.Revoke(tok.ID); err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("the window was never entered, so this test proved nothing")
	}
	cli.between = nil

	back, err := api.Get(tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Revoked {
		t.Fatal("the revocation did not hold")
	}
	if !back.LastUsed.Equal(base.Add(time.Hour)) {
		t.Errorf("the idle clock is %v, want the redeem's %v", back.LastUsed, base.Add(time.Hour))
	}
	if _, err := api.Redeem(secret, "10.0.0.1:1"); err == nil {
		t.Fatal("a revoked token was redeemed")
	}
}

// Redeem must not write the record at all. The two tests above prove
// the consequence; this pins the cause, so that a change which reopens
// it is named by the failure rather than inferred from it.
func TestRedeemDoesNotRewriteTheRecord(t *testing.T) {
	s := newStore(t)
	tok, secret, err := s.Issue(Options{Principal: "local:ed"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, tok.ID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	later := tok.Issued.Add(time.Minute)
	s.Now = func() time.Time { return later }
	if _, err := s.Redeem(secret, "10.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("redeem rewrote the record:\nbefore %s\nafter  %s", before, after)
	}
	// The idle clock still moved, and the idle expiry still reads it.
	back, err := s.Get(tok.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.LastUsed.Equal(later) {
		t.Errorf("last used is %v, want %v", back.LastUsed, later)
	}
}

// Pruning a token takes its idle clock with it.
func TestPruneRemovesTheIdleClock(t *testing.T) {
	s := newStore(t)
	base := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return base }
	tok, secret, err := s.Issue(Options{Principal: "local:ed", Lifetime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redeem(secret, "10.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	clock := filepath.Join(s.dir, tok.ID+lastUsedSuffix)
	if _, err := os.Stat(clock); err != nil {
		t.Fatalf("no idle clock was written: %v", err)
	}
	s.Now = func() time.Time { return base.Add(48 * time.Hour) }
	if n, err := s.Prune(time.Hour); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if _, err := os.Stat(clock); !os.IsNotExist(err) {
		t.Errorf("the idle clock outlived its record: %v", err)
	}
}
