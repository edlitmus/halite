package hub

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/keystore"
	"github.com/edlitmus/halite/internal/target"
)

// The tests below wait out nodeMemoMargin, the production value, before
// relying on what was kept. A shorter margin would be unsound on a
// filesystem that keeps whole seconds, and these run on every CI leg.

// memoHub is a Server with n accepted nodes, each with cached grains
// role: web, except the ones named in db, which have role: db.
func memoHub(t *testing.T, n int, db ...string) (*Server, *NodeCache) {
	t.Helper()
	dir := t.TempDir()
	store, err := keystore.Open(dir + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	cache, err := OpenNodeCache(dir + "/nodes")
	if err != nil {
		t.Fatal(err)
	}
	isDB := map[string]bool{}
	for _, id := range db {
		isDB[id] = true
	}
	for i := range n {
		id := fmt.Sprintf("n%02d.example", i)
		if err := store.Put(&keystore.Record{NodeID: id, State: keystore.Accepted,
			Fingerprint: "aa", NotAfter: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		role := "web"
		if isDB[id] {
			role = "db"
		}
		putGrains(t, cache, id, role)
	}
	return &Server{Authority: &keystore.Authority{Store: store}, Nodes: cache}, cache
}

func putGrains(t *testing.T, cache *NodeCache, id, role string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"id": id, "role": role})
	if err := cache.Put(&NodeData{NodeID: id, Grains: raw, LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func resolveOn(t *testing.T, s *Server, kind target.Kind, expr string) []string {
	t.Helper()
	m, err := target.Compile(kind, expr, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.resolve(m, "")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	return got
}

func settle() { time.Sleep(nodeMemoMargin + 200*time.Millisecond) }

// While nothing changes, a second dispatch reads no node files: before
// DIVERGENCE 5.670 every dispatch read and decoded every node's file,
// even for a target that names one node by its ID.
func TestADispatchDoesNotReadTheNodeCacheAgainWhileNothingChanges(t *testing.T) {
	s, cache := memoHub(t, 20)
	settle()
	if got := resolveOn(t, s, target.Glob, "n03.example"); len(got) != 1 {
		t.Fatalf("matched %v; want n03.example", got)
	}
	first := cache.reads.Load()
	resolveOn(t, s, target.Glob, "n03.example")
	if got := resolveOn(t, s, target.Grain, "role:web"); len(got) != 20 {
		t.Fatalf("role:web matched %d; want 20", len(got))
	}
	if first != 20 || cache.reads.Load() != 20 {
		t.Fatalf("read %d node files for the first dispatch and %d in all; want 20 and 20", first, cache.reads.Load())
	}
}

// A node whose grains change is matched on the new grains by the very
// next dispatch, and only its file is read again.
func TestADispatchSeesAChangedNodeAndReadsOnlyIt(t *testing.T) {
	s, cache := memoHub(t, 20)
	settle()
	resolveOn(t, s, target.Grain, "role:db")
	before := cache.reads.Load()
	putGrains(t, cache, "n07.example", "db")
	if got := resolveOn(t, s, target.Grain, "role:db"); len(got) != 1 || got[0] != "n07.example" {
		t.Fatalf("after n07 became role:db, role:db matched %v", got)
	}
	if read := cache.reads.Load() - before; read != 1 {
		t.Fatalf("the dispatch after one node changed read %d node files; want 1", read)
	}
	// A node that is forgotten matches on its ID alone, as before.
	if err := cache.Delete("n07.example"); err != nil {
		t.Fatal(err)
	}
	if got := resolveOn(t, s, target.Grain, "role:db"); len(got) != 0 {
		t.Fatalf("after n07's data was deleted, role:db matched %v", got)
	}
}

// What a dispatch reads straight after a write is not trusted: the
// write's mtime is too close to the clock for a later write in the same
// tick to be told apart, which is what nodeMemoMargin is for.
func TestADispatchStraightAfterAWriteTrustsNothing(t *testing.T) {
	s, cache := memoHub(t, 3)
	resolveOn(t, s, target.Glob, "*")
	resolveOn(t, s, target.Glob, "*")
	if got := cache.reads.Load(); got != 6 {
		t.Fatalf("two dispatches straight after a write read %d node files; want 6, each reading all three", got)
	}
}

// A node whose data will not decode is skipped, and named, on every
// dispatch: keeping what was read must not turn it into a node with no
// grains.
func TestAnUnreadableNodeIsSkippedEveryTime(t *testing.T) {
	s, cache := memoHub(t, 3)
	if err := os.WriteFile(cache.dir+"/n01.example.json", []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	settle()
	for i := range 2 {
		if got := resolveOn(t, s, target.Glob, "*"); len(got) != 2 {
			t.Fatalf("dispatch %d matched %v; want the two readable nodes, n01 skipped", i+1, got)
		}
	}
}

// Dispatches running while nodes connect: no race, and once the writes
// stop, every node's last grains are what a dispatch matches.
func TestDispatchesWhileNodesWriteAgreeAfterwards(t *testing.T) {
	s, cache := memoHub(t, 10)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				m, _ := target.Compile(target.Grain, "role:db", nil)
				if _, err := s.resolve(m, ""); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := range 10 {
		putGrains(t, cache, fmt.Sprintf("n%02d.example", i), "db")
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
	settle()
	if got := resolveOn(t, s, target.Grain, "role:db"); len(got) != 10 {
		t.Fatalf("after every node became role:db, role:db matched %d", len(got))
	}
}
