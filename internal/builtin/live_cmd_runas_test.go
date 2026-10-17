//go:build unix

package builtin

import (
	"os"
	"os/user"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// **A script run as another account works** (issue 321, M-27). As root,
// which is how a node runs, cmd.exec_code wrote its temporary script
// root:root 0700 and handed it to nobody, so the child running as the
// account got "permission denied" on its own script. `nobody` exists on
// every unix this runs on. The first fix still failed on macOS, where the
// script was given to nobody inside root's private $TMPDIR.
func TestLiveScriptRunsAsAnotherAccount(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1; this runs a command as another account")
	}
	if os.Geteuid() != 0 {
		t.Skip("running as another account needs root; run it under sudo")
	}
	if _, err := user.Lookup("nobody"); err != nil {
		t.Skip("this machine has no `nobody` account")
	}
	// A $TMPDIR the account cannot enter, as macOS gives every account
	// and as a node started under sudo inherits. The script must not be
	// put there.
	t.Setenv("TMPDIR", t.TempDir())
	out, err := New().Exec.Call(realCtx(t), "cmd.exec_code",
		value.MapOf("lang", "/bin/sh", "code", "id -un\n", "runas", "nobody"))
	if err != nil {
		t.Fatalf("cmd.exec_code as nobody: %v", err)
	}
	if got := strings.TrimSpace(out.(string)); got != "nobody" {
		t.Errorf("the script ran as %q, want nobody", got)
	}
}
