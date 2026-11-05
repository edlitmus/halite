package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
)

// `jobs active` read the 200 most recent jobs and answered "nothing is in
// flight" when none of those was. A batch with a long TTL is still going
// after any number of shorter jobs have been sent past it, and that is
// the answer an operator checks before a restart. DIVERGENCE 5.592.
func TestJobsActiveFindsAnOldJobStillInFlight(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	cache, err := job.OpenCache(filepath.Join(state, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	// The batch: started an hour ago with a day to run, two of four
	// nodes reached.
	start := now.Add(-time.Hour)
	batch := &job.Job{
		JID:       job.NewID(start),
		Fun:       "state.apply",
		Target:    "*",
		Nodes:     []string{"a", "b", "c", "d"},
		Created:   start,
		Expires:   now.Add(23 * time.Hour),
		State:     job.Batching,
		Batch:     job.Batch{Size: 2},
		Delivered: []string{"a", "b"},
	}
	if err := cache.Put(batch); err != nil {
		t.Fatal(err)
	}
	// And 201 finished jobs after it, one more than the 200 it read.
	for i := range 201 {
		at := start.Add(time.Duration(i+1) * 10 * time.Second)
		if err := cache.Put(&job.Job{
			JID:     job.NewID(at),
			Fun:     "test.ping",
			Target:  "*",
			Nodes:   []string{"a"},
			Created: at,
			Expires: at.Add(15 * time.Minute),
			State:   job.Complete,
		}); err != nil {
			t.Fatal(err)
		}
	}

	config := filepath.Join(root, "hub.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("state_dir: %s\n", state)), 0o644); err != nil {
		t.Fatal(err)
	}
	res := run(t, "jobs", "active", "--config", config)
	if res.code != 0 {
		t.Fatalf("jobs active exited %d: %s", res.code, res.stderr)
	}
	if !strings.Contains(res.stdout, string(batch.JID)) {
		t.Errorf("the batch still in flight is not listed:\n%s", res.stdout)
	}
	if strings.Contains(res.stdout, "nothing is in flight") {
		t.Errorf("said nothing is in flight with a batch half done:\n%s", res.stdout)
	}
}
