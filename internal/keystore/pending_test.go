package keystore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func enrolPending(t *testing.T, a *Authority, nodeID string) {
	t.Helper()
	csr, _ := request(t, nodeID)
	if _, err := a.Enroll(Request{CSR: csr}); !errors.Is(err, ErrPending) {
		t.Fatalf("enrolling %s: %v", nodeID, err)
	}
}

func markers(t *testing.T, s *Store) []string {
	t.Helper()
	names, err := s.pendingMarkers(0)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	return names
}

// **A new name does not read the store.** Enroll asks whether there is
// room for one more pending record before it writes one, and it used to
// answer by reading and parsing every record under the store's lock, so
// each identity cost the store's size: `simfleet setup` slowed in a
// straight line, 20 to 93 seconds per thousand (DIVERGENCE 5.279, 5.632).
//
// Timing that would be a flaky test, so this one makes every record that
// is not pending unreadable instead. The count never needed them, and a
// version that reads them fails with the parse error.
func TestANewNameDoesNotReadTheOtherRecords(t *testing.T) {
	a := filledStore(t, 50)
	a.MaxPending = 2
	enrolPending(t, a, "first.example")

	entries, err := os.ReadDir(a.Store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	spoiled := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "old") && strings.HasSuffix(e.Name(), ".json") {
			if err := os.WriteFile(filepath.Join(a.Store.Dir(), e.Name()), []byte("not a record"), 0o600); err != nil {
				t.Fatal(err)
			}
			spoiled++
		}
	}
	if spoiled != 50 {
		t.Fatalf("spoiled %d records, want 50", spoiled)
	}

	enrolPending(t, a, "second.example")

	// At the limit the count is checked record by record -- but only the
	// pending records, so the refusal is the bound's and not the parse
	// error of a record it had no reason to open.
	over, _ := request(t, "third.example")
	_, err = a.Enroll(Request{CSR: over})
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "2 enrollment requests are already pending") {
		t.Fatalf("the third pending request: %v", err)
	}
}

// **A marker left behind is an overcount, and an overcount refuses
// nothing.** A crash after a record stops being pending and before its
// marker is removed, or before a deleted record's is, leaves a marker for
// a node that is not pending. Below the limit that is harmless; at the
// limit the markers are checked against their records, the stale ones go,
// and the decision is made on the true number.
func TestAStaleMarkerDoesNotRefuseANewName(t *testing.T) {
	a, _ := newAuthority(t)
	a.MaxPending = 2
	enrolPending(t, a, "p1.example")
	enrolPending(t, a, "done.example")
	if _, err := a.Accept("done.example"); err != nil {
		t.Fatal(err)
	}
	// What the crash leaves: markers for an accepted record and for one
	// that was deleted.
	for _, id := range []string{"done.example", "gone.example"} {
		if err := writeAtomic(filepath.Join(a.Store.pendingDir(), id), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	enrolPending(t, a, "p2.example")
	if got, want := markers(t, a.Store), []string{"p1.example", "p2.example"}; !slices.Equal(got, want) {
		t.Errorf("the index after the check at the limit holds %v, want %v", got, want)
	}
	over, _ := request(t, "p3.example")
	if _, err := a.Enroll(Request{CSR: over}); !errors.Is(err, ErrRefused) {
		t.Errorf("a third pending request at a limit of two: %v", err)
	}
}

// **Every way out of pending takes the marker with it**, or the index
// would only ever grow and every new name would pay the check at the
// limit.
func TestDecidingARequestRemovesItsMarker(t *testing.T) {
	a, _ := newAuthority(t)
	for _, id := range []string{"accept.example", "reject.example", "delete.example", "stays.example"} {
		enrolPending(t, a, id)
	}
	if got := markers(t, a.Store); len(got) != 4 {
		t.Fatalf("four pending requests left markers %v", got)
	}
	if _, err := a.Accept("accept.example"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Reject("reject.example", "no"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Delete("delete.example"); err != nil {
		t.Fatal(err)
	}
	if got, want := markers(t, a.Store), []string{"stays.example"}; !slices.Equal(got, want) {
		t.Errorf("after an accept, a reject and a delete the index holds %v, want %v", got, want)
	}
}

// **A store written before the index is indexed from its records**, once,
// and the leftovers of an interrupted build do not count.
//
// Throwing the build directory away is tidiness rather than correctness,
// and this test does not tell the two apart: reusing it (tried, on
// purpose) passes too, because a rebuild writes a marker for every
// pending record anyway and a leftover marker for anything else is an
// overcount, which the check at the limit removes. What is load-bearing
// is that a build is renamed into place only when it is complete, so a
// crash mid-build never leaves an index missing markers; showing that
// needs a crash, and nothing here makes one.
func TestAStoreWithoutAnIndexIsIndexedFromItsRecords(t *testing.T) {
	a, _ := newAuthority(t)
	a.MaxPending = 2
	now := time.Now()
	for i := range 2 {
		if err := a.Store.Put(&Record{NodeID: fmt.Sprintf("before%d.example", i), State: Pending, Source: SourceManual,
			Fingerprint: "SHA256:x", FirstSeen: now, Updated: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(a.Store.pendingDir()); err == nil {
		t.Fatal("the index exists before anything has counted")
	}
	// An interrupted build, holding a marker for nothing.
	build := filepath.Join(a.Store.Dir(), pendingBuildName)
	if err := os.MkdirAll(build, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "phantom.example"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	over, _ := request(t, "new.example")
	if _, err := a.Enroll(Request{CSR: over}); !errors.Is(err, ErrRefused) {
		t.Fatalf("two pending records written before the index, at a limit of two: %v", err)
	}
	if got, want := markers(t, a.Store), []string{"before0.example", "before1.example"}; !slices.Equal(got, want) {
		t.Errorf("the built index holds %v, want %v", got, want)
	}
	if _, err := os.Stat(build); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the interrupted build is still there: %v", err)
	}
}
