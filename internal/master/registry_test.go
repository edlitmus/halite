package master

import (
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/transport"
)

func TestJobIDsAreOrdered(t *testing.T) {
	prev := newJobID()
	for i := 0; i < 10000; i++ {
		id := newJobID()
		if id <= prev {
			t.Fatalf("ids not increasing: %q then %q", prev, id)
		}
		prev = id
	}
}

func TestJobsArePruned(t *testing.T) {
	r := newRegistry(time.Minute, time.Minute)
	for i := 0; i < maxJobs+100; i++ {
		r.dispatch(transport.Job{ID: newJobID(), Target: "*", Created: time.Now()}, false)
	}
	r.mu.Lock()
	n := len(r.jobs)
	r.mu.Unlock()
	if n > maxJobs {
		t.Fatalf("jobs map grew to %d, want <= %d", n, maxJobs)
	}
}

func TestOrchestrationsArePrunedButNotRunning(t *testing.T) {
	r := newRegistry(time.Minute, time.Minute)
	running := newJobID()
	r.startOrchestration(running, "keep", "test")
	for i := 0; i < maxOrchestrations+50; i++ {
		id := newJobID()
		r.startOrchestration(id, "done", "test")
		r.finishOrchestration(id, nil)
	}
	r.mu.Lock()
	n := len(r.orchestrations)
	r.mu.Unlock()
	if n > maxOrchestrations {
		t.Fatalf("orchestrations map grew to %d, want <= %d", n, maxOrchestrations)
	}
	if _, ok := r.orchestration(running); !ok {
		t.Fatal("a running orchestration was evicted")
	}
}

func TestMineOmitsVanishedAgents(t *testing.T) {
	r := newRegistry(time.Minute, time.Minute)
	r.touch("alive", map[string]any{"os": "linux"}, "")
	r.storeMine("alive", "network.interfaces", map[string]any{"ip": "10.0.0.1"})
	r.storeMine("ghost", "network.interfaces", map[string]any{"ip": "10.0.0.2"})

	// An agent past the staleness window must also drop out.
	r.touch("stale", map[string]any{"os": "linux"}, "")
	r.storeMine("stale", "network.interfaces", map[string]any{"ip": "10.0.0.3"})
	r.mu.Lock()
	r.agents["stale"].info.LastSeen = time.Now().Add(-mineTTL - time.Minute)
	r.mu.Unlock()

	got := r.readMine("network.interfaces", "")
	byAgent := got["network.interfaces"]
	if len(byAgent) != 1 {
		t.Fatalf("want only the live agent's entry, got %v", byAgent)
	}
	if _, ok := byAgent["alive"]; !ok {
		t.Fatalf("live agent's entry missing: %v", byAgent)
	}
}

func TestMineTargetFilter(t *testing.T) {
	r := newRegistry(time.Minute, time.Minute)
	r.touch("web1", map[string]any{"role": "web"}, "")
	r.touch("db1", map[string]any{"role": "db"}, "")
	r.storeMine("web1", "f", map[string]any{"v": "1"})
	r.storeMine("db1", "f", map[string]any{"v": "2"})

	got := r.readMine("f", "role:web")
	if len(got["f"]) != 1 {
		t.Fatalf("want one entry for role:web, got %v", got["f"])
	}
	if _, ok := got["f"]["web1"]; !ok {
		t.Fatalf("web1 missing: %v", got["f"])
	}
}

func TestJobIDsUniqueUnderConcurrency(t *testing.T) {
	const n = 200
	ids := make(chan string, n)
	for i := 0; i < n; i++ {
		go func() { ids <- newJobID() }()
	}
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		id := <-ids
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	if len(seen) != n {
		t.Fatalf("want %d unique ids, got %d", n, len(seen))
	}
}
