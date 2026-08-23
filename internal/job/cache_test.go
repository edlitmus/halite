package job

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newCache(t *testing.T) *Cache {
	t.Helper()
	c, err := OpenCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func dispatched(t *testing.T, c *Cache, at time.Time, nodes ...string) *Job {
	t.Helper()
	j := &Job{
		JID:     NewID(at),
		Fun:     "test.ping",
		Nonce:   mustNonce(t),
		Created: at,
		Expires: at.Add(DefaultTTL),
		Nodes:   nodes,
		State:   Dispatched,
	}
	if err := c.Put(j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestAReturnIsFiledOnceHoweverOftenItArrives(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example", "web2.example")

	r := &Return{JID: j.JID, NodeID: "web1.example", Fun: j.Fun, Success: true, Schema: ReturnSchema}
	first, err := c.AddReturn(r)
	if err != nil || !first {
		t.Fatalf("the first return was not filed: %v %v", first, err)
	}
	// The node lost the acknowledgement and tried again.
	again, err := c.AddReturn(r)
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Error("a retry was filed as a second return")
	}
	returns, err := c.Returns(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(returns) != 1 {
		t.Fatalf("%d returns after a retry", len(returns))
	}

	// The node set was recorded before delivery, so the hub can say who
	// has not answered.
	missing, err := c.Missing(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "web2.example" {
		t.Errorf("missing = %v", missing)
	}
}

func TestAReturnForAJobTheHubNeverDispatchedIsRefused(t *testing.T) {
	c := newCache(t)
	r := &Return{JID: NewID(time.Now()), NodeID: "web1.example", Schema: ReturnSchema}
	if _, err := c.AddReturn(r); !errors.Is(err, ErrNoJob) {
		t.Errorf("a return against an unknown job gave %v", err)
	}
}

// The jid and the node identity both become path segments.
func TestTheCacheRefusesAnIdentifierThatIsAPath(t *testing.T) {
	c := newCache(t)
	if _, err := c.Get("../../etc/passwd"); err == nil || errors.Is(err, ErrNoJob) {
		t.Error("a path was accepted as a job identifier")
	}
	j := dispatched(t, c, time.Now(), "web1.example")
	for _, bad := range []string{"../escape", "a/b", "..", ""} {
		r := &Return{JID: j.JID, NodeID: bad, Schema: ReturnSchema}
		if _, err := c.AddReturn(r); err == nil {
			t.Errorf("%q was accepted as a node identity", bad)
		}
	}
}

func TestListIsNewestFirstAcrossDays(t *testing.T) {
	c := newCache(t)
	base := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	var ids []ID
	for i := 0; i < 5; i++ {
		j := dispatched(t, c, base.Add(time.Duration(i)*30*time.Hour), "web1.example")
		ids = append(ids, j.JID)
	}
	got, err := c.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("%d jobs listed", len(got))
	}
	for i, j := range got {
		want := ids[len(ids)-1-i]
		if j.JID != want {
			t.Fatalf("position %d is %s, want %s", i, j.JID, want)
		}
	}
	if limited, err := c.List(2); err != nil || len(limited) != 2 {
		t.Errorf("limit 2 returned %d (%v)", len(limited), err)
	}
}

// Salt's local_cache grows until the disk is full. This one does not. // lexicon:allow
func TestRetentionBindsByAgeAndBySize(t *testing.T) {
	c := newCache(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	c.Now = func() time.Time { return now }

	old := dispatched(t, c, now.Add(-72*time.Hour), "web1.example")
	recent := dispatched(t, c, now.Add(-time.Hour), "web1.example")

	c.Retention = 48 * time.Hour
	removed, err := c.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("pruned %d jobs, want 1", removed)
	}
	if _, err := c.Get(old.JID); !errors.Is(err, ErrNoJob) {
		t.Error("the job past its retention is still there")
	}
	if _, err := c.Get(recent.JID); err != nil {
		t.Errorf("the recent job was pruned: %v", err)
	}

	// An empty day directory left behind reads as a day with jobs in it.
	entries, _ := os.ReadDir(c.Dir())
	for _, e := range entries {
		inner, _ := os.ReadDir(filepath.Join(c.Dir(), e.Name()))
		if len(inner) == 0 {
			t.Errorf("%s was left empty", e.Name())
		}
	}

	// Size binds even when age does not.
	c.Retention = 0
	for i := 0; i < 5; i++ {
		dispatched(t, c, now.Add(time.Duration(i)*time.Minute), "web1.example")
	}
	c.MaxBytes = 1
	if _, err := c.Prune(); err != nil {
		t.Fatal(err)
	}
	left, err := c.List(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > 1 {
		t.Errorf("%d jobs survived a one-byte ceiling", len(left))
	}
}
