package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Issue 876. The child is created suspended, and os/exec starts watching
// the context inside Start, before afterStart has resumed it. A timeout
// that runs out in that gap makes Cancel kill the still-suspended child,
// its one thread goes with it, and winjob.Resume then finds the thread
// listed but gone: "it has no threads to resume (1 listed, none still
// there)". That was reported as a resume failure, not as the timeout that
// caused it, so a grain provider that timed out under load was logged as
// one that "exited non-zero and was skipped".
//
// On an idle machine the gap is far shorter than any timeout, so these
// tests do not wait to lose the race. The hook holds the resume until the
// timeout's kill has ended the child, and then calls the real Resume.
func TestATimeoutThatFiresBeforeTheResumeIsReportedAsTheTimeout(t *testing.T) {
	realResume := resumeProcess
	t.Cleanup(func() { resumeProcess = realResume })
	resumeProcess = func(pid uint32) error {
		if alive(pid, 10*time.Second) {
			return fmt.Errorf("test setup: the timeout did not end child %d within 10s", pid)
		}
		return realResume(pid)
	}

	cmd := helperCommand("sleep", "30s")
	cmd.Timeout = 100 * time.Millisecond
	_, err := (&OSRunner{}).Run(context.Background(), cmd)
	if err == nil {
		t.Fatal("a command killed by its timeout before it ran reported success")
	}
	if !strings.Contains(err.Error(), "timed out after 100ms") {
		t.Errorf("the error should name the timeout that killed the child, not the resume it stopped: %v", err)
	}
}

// The other half: a resume that fails with no timeout behind it is still
// the command's error. Reporting every resume failure as whatever the
// context says would hide the case errNeverResumed exists for.
func TestAResumeThatFailsWithoutATimeoutIsStillReported(t *testing.T) {
	realResume := resumeProcess
	t.Cleanup(func() { resumeProcess = realResume })
	planted := errors.New("planted resume failure")
	resumeProcess = func(uint32) error { return planted }

	cmd := helperCommand("sleep", "30s")
	cmd.Timeout = 30 * time.Second
	start := time.Now()
	_, err := (&OSRunner{}).Run(context.Background(), cmd)
	if !errors.Is(err, planted) || !strings.Contains(err.Error(), "could not be resumed") {
		t.Errorf("a failed resume should be the command's error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("a child that could not be resumed was waited on for %v instead of being killed", elapsed)
	}
}
