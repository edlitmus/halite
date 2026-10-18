//go:build linux

package builtin

import (
	"os/exec"
	"strings"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// **A user name longer than eight characters matches** (issue 321,
// section 5). procps prints `user` eight wide and marks a longer name with
// a `+` -- `systemd+` for systemd-resolve, on the Ubuntu the audit and this
// were run on -- so `user: systemd-resolve` matched nothing. Read-only,
// against the real ps: it finds an account running here whose name is
// longer than eight, and skips saying so where there is none.
func TestPsUserFilterMatchesALongName(t *testing.T) {
	out, err := exec.Command("ps", "-e", "--no-headers", "-o", "user:256").Output()
	if err != nil {
		t.Skipf("this ps does not take -o user:256: %v", err)
	}
	long := ""
	for _, u := range strings.Fields(string(out)) {
		if len(u) > 8 {
			long = u
			break
		}
	}
	if long == "" {
		t.Skip("no process here runs as an account whose name is longer than eight characters")
	}
	c := newCtx(false)
	c.Runner = &hexec.OSRunner{}
	got, err := New().Exec.Call(c, "ps.pgrep", value.MapOf("pattern", ".", "user", long))
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := got.([]any); len(list) == 0 {
		t.Errorf("no process matched user %q, which `ps -o user:256` shows running", long)
	}
}
