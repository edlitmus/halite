package job

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func ret(j *Job, node string) *Return {
	return &Return{JID: j.JID, NodeID: node, Fun: "test.ping", Success: true}
}

func file(t *testing.T, c *Cache, r *Return) Filing {
	t.Helper()
	f, err := c.File(r)
	if err != nil {
		t.Fatalf("filing the return from %s: %v", r.NodeID, err)
	}
	return f
}

// The count says how many targets are still owed, and a retry neither
// counts nor uncounts.
func TestFilingCountsTheTargetsStillOwed(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "a.example", "b.example", "c.example")
	j.Correlation = "chain-1"
	if err := c.Put(j); err != nil {
		t.Fatal(err)
	}

	for i, node := range []string{"b.example", "a.example", "c.example"} {
		f := file(t, c, ret(j, node))
		if !f.Fresh || f.Remaining != 2-i {
			t.Fatalf("return %d from %s: fresh %v, remaining %d; want fresh, %d",
				i+1, node, f.Fresh, f.Remaining, 2-i)
		}
		if f.Fun != "test.ping" || f.Correlation != "chain-1" {
			t.Errorf("the filing says fun %q, correlation %q: not the record's", f.Fun, f.Correlation)
		}
	}
	if f := file(t, c, ret(j, "a.example")); f.Fresh || f.Remaining != 0 {
		t.Errorf("a retry from a.example: fresh %v, remaining %d; want a stale filing with 0 owed",
			f.Fresh, f.Remaining)
	}
}

// What AddReturn refused before, File refuses the same way.
func TestFilingRefusesWhatAddReturnRefused(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "a.example")

	if _, err := c.File(ret(j, "stranger.example")); !errors.Is(err, ErrNotTargeted) {
		t.Errorf("a return from a node the job was not sent to: %v, want ErrNotTargeted", err)
	}
	// The job is real and still owed a return, so it keeps its index.
	if n := indexed(c); n != 1 {
		t.Errorf("%d indexes held after a refusal against one job in flight, want 1", n)
	}
	if _, err := c.File(&Return{JID: NewID(time.Now().Add(time.Hour)), NodeID: "a.example"}); !errors.Is(err, ErrNoJob) {
		t.Errorf("a return against no job: %v, want ErrNoJob", err)
	}
	if _, err := c.File(&Return{JID: j.JID, NodeID: "a.example", Chunk: 1}); err == nil {
		t.Error("a chunk-1 return was accepted")
	}
	// A jid with no job leaves no index behind: otherwise every guessed
	// identifier is a map entry for the life of the hub.
	if n := indexed(c); n != 1 {
		t.Errorf("%d indexes held after a return against no job, want still 1", n)
	}
}

// A hub that restarts mid-job counts the returns it filed before.
//
// The count is in memory and the returns are on disk; this is what holds
// the first to the second. Without it a restarted hub would wait for
// returns it already has, and the job would never complete.
func TestARestartedCacheCountsTheReturnsAlreadyFiled(t *testing.T) {
	dir := t.TempDir()
	before, err := OpenCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	j := dispatched(t, before, time.Now(), "a.example", "b.example", "c.example")
	file(t, before, ret(j, "a.example"))
	file(t, before, ret(j, "b.example"))

	after, err := OpenCache(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f := file(t, after, ret(j, "a.example")); f.Fresh || f.Remaining != 1 {
		t.Errorf("a retry after the restart: fresh %v, remaining %d; want stale, 1 owed", f.Fresh, f.Remaining)
	}
	if f := file(t, after, ret(j, "c.example")); !f.Fresh || f.Remaining != 0 {
		t.Errorf("the last return after the restart: fresh %v, remaining %d; want fresh, 0 owed", f.Fresh, f.Remaining)
	}
}

// Every node answers at once, and most of them twice. Exactly one filing
// takes the count to zero, every node is fresh exactly once, and the
// filing that reaches zero sees nothing missing.
//
// The last is the ordering File's comment describes: the count is taken
// after the file is written. Decrement first and the filing that reaches
// zero can ask Missing while another node's file is still being written,
// be told that node is missing, and the job is never marked complete,
// because the other filing saw a count above zero and did not ask.
func TestConcurrentReturnsReachZeroOnceWithNothingMissing(t *testing.T) {
	const n = 200
	c := newCache(t)
	nodes := make([]string, n)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%03d.example", i)
	}
	j := dispatched(t, c, time.Now(), nodes...)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		fresh    = map[string]int{}
		zeros    int
		problems []string
	)
	for _, node := range nodes {
		for range 2 { // the return and its retry, racing
			wg.Go(func() {
				f, err := c.File(ret(j, node))
				var missing []string
				if err == nil && f.Fresh && f.Remaining == 0 {
					missing, err = c.Missing(j.JID)
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					problems = append(problems, err.Error())
					return
				}
				if f.Fresh {
					fresh[node]++
					if f.Remaining == 0 {
						zeros++
						if len(missing) > 0 {
							problems = append(problems, fmt.Sprintf(
								"the filing that reached zero found %d missing: %v", len(missing), missing))
						}
					}
				}
			})
		}
	}
	wg.Wait()
	for _, p := range problems {
		t.Error(p)
	}
	if zeros != 1 {
		t.Errorf("%d filings reached zero, want exactly 1", zeros)
	}
	for _, node := range nodes {
		if fresh[node] != 1 {
			t.Errorf("%s was fresh %d times, want 1", node, fresh[node])
		}
	}
}

// A finished job's index is dropped, and a late retry rebuilds it from
// the directory and is still recognised as a retry.
func TestAFinishedJobsIndexIsDroppedAndRebuilt(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "a.example", "b.example")
	file(t, c, ret(j, "a.example"))
	if n := indexed(c); n != 1 {
		t.Fatalf("%d indexes held with a job in flight, want 1", n)
	}
	file(t, c, ret(j, "b.example"))
	if n := indexed(c); n != 0 {
		t.Errorf("%d indexes held after the only job completed, want 0", n)
	}
	if f := file(t, c, ret(j, "b.example")); f.Fresh || f.Remaining != 0 {
		t.Errorf("a late retry: fresh %v, remaining %d; want stale, 0 owed", f.Fresh, f.Remaining)
	}
}

// Jobs that never complete do not each hold an index for ever.
func TestIndexesForJobsThatNeverCompleteAreBounded(t *testing.T) {
	c := newCache(t)
	c.maxIndexed = 8
	base := time.Now()
	for i := range c.indexCap() + 5 {
		j := dispatched(t, c, base.Add(time.Duration(i)*time.Millisecond), "here.example", "gone.example")
		file(t, c, ret(j, "here.example"))
	}
	if n := indexed(c); n > c.indexCap() {
		t.Errorf("%d indexes held, more than the bound of %d", n, c.indexCap())
	}
}

// A pruned job's index goes with its records.
func TestPruneForgetsTheIndex(t *testing.T) {
	c := newCache(t)
	old := time.Now().Add(-48 * time.Hour)
	j := dispatched(t, c, old, "a.example", "b.example")
	file(t, c, ret(j, "a.example"))
	c.Retention = 24 * time.Hour
	if _, err := c.Prune(); err != nil {
		t.Fatal(err)
	}
	if n := indexed(c); n != 0 {
		t.Errorf("%d indexes held after the only job was pruned, want 0", n)
	}
	if _, err := c.File(ret(j, "b.example")); !errors.Is(err, ErrNoJob) {
		t.Errorf("a return against a pruned job: %v, want ErrNoJob", err)
	}
}

func indexed(c *Cache) int {
	c.idxMu.Lock()
	defer c.idxMu.Unlock()
	return len(c.idx)
}
