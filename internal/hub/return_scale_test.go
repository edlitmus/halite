package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/job"
	"github.com/edlitmus/halite/internal/transport"
)

// scaleJob is a job dispatched to n nodes, with a hub to file its
// returns through.
func scaleJob(tb testing.TB, n int) (*Server, job.ID, []string) {
	tb.Helper()
	jobs, err := job.OpenCache(tb.TempDir())
	if err != nil {
		tb.Fatal(err)
	}
	nodes := make([]string, n)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("sim-%05d.fleet.example", i)
	}
	const jid = job.ID("20261108T120000000001")
	if err := jobs.Put(&job.Job{
		JID: jid, Fun: "test.ping", Target: "*", Nodes: nodes, Delivered: nodes,
		State: job.Dispatched, Expires: time.Now().Add(time.Hour),
	}); err != nil {
		tb.Fatal(err)
	}
	return &Server{Jobs: jobs}, jid, nodes
}

// fileReturns posts one return per node through the hub's own handler,
// from `workers` goroutines at once, the way a fleet answering a ping
// arrives.
func fileReturns(tb testing.TB, s *Server, jid job.ID, nodes []string, workers int) {
	tb.Helper()
	work := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed []string
	for range workers {
		wg.Go(func() {
			for node := range work {
				body, _ := json.Marshal(job.Return{
					JID: jid, NodeID: node, Fun: "test.ping", Success: true,
					Return: json.RawMessage(`true`), Schema: job.ReturnSchema,
				})
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", transport.PathReturn, bytes.NewReader(body))
				s.returned(rec, req, node)
				if rec.Code != http.StatusNoContent {
					mu.Lock()
					failed = append(failed, fmt.Sprintf("%s: %d %s", node, rec.Code, rec.Body.String()))
					mu.Unlock()
				}
			}
		})
	}
	for _, n := range nodes {
		work <- n
	}
	close(work)
	wg.Wait()
	if len(failed) > 0 {
		tb.Fatalf("%d returns were refused, the first: %s", len(failed), failed[0])
	}
}

// BenchmarkReturnsForOneJob files every return of one job of N nodes.
// A hub whose per-return cost is constant scales this linearly in N.
func BenchmarkReturnsForOneJob(b *testing.B) {
	for _, n := range []int{250, 500, 1000, 2000} {
		b.Run(fmt.Sprintf("nodes=%d", n), func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				s, jid, nodes := scaleJob(b, n)
				b.StartTimer()
				fileReturns(b, s, jid, nodes, 8)
			}
		})
	}
}

// A return costs the same whatever the size of the job it answers.
//
// It did not: every return asked completeIfDone, which asked Missing,
// which read and decoded every return already filed, and the handler
// read the job's record -- its whole node list -- three more times. The
// k-th return of a job did k reads, a job to n nodes did n²/2, and one
// `test.ping` to 5,000 simulated nodes took a 16 GiB hub from 2.2 GB to
// 14.2 GB in a minute. DIVERGENCE 5.634.
//
// Measured as bytes allocated per return, because that is deterministic
// where time is not, and because memory is what ran out. Two jobs, one
// eight times the size of the other: constant work per return gives a
// ratio near 1. Measured on an M4 Pro, 2026-11-08: the quadratic
// handler allocated 96 KB and 584 KB per return at these sizes, a ratio
// of 6.1; the fixed one 15.7 KB and 14.0 KB, 0.9. The bound of 2.5
// leaves room for the once-per-job work -- building the index, the
// completion check -- and for whatever else the process allocates
// meanwhile.
func TestAReturnCostsTheSameWhateverTheSizeOfTheJob(t *testing.T) {
	perReturn := func(n int) float64 {
		s, jid, nodes := scaleJob(t, n)
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		fileReturns(t, s, jid, nodes, 8)
		runtime.ReadMemStats(&after)
		j, err := s.Jobs.Get(jid)
		if err != nil {
			t.Fatal(err)
		}
		if j.State != job.Complete {
			t.Fatalf("the job to %d nodes is %s after every node answered, want complete", n, j.State)
		}
		return float64(after.TotalAlloc-before.TotalAlloc) / float64(n)
	}
	small, large := perReturn(40), perReturn(320)
	t.Logf("bytes allocated per return: %.0f for 40 nodes, %.0f for 320", small, large)
	if ratio := large / small; ratio > 2.5 {
		t.Errorf("a return to a job eight times larger allocates %.1f times as much: "+
			"the per-return work grows with the size of the job", ratio)
	}
}
