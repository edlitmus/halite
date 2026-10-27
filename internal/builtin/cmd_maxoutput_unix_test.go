//go:build !windows

package builtin

import (
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// A command that writes without end was buffered whole in the node and
// waited out its timeout. With a ceiling it is stopped, and the failure
// says how to raise it.
func TestCmdRunPastMaxOutputIsStoppedAndSaysHowToRaiseIt(t *testing.T) {
	r := New()
	start := time.Now()
	_, err := r.Exec.Call(realCtx(t), "cmd.run", value.MapOf(
		"name", "head -c 4194304 /dev/zero; sleep 60", "max_output", int64(1<<20)))
	if err == nil || !strings.Contains(err.Error(), "max_output") {
		t.Fatalf("err = %v, want the limit failure naming max_output", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v: the command was waited out", time.Since(start))
	}
}

// max_output: 0 is the override that removes the ceiling, and an
// ordinary command is nowhere near the default.
func TestCmdRunMaxOutputZeroAndTheDefault(t *testing.T) {
	r := New()
	out, err := r.Exec.Call(realCtx(t), "cmd.run", value.MapOf(
		"name", "head -c 2097152 /dev/zero | tr '\\0' x", "max_output", int64(0)))
	if err != nil {
		t.Fatalf("max_output 0: %v", err)
	}
	if got, _ := out.(string); len(got) != 2097152 {
		t.Errorf("max_output 0 kept %d bytes, want all 2 MiB", len(got))
	}
	if _, err := r.Exec.Call(realCtx(t), "cmd.run", value.MapOf("name", "echo hi")); err != nil {
		t.Errorf("an ordinary command under the default: %v", err)
	}
	if _, err := r.Exec.Call(realCtx(t), "cmd.run", value.MapOf("name", "echo hi", "max_output", int64(-1))); err == nil {
		t.Error("a negative max_output was accepted")
	}
}
