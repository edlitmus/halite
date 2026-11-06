package exec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Issue 608 item 1, put to a runner.
//
// The job object is assigned after CreateProcess has returned, so a
// grandchild started in the gap between the two is outside the job, and
// neither the timeout's TerminateJobObject nor KILL_ON_JOB_CLOSE reaches
// it. TestTimeoutKillsAForkedChildToo already covers a grandchild started
// from the helper's main; this starts it from an init function, which is
// the earliest point a Go program runs any code of its own, and does it
// many times, because a race that is lost once in fifty is still lost.

// spawnAtOnceMode is read from the environment rather than from argv,
// because init runs before the testing package has parsed anything.
const spawnAtOnceEnv = "HALITE_TEST_SPAWN_AT_ONCE"

func init() {
	marker := os.Getenv(spawnAtOnceEnv)
	if marker == "" {
		return
	}
	// The grandchild must not inherit the variable, or it would do this
	// too and the tree would never end.
	os.Unsetenv(spawnAtOnceEnv)
	if err := startDetached(helperArgv("delayed-touch", marker)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestTimeoutKillsAGrandchildStartedBeforeTheChildsMain(t *testing.T) {
	const rounds = 40
	dir := t.TempDir()
	r := &OSRunner{}
	var markers []string
	for i := range rounds {
		marker := filepath.Join(dir, "survived-"+strconv.Itoa(i))
		markers = append(markers, marker)
		cmd := helperCommand("sleep", "30s")
		cmd.Env = append(CleanEnv(), spawnAtOnceEnv+"="+marker)
		cmd.Timeout = 300 * time.Millisecond
		if _, err := r.Run(context.Background(), cmd); err == nil {
			t.Fatalf("round %d: the timeout did not fire", i)
		}
	}
	// Past every grandchild's own two-second wait: one that escaped the
	// job has written its marker by now.
	time.Sleep(4 * time.Second)
	escaped := 0
	for _, m := range markers {
		if _, err := os.Stat(m); err == nil {
			escaped++
		}
	}
	if escaped > 0 {
		t.Errorf("%d of %d grandchildren started before the child's main outlived the timeout; "+
			"the job object was assigned too late to hold them", escaped, rounds)
	}
}
