//go:build unix

package atomicfile

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const threadsChildEnv = "HALITE_ATOMICFILE_THREADS_CHILD"

// Writes that are stuck in the kernel cannot take a process past its
// thread limit.
//
// What killed the hub on 2026-11-11 (DIVERGENCE 5.279, 5.653) was a
// goroutine per write sitting in fsync, each holding an operating-system
// thread, with nothing bounding how many. Reproducing that with real
// fsyncs depends on the disk being slow, which a laptop's is and a tmpfs
// is not, so this makes the kernel slow instead: inside every write, the
// hook blocks in read(2) on a pipe nobody writes to yet. A goroutine in a
// raw system call holds its thread exactly as one in fsync does.
//
// The child caps itself at 150 threads and starts 400 writes. Unbounded,
// that is 400 threads in read(2) and `fatal error: thread exhaustion`;
// bounded, it is MaxConcurrentWrites of them and the rest parked. Then the
// pipe is fed, one byte per write, and every write must finish.
func TestWritesHoldingThreadsAreBounded(t *testing.T) {
	if os.Getenv(threadsChildEnv) != "" {
		writesHoldingThreads(t)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^TestWritesHoldingThreadsAreBounded$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), threadsChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if strings.Contains(string(out), "thread exhaustion") {
		lines := strings.SplitN(string(out), "\n", 6)
		t.Fatalf("writes held in the kernel took the process past its thread limit:\n%s",
			strings.Join(lines[:min(len(lines), 5)], "\n"))
	}
	if err != nil {
		t.Fatalf("the child failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "threads: ") {
		t.Fatalf("the child did not report running:\n%s", out)
	}
}

func writesHoldingThreads(t *testing.T) {
	const limit, writers = 150, 400
	debug.SetMaxThreads(limit)

	var p [2]int
	if err := syscall.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	// Blocking descriptors, not an os.File: the runtime's poller would
	// park a read on one of those without a thread, which is the very
	// thing this must not do.
	r, w := p[0], p[1]
	defer syscall.Close(r)
	defer syscall.Close(w)
	beforeAttributes = func(string) {
		var b [1]byte
		_, _ = syscall.Read(r, b[:])
	}
	defer func() { beforeAttributes = nil }()

	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Go(func() {
			errs <- Write(filepath.Join(dir, fmt.Sprintf("f%03d", i)), []byte("x"), 0o600)
		})
	}
	// Long enough for every writer that can get into the kernel to have
	// done so; an unbounded process is dead well before this.
	time.Sleep(500 * time.Millisecond)
	if _, err := syscall.Write(w, make([]byte, writers)); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	fmt.Printf("threads: %d writes held in read(2) finished under a %d-thread limit\n", writers, limit)
}
