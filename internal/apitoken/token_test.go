package apitoken

import (
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The secret is returned once and never stored, so a token file that is
// read is not a set of working credentials. The same reasoning as a
// password file, for the same reason.
func TestTheSecretIsNeverStored(t *testing.T) {
	s := newStore(t)
	token, secret, err := s.Issue(Options{Principal: "local:ed", Roles: []string{"administrator"}})
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" {
		t.Fatal("no secret was returned")
	}
	if strings.Contains(token.Digest, secret) || token.Digest == secret {
		t.Error("the stored digest is the secret")
	}
	if Digest(secret) != token.Digest {
		t.Error("the digest does not match the secret")
	}

	// Read back from disk, the secret is not there.
	back, err := s.Get(token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Digest != token.Digest || strings.Contains(back.Digest, secret) {
		t.Errorf("the stored record is %+v", back)
	}

	// And it redeems.
	redeemed, err := s.Redeem(secret, "10.0.0.1:1234")
	if err != nil {
		t.Fatal(err)
	}
	if redeemed.Principal != "local:ed" {
		t.Errorf("redeemed as %q", redeemed.Principal)
	}
}

// Two tokens are two secrets: 256 bits from crypto/rand, so a token is
// not guessable from another.
func TestEveryTokenIsDistinct(t *testing.T) {
	s := newStore(t)
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		token, secret, err := s.Issue(Options{Principal: "local:ed"})
		if err != nil {
			t.Fatal(err)
		}
		if seen[secret] || seen[token.ID] {
			t.Fatal("a token repeated")
		}
		seen[secret] = true
		seen[token.ID] = true
	}
}

// A secret that was never issued is refused, and says so without
// confirming anything about the ones that were.
func TestAnUnknownSecretIsRefused(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.Issue(Options{Principal: "local:ed"}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Redeem("not-a-token", "10.0.0.1:1234")
	if err == nil {
		t.Fatal("an unknown secret was redeemed")
	}
	if _, err := s.Redeem("", "10.0.0.1:1234"); err == nil {
		t.Error("an empty secret was redeemed")
	}
}

// Both expiries, and each says which it was: "your token does not work"
// is the least useful thing an API can say at three in the morning.
func TestBothExpiriesStopATokenAndSayWhich(t *testing.T) {
	s := newStore(t)
	now := time.Unix(1_700_000_000, 0)
	s.Now = func() time.Time { return now }

	token, secret, err := s.Issue(Options{
		Principal: "local:ed", Lifetime: time.Hour, IdleFor: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !token.Live(now) {
		t.Fatal("a fresh token is not live")
	}

	// Idle first: eleven minutes without use.
	now = now.Add(11 * time.Minute)
	if _, err := s.Redeem(secret, ""); err == nil {
		t.Error("an idle token was redeemed")
	} else if !strings.Contains(err.Error(), "unused") {
		t.Errorf("the idle refusal says %q", err)
	}

	// Absolute: a token used steadily still stops at its expiry.
	_, secret2, err := s.Issue(Options{
		Principal: "local:ed", Lifetime: time.Hour, IdleFor: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		now = now.Add(20 * time.Minute)
		_, err := s.Redeem(secret2, "")
		if i < 2 && err != nil {
			t.Fatalf("a token in use was refused after %d steps: %v", i, err)
		}
	}
	if _, err := s.Redeem(secret2, ""); err == nil {
		t.Error("a token past its absolute expiry was redeemed")
	} else if !strings.Contains(err.Error(), "expired") {
		t.Errorf("the expiry refusal says %q", err)
	}
}

// A token bound to a network is refused from anywhere else.
func TestASourceBoundTokenIsRefusedElsewhere(t *testing.T) {
	s := newStore(t)
	_, secret, err := s.Issue(Options{Principal: "local:ci", SourceCIDR: "10.0.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redeem(secret, "10.0.0.7:5555"); err != nil {
		t.Errorf("a token was refused inside its network: %v", err)
	}
	if _, err := s.Redeem(secret, "192.168.1.1:5555"); err == nil {
		t.Error("a bound token was redeemed from outside its network")
	}
	if _, err := s.Redeem(secret, "not-an-address"); err == nil {
		t.Error("a bound token was redeemed from an unreadable address")
	}
	if _, _, err := s.Issue(Options{Principal: "x", SourceCIDR: "not a network"}); err == nil {
		t.Error("a token was bound to something that is not a network")
	}
}

// Revocation is individual and by principal, and the record survives so
// an audit can say what was withdrawn and when.
func TestRevocationIsIndividualAndByPrincipal(t *testing.T) {
	s := newStore(t)
	one, secret, err := s.Issue(Options{Principal: "local:ed"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Revoke(one.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redeem(secret, ""); err == nil {
		t.Error("a revoked token was redeemed")
	}
	back, err := s.Get(one.ID)
	if err != nil {
		t.Fatal("the record went with the revocation")
	}
	if !back.Revoked || back.RevokedAt.IsZero() {
		t.Errorf("the revocation was not recorded: %+v", back)
	}

	// By principal: an account being disabled means every token it
	// holds stops.
	for i := 0; i < 3; i++ {
		if _, _, err := s.Issue(Options{Principal: "local:ci"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.Issue(Options{Principal: "local:other"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.RevokePrincipal("local:ci")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("revoked %d of 3", n)
	}
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range all {
		if tok.Principal == "local:other" && tok.Revoked {
			t.Error("another principal's token was revoked")
		}
	}
}

// The roles are frozen at issue: a role added to the principal
// afterwards does not widen a token already in someone's hands.
func TestRolesAreFrozenAtIssue(t *testing.T) {
	s := newStore(t)
	roles := []string{"deployer"}
	token, secret, err := s.Issue(Options{Principal: "local:ci", Roles: roles})
	if err != nil {
		t.Fatal(err)
	}
	// Mutating the caller's slice must not reach the stored token.
	roles[0] = "administrator"

	redeemed, err := s.Redeem(secret, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(redeemed.Roles) != 1 || redeemed.Roles[0] != "deployer" {
		t.Errorf("the token's roles are %v", redeemed.Roles)
	}
	if len(token.Roles) != 1 || token.Roles[0] != "deployer" {
		t.Errorf("the issued token's roles are %v", token.Roles)
	}
}

// An identifier that is not one must not become a path.
func TestATokenIdCannotEscapeTheStore(t *testing.T) {
	s := newStore(t)
	for _, bad := range []string{"", "..", "../escape", "ZZZZ", strings.Repeat("a", 31)} {
		if _, err := s.Get(bad); err == nil {
			t.Errorf("%q was accepted as a token identifier", bad)
		}
	}
}

// Pruning drops what is past the audit's interest and keeps the rest.
func TestPruningKeepsWhatAnAuditNeeds(t *testing.T) {
	s := newStore(t)
	now := time.Unix(1_700_000_000, 0)
	s.Now = func() time.Time { return now }

	if _, _, err := s.Issue(Options{Principal: "local:old", Lifetime: time.Hour}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	if _, _, err := s.Issue(Options{Principal: "local:new", Lifetime: time.Hour}); err != nil {
		t.Fatal(err)
	}

	// Keep a day past expiry: the old one goes, the new one stays.
	n, err := s.Prune(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d", n)
	}
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Principal != "local:new" {
		t.Errorf("the store holds %d tokens: %+v", len(all), all)
	}
}
