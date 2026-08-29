//go:build unix

package keystore

import (
	"os"
	"path/filepath"
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
