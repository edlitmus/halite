//go:build unix

package exec

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A command that stops to ask a question is answered with end-of-file,
// not left waiting.
//
// Issue 321's audit noted that a dnf5 read, run as a user, "prompted to
// import a third-party repo GPG key (declined, no tty)". Package managers
// do this -- dnf and dnf5 on a repository key, apt on a changed Release
// file, pkg on a fingerprint -- and a hub run that blocks on one blocks
// forever, with nobody at the other end to type. What keeps that from
// happening is that a Command with no Stdin gets none: os/exec connects
// the child's standard input to the null device, so `read` returns at
// once and the tool takes its "no" path. Nothing wrote that down or held
// it, and a change that handed the child the parent's standard input (a
// plausible tidy-up, for a runner that wants to pass a terminal through)
// would turn every prompt into a hang on a hub started from a shell.
//
// The child is a shell `read`, which asks exactly as a package manager
// does. The run is bounded from outside, in a goroutine, because
// Command.Timeout is not enough here: with a reader that never ends
// connected to the child, os/exec's Wait blocks on copying it even after
// the child is killed (measured by breaking the runner this way, where
// the test hung instead of failing). The test is run on sh's own prompt,
// not dnf's, so it holds the runner and says nothing about what dnf5
// prints or which way it answers.
func TestACommandThatPromptsIsAnsweredWithEndOfFile(t *testing.T) {
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := (&OSRunner{}).Run(context.Background(), Command{
			Argv:    []string{"sh", "-c", "if read answer; then echo answered; else echo eof; fi"},
			Timeout: 5 * time.Second,
		})
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		if o.err != nil {
			t.Fatalf("the prompt was not answered: %v", o.err)
		}
		if got := strings.TrimSpace(o.res.Stdout); got != "eof" {
			t.Errorf("a prompting child saw %q, want end-of-file", got)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a child that asked a question was left waiting for an answer")
	}
}
