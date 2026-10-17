package builtin

import (
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// **An unparseable timeout is refused, not dropped** (issue 321, section
// 5). cmd.shell, cmd.exec_code and the script functions built their
// command with `if d, err := durationOf(v); err == nil`, so `timeout:
// banana` -- or any template slip -- ran the command with no deadline at
// all. It is the 5.139 defect, which cmd.run was fixed for and these were
// not. Nothing may run.
func TestAnUnparseableTimeoutIsRefused(t *testing.T) {
	r := New()
	for _, c := range []struct {
		fn   string
		args *value.Map
	}{
		{"cmd.shell", value.MapOf("cmd", "true", "timeout", "banana")},
		{"cmd.exec_code", value.MapOf("lang", "/bin/sh", "code", "true", "timeout", "banana")},
		{"network.connect", value.MapOf("host", "127.0.0.1", "port", int64(9), "timeout", "banana")},
	} {
		ctx := newCtx(false)
		_, err := r.Exec.Call(ctx, c.fn, c.args)
		if err == nil || !strings.Contains(err.Error(), "timeout") {
			t.Errorf("%s with timeout banana: %v", c.fn, err)
		}
		if rec, ok := ctx.Runner.(*exec.RecordingRunner); ok && len(rec.RanCommands()) > 0 {
			t.Errorf("%s ran %q with a timeout it could not read", c.fn, rec.RanCommands())
		}
	}
}
