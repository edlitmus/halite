package builtin

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// psProtectedCtx is a node whose process table is the one given and whose
// `kill` is recorded, not run. Every command gets the table as its
// output, which `kill` ignores; what is asserted is whether `kill` was
// asked for at all.
func psProtectedCtx(t *testing.T) (*exec.Context, *exec.RecordingRunner) {
	t.Helper()
	if runtime.GOOS == "windows" {
		// ps is unixOnly, so the registry's platform gate answers before
		// the function is reached and these would test the gate.
		t.Skip("ps does not run on Windows")
	}
	if psUsesLibxo() {
		// FreeBSD's ps is read as libxo JSON, so this table would not
		// parse there. The rule is not platform-specific; the fixture is.
		t.Skip("the scripted table is the column format; FreeBSD reads libxo JSON")
	}
	table := strings.Join([]string{
		"  PID  PPID USER %CPU %MEM  RSS    VSZ STAT COMMAND",
		"    1     0 root  0.0  0.1 1234  56789 Ss   /sbin/init",
		fmt.Sprintf("%5d     1 root  0.0  0.1 1234  56789 Ssl  /usr/local/bin/halite-node", os.Getpid()),
		"  900     1 www   0.0  0.1 1234  56789 S    nginx: worker process",
	}, "\n")
	r := &exec.RecordingRunner{Default: exec.Result{Stdout: table}}
	return &exec.Context{Runner: r, Lookup: func(n string) string { return "/bin/" + n }}, r
}

func psKills(r *exec.RecordingRunner) []string {
	var out []string
	for _, cmd := range r.RanCommands() {
		if strings.HasPrefix(cmd, "kill ") {
			out = append(out, cmd)
		}
	}
	return out
}

// Signalling init, or the process that is running the signal, is how a
// mistyped pattern ends a machine or a node's own report. Both are
// refused by name, before `kill` is asked for anything, and `force` is
// the way to say that it was meant (issue 321, section 5).
func TestPkillRefusesInitAndTheNodeItself(t *testing.T) {
	c, r := psProtectedCtx(t)
	_, err := New().Exec.Call(c, "ps.pkill", value.MapOf("pattern", ".*"))
	if err == nil {
		t.Fatal("ps.pkill '.*' signalled init and the node itself")
	}
	for _, want := range []string{" 1 ", fmt.Sprint(os.Getpid()), "force"} {
		if !strings.Contains(" "+err.Error()+" ", want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if k := psKills(r); len(k) != 0 {
		t.Errorf("kill was run despite the refusal: %v", k)
	}
}

func TestPkillStillSignalsAnOrdinaryProcess(t *testing.T) {
	c, r := psProtectedCtx(t)
	if _, err := New().Exec.Call(c, "ps.pkill", value.MapOf("pattern", "nginx")); err != nil {
		t.Fatal(err)
	}
	if k := psKills(r); len(k) != 1 || !strings.Contains(k[0], "900") {
		t.Errorf("kill ran as %v, want one signal to 900", k)
	}
}

func TestKillPIDRefusesInitAndTheNodeItself(t *testing.T) {
	for _, pid := range []int64{1, int64(os.Getpid())} {
		c, r := psProtectedCtx(t)
		if _, err := New().Exec.Call(c, "ps.kill_pid", value.MapOf("pid", pid)); err == nil {
			t.Errorf("ps.kill_pid signalled %d", pid)
		}
		if k := psKills(r); len(k) != 0 {
			t.Errorf("kill was run for %d: %v", pid, k)
		}
	}
}

// A question is not a signal: kill -0 asks whether a process exists, and
// refusing it would make the guard the thing that hides init.
func TestSignalZeroIsNotRefusedForInit(t *testing.T) {
	c, r := psProtectedCtx(t)
	if _, err := New().Exec.Call(c, "ps.kill_pid", value.MapOf("pid", int64(1), "signal", "0")); err != nil {
		t.Fatalf("kill -0 on init was refused: %v", err)
	}
	if k := psKills(r); len(k) != 1 {
		t.Errorf("kill -0 ran as %v", k)
	}
}

func TestForceSignalsAProtectedProcess(t *testing.T) {
	c, r := psProtectedCtx(t)
	if _, err := New().Exec.Call(c, "ps.kill_pid", value.MapOf("pid", int64(1), "signal", "HUP", "force", true)); err != nil {
		t.Fatalf("force did not allow it: %v", err)
	}
	if k := psKills(r); len(k) != 1 || !strings.Contains(k[0], "-HUP") {
		t.Errorf("kill ran as %v", k)
	}
}
