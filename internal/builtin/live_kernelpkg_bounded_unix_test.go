//go:build unix

package builtin

import (
	"strings"
	"testing"
	"time"
)

// The Debian kernel tests' runner, held to what issue 852 needed of it:
// a command that hangs is killed at its limit with everything it forked,
// and the failure says what it had printed, so the next hang on a runner
// names where it stopped instead of eating the leg's forty minutes.
//
// The child here is a shell whose background sleep holds the output pipe
// open, which is the shape of apt waiting on a dpkg hook: killing only
// the process that was started leaves Wait blocked on the pipe until the
// grandchild exits by itself, sixty seconds later.
func TestKernelpkgTestRunnerKillsAHungCommandAndKeepsItsOutput(t *testing.T) {
	start := time.Now()
	out, timedOut, err := runBounded(time.Second, []string{"sh", "-c", "echo fetched the image; sleep 60 & wait"})
	took := time.Since(start)

	if !timedOut || err == nil {
		t.Fatalf("a command past its limit returned timedOut=%v err=%v", timedOut, err)
	}
	if took > 15*time.Second {
		t.Errorf("the runner returned %s after a 1s limit; the sleep it forked was waited out, not killed", took.Round(time.Second))
	}
	for _, want := range []string{"sh -c echo fetched the image", "did not finish in 1s", "fetched the image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the timeout's error does not say %q:\n%v", want, err)
		}
	}
	if !strings.Contains(out, "fetched the image") {
		t.Errorf("the partial output was lost: %q", out)
	}
	t.Logf("returned after %s: %v", took.Round(10*time.Millisecond), err)
}

// stdin is /dev/null: a command that reads it, as a prompt would, gets
// end of file at once rather than waiting for a terminal.
func TestKernelpkgTestRunnerGivesTheCommandAnEmptyStdin(t *testing.T) {
	out, timedOut, err := runBounded(5*time.Second, []string{"sh", "-c", "read answer; echo got:$?:$answer"})
	if timedOut || err != nil {
		t.Fatalf("a read of stdin did not return: timedOut=%v err=%v", timedOut, err)
	}
	if strings.TrimSpace(out) != "got:1:" {
		t.Errorf("the read saw %q, not end of file", out)
	}
}

func TestKernelpkgTestRunnerSetsDebconfNonInteractive(t *testing.T) {
	out, _, err := runBounded(5*time.Second, []string{"sh", "-c", "echo $DEBIAN_FRONTEND"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "noninteractive" {
		t.Errorf("DEBIAN_FRONTEND is %q", out)
	}
}
