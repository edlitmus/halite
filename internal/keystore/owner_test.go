//go:build unix

package keystore

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A record written by root into a store owned by the service account
// must belong to that account, or the hub cannot read what the operator
// just accepted.
//
// `halite-hub keys accept` runs as root and `halite-hub serve` runs as
// an unprivileged account. Without this the accepted record was written
// owned by root, and the node's next enrollment failed with
// `/v1/enroll: reading the record for mail.example: permission denied` —
// from the hub, about a file the operator could see perfectly well.
func TestARecordWrittenAsRootBelongsToTheStore(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can change a file's owner, which is the case under test")
	}
	dir := t.TempDir()
	// Someone other than root owns the store, as the service account
	// does in an installed estate.
	const uid, gid = 65534, 65534
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Skipf("cannot chown the store: %v", err)
	}

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(&Record{
		NodeID: "mail.example", State: Accepted,
		Fingerprint: "aa", NotAfter: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, "mail.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no uid on this platform")
	}
	if int(st.Uid) != uid {
		t.Errorf("the record is owned by uid %d; the store belongs to %d, "+
			"so the service cannot read it", st.Uid, uid)
	}
}

// And a store this process already owns is left exactly as it was: the
// helper only ever hands ownership downward.
func TestAnOrdinaryWriteDoesNotChangeOwnership(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(&Record{
		NodeID: "web1.example", State: Pending, Fingerprint: "bb",
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "web1.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != os.Geteuid() {
			t.Errorf("the record changed owner to %d for no reason", st.Uid)
		}
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the record is mode %v, want 0600", info.Mode().Perm())
	}
}

// chownLog stands in for chown, and for root: it records who the process
// asked to hand each path to, so what only root can do is checked by a test
// that is not root.
type chownLog struct{ calls map[string][2]int }

func fakeRoot(t *testing.T) *chownLog {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root, where the store's real owner is root; the real-root test covers this")
	}
	l := &chownLog{calls: map[string][2]int{}}
	oldE, oldC := geteuid, chown
	geteuid = func() int { return 0 }
	chown = func(path string, uid, gid int) error {
		l.calls[path] = [2]int{uid, gid}
		return nil
	}
	t.Cleanup(func() { geteuid, chown = oldE, oldC })
	return l
}

// `halite-hub keys token create` run as root made state_dir/keys/tokens as
// root:root, inside the hub account's keys directory, and every token written
// into it took that owner from it: the hub read none of them, and every
// token enrolment was refused with nothing on the node but "the hub's log
// says why". The directory has to be handed to the account that owns its
// parent, the way the files in it already were. #317, DIVERGENCE 5.285.
func TestTheTokenDirectoryBelongsToTheStoreWhenRootCreatesIt(t *testing.T) {
	l := fakeRoot(t)
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.MintToken(TokenOptions{TTL: time.Hour}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := [2]int{os.Geteuid(), os.Getegid()} // the parent's owner: this test's user
	tokens := filepath.Join(dir, "keys", "tokens")
	if got, ok := l.calls[tokens]; !ok || got != want {
		t.Errorf("the token directory was not handed to %v: chown calls %v", want, l.calls)
	}
	if got, ok := l.calls[filepath.Join(dir, "keys")]; !ok || got != want {
		t.Errorf("the store directory was not handed to its parent's owner: %v", l.calls)
	}
	var file string
	for p := range l.calls {
		if filepath.Dir(p) == tokens {
			file = p
		}
	}
	if file == "" {
		t.Errorf("the token %s was written without being handed on: %v", tok.ID, l.calls)
	}
}

// A tokens directory an earlier run left owned by root is put right the next
// time root touches it, so the fix reaches stores that are already wrong.
func TestAWronglyOwnedTokenDirectoryIsPutRight(t *testing.T) {
	l := fakeRoot(t)
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if err := os.MkdirAll(filepath.Join(keys, "tokens"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(keys)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MintToken(TokenOptions{TTL: time.Hour}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.calls[filepath.Join(keys, "tokens")]; !ok {
		t.Errorf("an existing tokens directory was not put right: %v", l.calls)
	}
}

// The same, for real: only root can hand a file to someone else, so this
// runs where it can (`sudo ./keystore.test -test.run RealRoot`).
func TestRealRootLeavesTheTokenStoreToItsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can change a file's owner, which is the case under test")
	}
	dir := t.TempDir()
	const uid, gid = 65534, 65534
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Skipf("cannot chown the state directory: %v", err)
	}
	s, err := Open(filepath.Join(dir, "keys"))
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.MintToken(TokenOptions{TTL: time.Hour}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(dir, "keys"),
		filepath.Join(dir, "keys", "tokens"),
		filepath.Join(dir, "keys", "tokens", tok.ID+".json"),
	} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && (int(st.Uid) != uid || int(st.Gid) != gid) {
			t.Errorf("%s belongs to %d:%d; the hub account is %d:%d and cannot read it", p, st.Uid, st.Gid, uid, gid)
		}
	}
}

// What the hub checks at start in token mode: a store it cannot read is
// reported, with the reason, rather than discovered one refused node at a time.
func TestAnUnreadableTokenStoreIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything, so the case cannot be made")
	}
	s, err := Open(filepath.Join(t.TempDir(), "keys"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CheckTokens(); err != nil {
		t.Fatalf("an empty store: %v", err)
	}
	if _, _, err := s.MintToken(TokenOptions{TTL: time.Hour}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckTokens(); err != nil {
		t.Fatalf("a readable store: %v", err)
	}
	tokens := filepath.Join(s.Dir(), "tokens")
	if err := os.Chmod(tokens, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(tokens, 0o700) })
	err = s.CheckTokens()
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("an unreadable store gave %v; want permission denied named", err)
	}
}
