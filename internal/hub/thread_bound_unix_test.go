//go:build unix

package hub

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const anchorThreadsEnv = "HALITE_HUB_ANCHOR_THREADS_CHILD"

// Evidence anchors reported at once cannot take the hub past its thread
// limit.
//
// AnchorStore.Record fsyncs each accepted line, serialised per node and
// deliberately not across nodes, and a node reports when its stream opens.
// TestHubThreadsAreBoundedUnderAStorm drives real reports, and on the Mac
// this was written on they finish too quickly to pile up, so it passes
// with the bound on them removed: it does not show this bound. This does,
// whatever the disk: appendWrite, the one write appendAnchor makes, first
// blocks in read(2) on a pipe nobody writes to yet, which holds a thread
// as an fsync does. 400 reports under a 150-thread limit is 400 threads
// unbounded and `fatal error: thread exhaustion`; bounded, it is
// atomicfile.MaxConcurrentWrites of them. DIVERGENCE 5.653.
func TestAnchorReportsHoldingThreadsAreBounded(t *testing.T) {
	if os.Getenv(anchorThreadsEnv) != "" {
		anchorReportsHoldingThreads(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestAnchorReportsHoldingThreadsAreBounded$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), anchorThreadsEnv+"=1")
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "thread exhaustion") {
		t.Fatalf("evidence anchors held in the kernel took the hub past its thread limit:\n%s", head(out, 5))
	}
	if err != nil {
		t.Fatalf("the child failed: %v\n%s", err, tail(out, 40))
	}
	if !strings.Contains(string(out), "anchors: ") {
		t.Fatalf("the child did not report running:\n%s", tail(out, 40))
	}
}

func anchorReportsHoldingThreads(t *testing.T) {
	const limit, reports = 150, 400
	debug.SetMaxThreads(limit)

	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	// Blocking descriptors, not an os.File, whose reads the runtime's
	// poller would park without a thread.
	r, w := p[0], p[1]
	defer syscall.Close(r)
	defer syscall.Close(w)
	saved := appendWrite
	appendWrite = func(f *os.File, b []byte) (int, error) {
		var one [1]byte
		_, _ = syscall.Read(r, one[:])
		return f.Write(b)
	}
	defer func() { appendWrite = saved }()

	store, err := OpenAnchorStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, reports)
	for i := range reports {
		wg.Go(func() {
			_, err := store.Record(fmt.Sprintf("sim-%05d.fleet.example", i), 1, strings.Repeat("ab", 32),
				time.Now(), func(string) (string, error) { return "receipt", nil })
			errs <- err
		})
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := syscall.Write(w, make([]byte, reports)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	fmt.Printf("anchors: %d reports held in read(2) finished under a %d-thread limit\n", reports, limit)
}
