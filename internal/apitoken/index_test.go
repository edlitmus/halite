package apitoken

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Redeem read and parsed every record in the store on every request, so
// a made-up bearer token, which anybody who can reach the API can send,
// cost time in proportion to the store (issue 321, M-07). The directory
// is made unlistable here: a Redeem that enumerates the store cannot work
// at all, and one that goes by the index does not notice.
func TestRedeemDoesNotListTheStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a directory that can be entered and not listed is a Unix permission; Windows has no 0300")
	}
	if os.Geteuid() == 0 {
		t.Skip("root lists a 0300 directory regardless, so this would prove nothing")
	}
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tok, secret, err := s.Issue(Options{Principal: "local:ed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Dir(), 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(s.Dir(), 0o700) })
	if _, err := os.ReadDir(s.Dir()); err == nil {
		t.Fatal("the store can still be listed; the test proves nothing")
	}

	got, err := s.Redeem(secret, "10.0.0.1:1")
	if err != nil {
		t.Fatalf("a real token, without listing the store: %v", err)
	}
	if got.ID != tok.ID {
		t.Errorf("redeemed %s, want %s", got.ID, tok.ID)
	}
	if _, err := s.Redeem("not-a-token", "10.0.0.1:1"); err == nil ||
		!strings.Contains(err.Error(), "not one this service issued") {
		t.Errorf("a made-up token, without listing the store: %v", err)
	}
}

// A token issued before the index existed has no entry, and Redeem looks
// nowhere else; the API's start puts it in.
func TestATokenIssuedBeforeTheIndexWorksAfterAReindex(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := s.Issue(Options{Principal: "local:ed"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(s.Dir(), indexDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Redeem(secret, "10.0.0.1:1"); err == nil {
		t.Fatal("redeemed with no index; the test proves nothing")
	}
	n, err := s.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("Reindex wrote %d entries, want 1", n)
	}
	if _, err := s.Redeem(secret, "10.0.0.1:1"); err != nil {
		t.Errorf("after the reindex: %v", err)
	}
	if n, _ := s.Reindex(); n != 0 {
		t.Errorf("a second Reindex wrote %d entries, want 0", n)
	}
}

// The index says where to look, not what to accept: an entry naming
// another token's record must not admit the secret as that token.
func TestAnIndexEntryNamingAnotherTokenDoesNotAdmitAsIt(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mine, secret, err := s.Issue(Options{Principal: "local:ed"})
	if err != nil {
		t.Fatal(err)
	}
	theirs, _, err := s.Issue(Options{Principal: "local:root", Roles: []string{"administrator"}})
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.indexPath(mine.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(theirs.ID), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Redeem(secret, "10.0.0.1:1")
	if err == nil && got.ID != mine.ID {
		t.Fatalf("redeemed as %s (%s) on another token's secret", got.ID, got.Principal)
	}
}

// Prune takes the index entry with the record.
func TestPruneRemovesTheIndexEntry(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.Now = func() time.Time { return now }
	tok, _, err := s.Issue(Options{Principal: "local:ed", Lifetime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(48 * time.Hour)
	if n, err := s.Prune(24 * time.Hour); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v; want 1", n, err)
	}
	path, err := s.indexPath(tok.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the index entry outlived its record: %v", err)
	}
}
