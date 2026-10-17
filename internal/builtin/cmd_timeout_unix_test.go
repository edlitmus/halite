//go:build unix

package builtin

import (
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// And a timeout that reads is still applied: a five-second sleep under a
// one-second timeout, through the real runner, ends in about a second.
func TestAReadableTimeoutStillStopsTheScript(t *testing.T) {
	start := time.Now()
	_, err := New().Exec.Call(realCtx(t), "cmd.exec_code",
		value.MapOf("lang", "/bin/sh", "code", "sleep 5\n", "timeout", "1"))
	took := time.Since(start)
	if err == nil {
		t.Fatalf("a five-second sleep under a one-second timeout succeeded after %v", took)
	}
	if took > 4*time.Second {
		t.Errorf("the timeout did not stop it: %v, %v", took, err)
	}
}
