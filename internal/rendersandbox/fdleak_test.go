package rendersandbox

import (
	"os"
	"runtime"
	"testing"
)

// A failed start leaks no file descriptors.
//
// `start` opens two pipes before it does anything that can fail, and
// `exec.Cmd.Start` is what closes them: its deferred cleanup closes the
// child ends always and the parent ends when the process did not start.
// So a failure *before* `Start` is reached leaves four descriptors open
// with nothing but the garbage collector's finalizer to reclaim them.
//
// `applyConfinement` is such a failure, and a reachable one:
// `renderAccount` resolves `render_sandbox_user` and refuses a name this
// machine cannot look up, which is what a typo in that setting produces.
// A node that retries a render then accumulates four descriptors per
// attempt.
//
// Measured before the fix on this host: 50 attempts took the process from
// 7 open descriptors to 207, exactly four per attempt. DIVERGENCE 5.144.
func TestAFailedStartLeaksNoDescriptors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("handles are not numbered lowest-free on Windows, so this measurement does not apply")
	}
	// A user name nothing can resolve, which is the whole of the failure:
	// `applyConfinement` returns before `cmd.Start` is called.
	cfg := Config{User: "halite-no-such-account-for-a-test"}
	if _, err := start(cfg); err == nil {
		t.Fatal("start succeeded with an unresolvable render account, so this test is " +
			"no longer measuring the path it was written for")
	}

	before := nextDescriptor(t)
	const attempts = 50
	for range attempts {
		if _, err := start(cfg); err == nil {
			t.Fatal("start succeeded on a later attempt")
		}
	}
	after := nextDescriptor(t)

	// A small allowance rather than equality: the runtime opens and closes
	// descriptors of its own, and a test that fails on one or two is a test
	// that flakes. Four per attempt is 200, so the allowance cannot hide it.
	if grew := after - before; grew > attempts/5 {
		t.Errorf("%d failed start(s) moved the lowest free descriptor from %d to %d, "+
			"which is %.1f per attempt: a failure before cmd.Start means nothing closes "+
			"the pipes it already made",
			attempts, before, after, float64(grew)/float64(attempts))
	} else {
		t.Logf("%d failed start(s) moved the lowest free descriptor by %d (%d -> %d)",
			attempts, after-before, before, after)
	}
}

// nextDescriptor opens a file and reports the number the kernel gave it.
//
// A unix kernel hands out the lowest free descriptor, so this number is a
// high-water mark for how many the process is holding: if a loop leaks
// four per turn, the number climbs by about four per turn.
//
// It is measured this way because counting entries in a descriptor
// directory does not work here. The first version of this test read
// `/dev/fd`, which on this host is a static three entries -- 0, 1 and 2 --
// and does not change when twenty files are opened. It reported no leak,
// and the no-leak result was the broken probe, not the code. `/proc/self/fd`
// does not exist here at all.
func nextDescriptor(t *testing.T) int {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s to read a descriptor number: %v", os.DevNull, err)
	}
	defer f.Close()
	return int(f.Fd())
}
