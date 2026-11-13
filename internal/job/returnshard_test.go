package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// upgrade:cache-format
//
// The shard a node's return goes in is pinned. A hub reads back what an
// earlier run of it filed, so a change to returnShard would leave every
// return already on disk in a directory nothing looks in -- and a later
// retry from that node would be filed a second time. Change these values
// only together with a reader that looks in the old shards too.
func TestReturnShardIsPinned(t *testing.T) {
	for node, want := range map[string]string{
		"web1.example":       "a4",
		"sim-00001.simfleet": "19",
	} {
		if got := returnShard(node); got != want {
			t.Errorf("returnShard(%q) = %s, want %s", node, got, want)
		}
	}
}

// A return is filed in its shard, read back by Returns, and a job to one
// node makes one shard directory, not returnShards of them.
func TestAReturnIsFiledInItsShard(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example")
	r := &Return{JID: j.JID, NodeID: "web1.example", Success: true, Schema: ReturnSchema}
	if fresh, err := c.AddReturn(r); err != nil || !fresh {
		t.Fatalf("AddReturn = %v, %v", fresh, err)
	}
	dir, err := c.jobDir(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "returns", "a4", "web1.example.json")); err != nil {
		t.Errorf("the return is not in its shard: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "returns"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "a4" || names[1] != shardMarker {
		t.Errorf("returns/ holds %v, want the one shard a4 and the marker", names)
	}
	back, err := c.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.ReturnsSharded || back.Schema != ShardedJobSchema {
		t.Errorf("the record says sharded=%v schema=%q, want true and %q", back.ReturnsSharded, back.Schema, ShardedJobSchema)
	}
	got, err := c.Returns(j.JID)
	if err != nil || len(got) != 1 || got[0].NodeID != "web1.example" {
		t.Fatalf("Returns = %v, %v", got, err)
	}
}

// A job filed by a hub from before the shards has its returns directly
// in returns/. A hub after them must still read those, and count them as
// answered when it rebuilds its index, or an upgrade would show every
// earlier job's nodes as missing and file their retries a second time.
func TestReturnsInTheUnshardedLayoutStillCount(t *testing.T) {
	root := t.TempDir()
	c, err := OpenCache(root)
	if err != nil {
		t.Fatal(err)
	}
	j := dispatched(t, c, time.Now(), "web1.example", "web2.example")
	dir, err := c.jobDir(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	old := &Return{JID: j.JID, NodeID: "web1.example", Success: true, Schema: ReturnSchema}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "returns", "web1.example.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	// A fresh cache, as after a restart: its index is built from disk.
	c2, err := OpenCache(root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := c2.File(&Return{JID: j.JID, NodeID: "web1.example", Success: true, Schema: ReturnSchema})
	if err != nil {
		t.Fatal(err)
	}
	if f.Fresh {
		t.Error("a retry of a return filed in the old layout was filed again")
	}
	if f.Remaining != 1 {
		t.Errorf("Remaining = %d, want 1 (web2 has not answered)", f.Remaining)
	}
	if _, err := c2.AddReturn(&Return{JID: j.JID, NodeID: "web2.example", Success: true, Schema: ReturnSchema}); err != nil {
		t.Fatal(err)
	}
	got, err := c2.Returns(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].NodeID != "web1.example" || got[1].NodeID != "web2.example" {
		t.Errorf("Returns read %d returns across both layouts, want web1 and web2", len(got))
	}
	missing, err := c2.Missing(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("Missing = %v after both answered", missing)
	}
}

// A job none of whose returns went into a shard keeps the schema an
// older build can write: the stamp describes the record, not the build
// (TestOnlyASignedRecordCarriesTheSignedSchema has the same rule).
func TestAJobWithNoShardedReturnIsNotStamped(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example")
	back, err := c.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if back.ReturnsSharded || back.Schema != JobSchema {
		t.Errorf("a job with no returns says sharded=%v schema=%q, want false and %q", back.ReturnsSharded, back.Schema, JobSchema)
	}
	dir, err := c.jobDir(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "returns", shardMarker)); err == nil {
		t.Error("a job with no sharded return has the marker")
	}
}

// A job an older hub dispatched, with returns it filed flat, is marked
// the moment this build files the first return into a shard -- not
// before, and not never.
func TestAnUpgradedJobIsMarkedAtItsFirstShardedReturn(t *testing.T) {
	root := t.TempDir()
	c, err := OpenCache(root)
	if err != nil {
		t.Fatal(err)
	}
	j := dispatched(t, c, time.Now(), "web1.example", "web2.example")
	dir, err := c.jobDir(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(&Return{JID: j.JID, NodeID: "web1.example", Success: true, Schema: ReturnSchema})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "returns", "web1.example.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	c2, err := OpenCache(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c2.AddReturn(&Return{JID: j.JID, NodeID: "web2.example", Success: true, Schema: ReturnSchema}); err != nil {
		t.Fatal(err)
	}
	back, err := c2.Get(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	if !back.ReturnsSharded || back.Schema != ShardedJobSchema {
		t.Errorf("after a sharded return the record says sharded=%v schema=%q", back.ReturnsSharded, back.Schema)
	}
}

// What a hub from before the shards does with a sharded job, reproduced:
// this is the loop every released build's Returns runs (v0.1.1 and main
// at 574a0ae9), and it must fail, naming the marker, rather than answer
// "no returns". The released binaries themselves were run against such
// a job too (DIVERGENCE 5.660); this keeps the property from regressing
// without them.
func TestAnOlderReaderFailsOnAShardedJob(t *testing.T) {
	c := newCache(t)
	j := dispatched(t, c, time.Now(), "web1.example")
	if _, err := c.AddReturn(&Return{JID: j.JID, NodeID: "web1.example", Success: true, Schema: ReturnSchema}); err != nil {
		t.Fatal(err)
	}
	dir, err := c.jobDir(j.JID)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "returns"))
	if err != nil {
		t.Fatal(err)
	}
	var failed error
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "returns", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var r Return
		if err := json.Unmarshal(raw, &r); err != nil {
			failed = fmt.Errorf("the return at %s is not readable: %w", e.Name(), err)
			break
		}
	}
	if failed == nil {
		t.Fatal("an older reader would have read this job as having no returns")
	}
	if !strings.Contains(failed.Error(), shardMarker) {
		t.Errorf("the failure does not name the marker: %v", failed)
	}
}
