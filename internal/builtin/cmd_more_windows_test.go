package builtin

import (
	"github.com/edlitmus/halite/internal/value"
	"testing"
)

// cmd.exe cannot run a file handed to it positionally -- `cmd.exe
// path\to\script.cmd`, with no switch, starts an interactive session
// and ignores the path, exiting 0 without running anything. `shell:
// cmd` on `cmd.script` has to insert /c the same way the Shell form of
// cmd.run does, or the script never runs and nothing says so.
func TestCmdScriptWithShellCmdRunsTheScript(t *testing.T) {
	r := New()
	script := writeEchoScript(t, t.TempDir())

	c := realCtx(t)
	out, err := r.Exec.Call(c, "cmd.script",
		value.MapOf("source", script, "args", []any{"world"}, "shell", comspecForTest()))
	if err != nil {
		t.Fatal(err)
	}
	m := out.(*value.Map)
	if stdout, _ := m.Get("stdout"); stdout != "hello world" {
		t.Errorf("stdout = %#v, want %q; the script did not run", stdout, "hello world")
	}
	if code, _ := m.Get("retcode"); code != int64(0) {
		t.Errorf("retcode = %#v", code)
	}
}
