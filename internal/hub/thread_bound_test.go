package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// threadStormEnv runs TestHubThreadsAreBoundedUnderAStorm's body in the
// child it starts, rather than in the test binary that is running the
// whole package.
const threadStormEnv = "HALITE_HUB_THREAD_STORM_CHILD"

// stormThreadLimit is the child's debug.SetMaxThreads. Go kills a process
// that goes past it with `fatal error: thread exhaustion`, which no
// recover catches; that is why the storm runs in a child.
//
// Two hundred is the production limit, 10,000, scaled to what a unit test
// can drive: well above what the hub needs to run -- a thread per P, sysmon,
// the collector, atomicfile.MaxConcurrentWrites writers in the kernel -- and
// well below the 2 × stormNodes requests the storm makes at once.
const stormThreadLimit = 200

// stormNodes is how many nodes connect, and how many returns are filed, all
// at the same moment.
const stormNodes = 300

// The hub's thread count does not grow with the number of requests that
// arrive together.
//
// It did. Every request that wrote to disk held an operating-system thread
// for as long as its write took -- Go parks a goroutine waiting on the
// network without one, but a goroutine in fsync, rename or unlink is in the
// kernel and keeps its thread -- and nothing bounded how many did so at
// once. On 2026-11-11 a 16-core hub at `main` 23e12c1a died with
// `fatal error: thread exhaustion` twice at 20,000 simulated nodes: 1.2 s
// after a `test.ping` reached them all, and again 18 s into a clean restart
// with every node reconnecting at once. The second crash's stack was
// NodeCache.Put's atomic write, from subscribe. DIVERGENCE 5.279, and the
// fix is 5.653.
//
// This is that, at a size a unit test can drive: the child caps itself at
// stormThreadLimit threads and then connects stormNodes nodes, each of which
// records its grains, while stormNodes returns are filed against one job. A
// hub whose disk writes are unbounded needs a thread for each one in the
// kernel and dies; a bounded one finishes.
//
// It depends on the writes being slow enough to overlap, which on a laptop
// they are: macOS's fsync is F_FULLFSYNC, a trip to the drive's platter or
// flash. On a filesystem whose fsync returns at once -- tmpfs -- the
// unbounded hub may also finish, and this passes without having shown
// anything; atomicfile's TestWritesHoldingThreadsAreBounded is the half that
// does not depend on the disk, and TestAnchorReportsHoldingThreadsAreBounded
// the test of the evidence anchors' bound. Adding real anchor reports to
// this storm made it pass with every bound removed: they did not pile up on
// that Mac, each being its own file, and spread out the writes that do.
//
// Measured on an M4 Pro, macOS, 2026-11-11, GOMAXPROCS=2: at `main` 23e12c1a
// the child died of thread exhaustion in under a second, with 108 threads in
// subscribe's NodeCache.Put and 87 in returned's job.Cache.File, all inside
// atomicfile.WritePrepared. At this size, with the bound removed, it failed
// in four runs of five (seven of eight at 500 nodes); with it, five of five
// passed in about 5 s. So it is a likely reproduction, not a certain one,
// which is the other reason for the two read(2) tests.
func TestHubThreadsAreBoundedUnderAStorm(t *testing.T) {
	if os.Getenv(threadStormEnv) != "" {
		threadStorm(t)
		return
	}
	if testing.Short() {
		t.Skip("starts a child that writes six hundred fsynced files; not in -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestHubThreadsAreBoundedUnderAStorm$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), threadStormEnv+"=1")
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "thread exhaustion") {
		t.Fatalf("the hub ran out of threads with %d nodes connecting and as many returns "+
			"arriving at once, at a limit of %d:\n%s", stormNodes, stormThreadLimit, head(out, 12))
	}
	if err != nil {
		t.Fatalf("the storm's child failed: %v\n%s", err, tail(out, 40))
	}
	if !strings.Contains(string(out), "storm: ") {
		// A child that printed nothing of its own did not run the storm,
		// whatever its exit status says.
		t.Fatalf("the storm's child did not report running:\n%s", tail(out, 40))
	}
	t.Logf("%s", tail(out, 6))
}

// threadStorm is the child.
func threadStorm(t *testing.T) {
	debug.SetMaxThreads(stormThreadLimit)

	nodeCache, err := OpenNodeCache(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, jid, nodes := scaleJob(t, stormNodes)
	s.Nodes = nodeCache

	// A cancelled context: the stream returns as soon as it is attached.
	// What is being measured is everything before that -- the grains
	// written to the node cache -- and the queued-delivery goroutine it
	// starts.
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	subscribe := func(node string) error {
		body, _ := json.Marshal(transport.SubscribeRequest{
			NodeID: node, Version: "storm", Grains: json.RawMessage(`{"os":"FreeBSD"}`),
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", transport.PathSubscribe, bytes.NewReader(body)).WithContext(gone)
		s.subscribe(rec, req, node)
		if rec.Code != http.StatusOK {
			return fmt.Errorf("subscribe %s: %d %s", node, rec.Code, rec.Body.String())
		}
		return nil
	}
	file := func(node string) error {
		body, _ := json.Marshal(job.Return{
			JID: jid, NodeID: node, Fun: "test.ping", Success: true,
			Return: json.RawMessage(`true`), Schema: job.ReturnSchema,
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", transport.PathReturn, bytes.NewReader(body))
		s.returned(rec, req, node)
		if rec.Code != http.StatusNoContent {
			return fmt.Errorf("return %s: %d %s", node, rec.Code, rec.Body.String())
		}
		return nil
	}

	start := make(chan struct{})
	errs := make(chan error, 2*stormNodes)
	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Go(func() { <-start; errs <- subscribe(node) })
		wg.Go(func() { <-start; errs <- file(node) })
	}
	began := time.Now()
	close(start)
	wg.Wait()
	s.background.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	known, err := nodeCache.Known()
	if err != nil {
		t.Fatal(err)
	}
	if len(known) != stormNodes {
		t.Fatalf("the node cache holds %d nodes after %d connected", len(known), stormNodes)
	}
	j, err := s.Jobs.Get(jid)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != job.Complete {
		t.Fatalf("the job is %s after every node answered, want complete", j.State)
	}
	fmt.Printf("storm: %d connects and %d returns in %s under a %d-thread limit\n",
		stormNodes, stormNodes, time.Since(began).Round(time.Millisecond), stormThreadLimit)
}

// head is the first n lines of out: a crash's stack is thousands of
// lines, and Go names the limit it hit at the top.
func head(out []byte, n int) string {
	lines := strings.SplitN(string(out), "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// tail is the last n lines of out.
func tail(out []byte, n int) string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
