package job

import (
	"errors"
	"testing"
	"time"
)

func TestIdentifiersAreSortableReadableAndDistinct(t *testing.T) {
	at := time.Date(2026, 8, 22, 14, 22, 11, 123456000, time.UTC)
	id := NewID(at)
	if string(id) != "20260822T142211123456" {
		t.Fatalf("jid = %q, want the layout of SPEC 6.3", id)
	}
	if !id.Valid() {
		t.Error("a jid this package produced does not validate")
	}
	if id.Day() != "20260822" {
		t.Errorf("day = %q", id.Day())
	}
	back, err := id.Time()
	if err != nil {
		t.Fatal(err)
	}
	if !back.Equal(at) {
		t.Errorf("the jid round trips to %s, not %s", back, at)
	}

	// A jid is a key and a path segment, so what arrives from the
	// network is checked rather than trusted.
	for _, bad := range []string{"", "../../etc/passwd", "20260822T14221112345", "not-a-jid-at-all!!!!!!", "20261322T142211123456"} {
		if ID(bad).Valid() {
			t.Errorf("%q was accepted as a job identifier", bad)
		}
	}
}

// Two jobs in the same microsecond would share a key otherwise, and the
// key is what the job cache, the replay cache, and the return are all
// filed under.
func TestTheClockNeverRepeatsAndNeverGoesBackwards(t *testing.T) {
	frozen := time.Date(2026, 8, 22, 14, 22, 11, 0, time.UTC)
	clock := &Clock{Now: func() time.Time { return frozen }}

	seen := map[ID]bool{}
	var last ID
	for i := 0; i < 1000; i++ {
		id := clock.Next()
		if seen[id] {
			t.Fatalf("%s was issued twice", id)
		}
		if last != "" && id <= last {
			t.Fatalf("%s does not follow %s", id, last)
		}
		seen[id] = true
		last = id
	}

	// The system clock going backwards must not produce a jid that
	// collides with one already issued.
	clock.Now = func() time.Time { return frozen.Add(-time.Hour) }
	id := clock.Next()
	if id <= last {
		t.Errorf("a backwards clock produced %s, which is not after %s", id, last)
	}
}

func TestAJobRunsOnceAndNotAfterItExpires(t *testing.T) {
	now := time.Date(2026, 8, 22, 14, 0, 0, 0, time.UTC)
	guard := NewGuard(16)
	guard.Now = func() time.Time { return now }

	nonce, err := Nonce()
	if err != nil {
		t.Fatal(err)
	}
	j := &Job{JID: NewID(now), Fun: "test.ping", Nonce: nonce, Expires: now.Add(DefaultTTL)}

	if err := guard.Admit(j); err != nil {
		t.Fatalf("a fresh job was refused: %v", err)
	}
	err = guard.Admit(j)
	if err == nil {
		t.Fatal("the same job ran twice")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("a replay should be a refusal: %v", err)
	}
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Reason != ReasonReplayed {
		t.Errorf("the refusal is %v, want a replay", err)
	}

	// A different jid carrying a nonce this node has seen is the same
	// attack wearing a hat.
	reworn := &Job{JID: NewID(now.Add(time.Second)), Fun: "test.ping", Nonce: nonce, Expires: now.Add(DefaultTTL)}
	if err := guard.Admit(reworn); err == nil {
		t.Error("a replayed nonce under a new jid was admitted")
	}

	// Expiry.
	stale := &Job{JID: NewID(now.Add(2 * time.Second)), Fun: "test.ping", Nonce: mustNonce(t), Expires: now.Add(-time.Second)}
	err = guard.Admit(stale)
	if !errors.As(err, &refusal) || refusal.Reason != ReasonExpired {
		t.Errorf("an expired job gave %v", err)
	}

	// Malformed.
	for _, bad := range []*Job{
		nil,
		{Fun: "test.ping"},
		{JID: NewID(now), Fun: "test.ping"},
		{JID: "nonsense", Fun: "test.ping", Nonce: mustNonce(t)},
	} {
		if err := guard.Admit(bad); err == nil {
			t.Errorf("%+v was admitted", bad)
		}
	}
}

// A cache that grows with every job is a node that runs out of memory
// after a long enough uptime.
func TestTheReplayCacheIsBounded(t *testing.T) {
	now := time.Date(2026, 8, 22, 14, 0, 0, 0, time.UTC)
	guard := NewGuard(8)
	for i := 0; i < 500; i++ {
		at := now.Add(time.Duration(i) * time.Second)
		guard.Now = func() time.Time { return at }
		j := &Job{JID: NewID(at), Fun: "test.ping", Nonce: mustNonce(t), Expires: at.Add(time.Hour)}
		if err := guard.Admit(j); err != nil {
			t.Fatal(err)
		}
	}
	// Two keys per job, and the bound is on the pair.
	if got := guard.Len(); got > 8*2 {
		t.Errorf("the cache holds %d keys after 500 jobs; the bound is %d", got, 8*2)
	}
}

func mustNonce(t *testing.T) string {
	t.Helper()
	n, err := Nonce()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestABatchSizeIsResolvedAgainstTheMatchedSet(t *testing.T) {
	cases := []struct {
		spec    string
		matched int
		want    int
		bad     bool
	}{
		{"", 100, 0, false},
		{"10", 100, 10, false},
		{"25%", 100, 25, false},
		{"50%", 5, 2, false},
		// A percentage that rounds to zero becomes one: an operator
		// who wrote 1% of fifty meant "a few at a time", not "none",
		// and zero would mean the whole estate.
		{"1%", 50, 1, false},
		{"100%", 8, 8, false},
		{"0", 10, 0, true},
		{"-3", 10, 0, true},
		{"0%", 10, 0, true},
		{"200%", 10, 0, true},
		{"lots", 10, 0, true},
	}
	for _, c := range cases {
		got, err := ParseBatchSize(c.spec, c.matched)
		if c.bad {
			if err == nil {
				t.Errorf("--batch %q was accepted as %d", c.spec, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("--batch %q: %v", c.spec, err)
			continue
		}
		if got != c.want {
			t.Errorf("--batch %q of %d = %d, want %d", c.spec, c.matched, got, c.want)
		}
	}
}

// A batch's record is what makes it resumable, so Remaining has to be
// exactly the nodes that were never delivered to.
func TestRemainingIsWhatTheBatchHasNotReached(t *testing.T) {
	j := &Job{
		Nodes:     []string{"a", "b", "c", "d"},
		Delivered: []string{"a", "b"},
		Batch:     Batch{Size: 2},
	}
	if got := j.Remaining(); len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Errorf("remaining = %v", got)
	}
	if !j.Batched() {
		t.Error("a size below the node count is a batch")
	}
	// A batch as large as the fleet is not a batch, and treating it as
	// one would put a job through the staging machinery for nothing.
	if (&Job{Nodes: []string{"a", "b"}, Batch: Batch{Size: 2}}).Batched() {
		t.Error("a batch covering every node is not a batch")
	}
	if (&Job{Nodes: []string{"a", "b"}}).Batched() {
		t.Error("no batch size is not a batch")
	}
	// Delivered to everything.
	j.Delivered = []string{"a", "b", "c", "d"}
	if got := j.Remaining(); len(got) != 0 {
		t.Errorf("remaining = %v after reaching everything", got)
	}
}
