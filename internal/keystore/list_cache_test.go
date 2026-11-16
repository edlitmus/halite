package keystore

import (
	"fmt"
	"testing"
	"time"
)

// The tests below wait out listCacheMargin, the production value, before
// relying on a kept snapshot. A shorter margin would be unsound on a
// filesystem that keeps whole seconds, and these run on every CI leg.

func putAccepted(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.Put(&Record{NodeID: id, State: Accepted, Fingerprint: "aa",
		NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}

func listIDs(t *testing.T, s *Store) map[string]State {
	t.Helper()
	recs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]State{}
	for _, r := range recs {
		out[r.NodeID] = r.State
	}
	return out
}

func reads(s *Store) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listReads
}

// While nothing changes, a second List reads no record files: the hub
// calls List for every job it dispatches (DIVERGENCE 5.669).
func TestListReadsTheRecordsOnceWhileNothingChanges(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		putAccepted(t, s, fmt.Sprintf("n%02d.example", i))
	}
	time.Sleep(listCacheMargin + 200*time.Millisecond)
	first := listIDs(t, s)
	after := reads(s)
	second := listIDs(t, s)
	if len(first) != 20 || len(second) != 20 {
		t.Fatalf("listed %d then %d records; want 20 each time", len(first), len(second))
	}
	if after != 20 || reads(s) != 20 {
		t.Fatalf("List read %d record files the first time and %d in all; want 20 and 20, the second List reading none",
			after, reads(s))
	}
}

// What another process writes is seen at once: `halite-hub keys`
// accepts, revokes and deletes in this directory while the hub serves,
// and a hub that kept dispatching to a revoked node, or could not target
// a node just accepted, would be wrong in both directions.
func TestListSeesWhatAnotherStoreWrites(t *testing.T) {
	dir := t.TempDir()
	hub, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		putAccepted(t, hub, fmt.Sprintf("n%02d.example", i))
	}
	time.Sleep(listCacheMargin + 200*time.Millisecond)
	if got := listIDs(t, hub); len(got) != 5 {
		t.Fatalf("listed %v; want 5 records", got)
	}

	// Each change straight after a List that kept its snapshot.
	putAccepted(t, cli, "new.example")
	if got := listIDs(t, hub); got["new.example"] != Accepted || len(got) != 6 {
		t.Fatalf("after another store accepted new.example, List gave %v", got)
	}
	time.Sleep(listCacheMargin + 200*time.Millisecond)
	listIDs(t, hub)
	rec, err := cli.Get("n00.example")
	if err != nil {
		t.Fatal(err)
	}
	rec.State = Revoked
	if err := cli.Put(rec); err != nil {
		t.Fatal(err)
	}
	if got := listIDs(t, hub); got["n00.example"] != Revoked {
		t.Fatalf("after another store revoked n00.example, List gave it as %q", got["n00.example"])
	}
	time.Sleep(listCacheMargin + 200*time.Millisecond)
	listIDs(t, hub)
	if err := cli.Delete("n01.example"); err != nil {
		t.Fatal(err)
	}
	if got := listIDs(t, hub); len(got) != 5 {
		t.Fatalf("after another store deleted n01.example, List gave %v", got)
	}
	if _, ok := listIDs(t, hub)["n01.example"]; ok {
		t.Fatal("after another store deleted n01.example, List still gave it")
	}
}

// A List straight after a write keeps nothing: the write's mtime is too
// close to the clock for a later write in the same tick to be told
// apart from it, which is what listCacheMargin is for.
func TestListKeepsNothingReadTooSoonAfterAWrite(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		putAccepted(t, s, fmt.Sprintf("n%02d.example", i))
	}
	listIDs(t, s)
	listIDs(t, s)
	if got := reads(s); got != 6 {
		t.Fatalf("two Lists straight after a write read %d record files; want 6, each reading all three", got)
	}
}

// A caller that changes a record it was given does not change what the
// next caller gets.
func TestListHandsOutCopies(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	putAccepted(t, s, "a.example")
	time.Sleep(listCacheMargin + 200*time.Millisecond)
	listIDs(t, s)         // reads the records and keeps them
	recs, err := s.List() // answered from what was kept
	if err != nil {
		t.Fatal(err)
	}
	if reads(s) != 1 {
		t.Fatalf("List read %d record files; want 1, the second List answered from what the first kept", reads(s))
	}
	recs[0].State = Revoked
	recs[0].Superseded = append(recs[0].Superseded, SupersededCert{Serial: "x"})
	again, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if again[0].State != Accepted || len(again[0].Superseded) != 0 {
		t.Fatalf("a caller's change reached the next List: %+v", again[0])
	}
}
