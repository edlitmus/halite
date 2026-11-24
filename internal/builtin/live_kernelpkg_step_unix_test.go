//go:build unix

package builtin

import (
	"context"
	"strings"
	"testing"
	"time"

	hexec "github.com/edlitmus/halite/internal/exec"
)

// kpStep is what bounds the module's own apt in the Debian kernel tests
// (5.729.1): the module runs its commands through c.Ctx, and kpStep
// cancels that context at the step's limit. Here the command is a shell
// whose forked sleep holds the output pipe, run through the OSRunner as
// the module's are, so a cancel that reached only the shell would leave
// Run waiting on the sleep for a minute.
func TestKernelpkgStepStopsAModuleCommandAtItsLimit(t *testing.T) {
	start := time.Now()
	err := kpStep(t, "a module command", time.Second, func(ctx context.Context) error {
		_, err := (&hexec.OSRunner{}).Run(ctx, hexec.Command{Argv: []string{"sh", "-c", "sleep 60 & wait"}})
		return err
	})
	took := time.Since(start)

	if err == nil {
		t.Fatal("a command past its step's limit returned no error")
	}
	if took > 15*time.Second {
		t.Errorf("the step returned %s after a 1s limit; the command was waited out, not stopped", took.Round(time.Second))
	}
	if !strings.Contains(err.Error(), "a module command did not finish in 1s") {
		t.Errorf("the error does not name the step and its limit: %v", err)
	}
}
