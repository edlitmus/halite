package bridge

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// The process limit, and the account it actually bounds.
//
// `RLIMIT_NPROC` is per real UID. The kernel counts every process — on
// Linux every *thread* — that account already has anywhere on the
// machine, so handing an extension a limit of 32 while it shares the
// agent's identity does not give it 32 threads. It gives the whole
// account 32, and the extension is the one that dies when the rest of
// the machine has used them.
//
// That is what `internal/extpillar`'s end-to-end test had been flaking
// on for weeks, reported only as "the extension exited without
// answering" until the stderr tail kept both ends of a Go fatal:
//
//	runtime: failed to create new OS thread (have 5 already; errno=11)
//	runtime: may need to increase max user processes (ulimit -u)
//	fatal error: newosproc
//
// DIVERGENCE 5.125.

// **The environment carries the limit only where it means what it
// says.** This is the decision itself, with no machine state in it, so
// it runs everywhere the suite does.
func TestTheProcessLimitIsCarriedOnlyForAnExtensionWithItsOwnAccount(t *testing.T) {
	shared := DefaultSandbox()
	if shared.Processes == 0 {
		t.Fatal("the default sandbox no longer sets a process limit; this test is about how it is applied")
	}
	if env := strings.Join(shared.limitEnvironment(), " "); strings.Contains(env, "RLIMIT_NPROC") {
		t.Errorf("an extension running as the host's identity was handed a per-account process limit: %s", env)
	}

	own := DefaultSandbox()
	own.User = "halite-ext"
	env := strings.Join(own.limitEnvironment(), " ")
	if !strings.Contains(env, "HALITE_EXT_RLIMIT_NPROC=32") {
		t.Errorf("an extension with an account of its own was not handed the process limit: %s", env)
	}

	// And the description says which of the two happened, because an
	// operator who reads "processes 32" and gets no limit has been told
	// something untrue.
	shared.User = ""
	sharedText := strings.Join(shared.Describe(), "; ")
	if strings.Contains(sharedText, "processes 32") && !strings.Contains(sharedText, "not applied") {
		t.Errorf("Describe reports a process limit that is not applied: %s", sharedText)
	}
}

// **The flake itself, made to happen on purpose.**
//
// The test process takes more OS threads than the sandbox's limit
// allows and holds them, so the account is over its budget before the
// extension starts. Under the old behaviour the extension is handed
// that budget and cannot create the threads a Go runtime needs; it dies
// during start-up with `newosproc`, which is exactly the CI failure.
//
// Linux only, and deliberately: RLIMIT_NPROC counts threads there and
// processes on the BSDs, so locking threads in this process is a real
// reproduction on one and no reproduction at all on the other. A test
// that quietly passed on FreeBSD by not reproducing anything would be
// worse than one that says it is not running.
func TestAnExtensionStartsWhileTheAccountIsOverTheProcessLimit(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("RLIMIT_NPROC counts threads on Linux and processes elsewhere; this is %s", runtime.GOOS)
	}

	limit := DefaultSandbox().Processes
	if limit == 0 {
		t.Skip("the default sandbox sets no process limit, so there is nothing to be over")
	}

	// Hold more threads than the limit, for the lifetime of the test.
	// runtime.LockOSThread is what makes them real threads of this
	// process rather than goroutines the scheduler may multiplex.
	release := make(chan struct{})
	var holding sync.WaitGroup
	for i := uint64(0); i < limit+8; i++ {
		holding.Add(1)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			holding.Done()
			<-release
		}()
	}
	holding.Wait()
	t.Cleanup(func() { close(release) })

	proc := startEcho(t, func(o *Options) { o.Sandbox = DefaultSandbox() })
	info := proc.Info()
	if info.Name != "echo" {
		t.Fatalf("the extension did not identify itself: %+v", info)
	}
	if _, err := proc.Call(context.Background(), "say", nil,
		map[string]any{"message": "hello"}, nil); err != nil {
		t.Fatalf("the extension could not answer while this account held %d threads: %v", limit+8, err)
	}
}
