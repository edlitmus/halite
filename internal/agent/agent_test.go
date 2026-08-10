package agent

import (
	"io"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/edlitmus/halite/internal/transport"
)

func quietLogger() *log.Logger { return log.New(io.Discard, "", 0) }

func TestNewRequiresAControlPlane(t *testing.T) {
	if _, err := New(Config{ID: "web1"}, map[string]any{}, quietLogger()); err == nil {
		t.Error("an agent with no master must not start")
	}
}

func TestNewDefaultsTheIdentityToTheHostGrain(t *testing.T) {
	a, err := New(Config{Masters: []string{"master1"}},
		map[string]any{"host": "web1.example.com"}, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	if a.cfg.ID != "web1.example.com" {
		t.Errorf("id = %q", a.cfg.ID)
	}
	// The enrolled identity becomes the id grain, which is what targeting
	// and state templates use.
	if a.grains["id"] != "web1.example.com" {
		t.Errorf("id grain = %v", a.grains["id"])
	}
}

func TestNewRefusesAnUnsafeIdentity(t *testing.T) {
	_, err := New(Config{Masters: []string{"master1"}, ID: "../escape"},
		map[string]any{}, quietLogger())
	if err == nil {
		t.Fatal("an unsafe id must be refused before anything connects")
	}
	if !strings.Contains(err.Error(), "agent id") {
		t.Errorf("err = %v", err)
	}
}

func TestClientAccessIsSafeAcrossFailover(t *testing.T) {
	// Beacons and the mine publish through the client from their own
	// goroutines while Run may be replacing it. -race turns an unguarded
	// swap here into a failure.
	a := &Agent{cfg: Config{ID: "web1"}, log: quietLogger(), grains: map[string]any{}}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = a.currentClient()
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		a.setClient(transport.NewJSONClient("master1", nil, 0))
	}
	close(stop)
	wg.Wait()
}

func TestBeaconAndMinePublishingBeforeConnectionAreNoOps(t *testing.T) {
	// Watchers start after the first connection, but a failover leaves a
	// window with no client; publishing then must not panic.
	a := &Agent{cfg: Config{ID: "web1"}, log: quietLogger(), grains: map[string]any{}}
	a.raiseBeacon("disk", map[string]any{"mount": "/"})
	a.publishMine("grains", map[string]any{"id": "web1"})
}
