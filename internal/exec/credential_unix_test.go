//go:build unix

package exec

import (
	"context"
	"os"
	"os/user"
	"strings"
	"testing"
)

// macOS will not take more supplementary groups than NGROUPS_MAX, and
// the failure when it is handed more says nothing about groups.
//
// `fork/exec /usr/bin/defaults: invalid argument` is what came back --
// naming a binary that is present, executable and innocent. A CI
// account is in dozens of groups where a personal one is in a handful,
// so this was reachable only from a machine nobody had run it on.
func TestSupplementaryGroupsAreCappedOnDarwinAlone(t *testing.T) {
	many := make([]uint32, 40)
	for i := range many {
		many[i] = uint32(i + 20)
	}

	got := capGroups("darwin", many)
	if len(got) != darwinMaxGroups {
		t.Errorf("darwin kept %d groups, which setgroups refuses; want %d",
			len(got), darwinMaxGroups)
	}
	// The primary group leads the list and has to survive the trim, or
	// the child runs without the group it was given.
	if len(got) > 0 && got[0] != many[0] {
		t.Errorf("the first group became %d, want %d", got[0], many[0])
	}

	for _, goos := range []string{"linux", "freebsd", "openbsd", "netbsd", "illumos"} {
		if n := len(capGroups(goos, many)); n != len(many) {
			t.Errorf("%s kept %d of %d groups; the cap is macOS's alone and "+
				"dropping them anywhere else removes access the account has",
				goos, n, len(many))
		}
	}

	// A list already within the cap is handed back untouched, including
	// on darwin -- the common case, and the one a personal Mac hits.
	few := []uint32{20, 21, 22}
	if got := capGroups("darwin", few); len(got) != len(few) {
		t.Errorf("a short list was trimmed to %d", len(got))
	}
	if capGroups("darwin", nil) != nil {
		t.Error("a nil group list did not come back nil")
	}
}

// The credential reaches a background child, for an account that exists.
//
// This cannot watch a process change account without root, so it uses
// the one observable thing an unprivileged run has: switching to any
// account, even this one, means setgroups(2), and the kernel refuses
// that to a process that is not root. A command whose credential was
// applied therefore fails to start here, and one whose credential was
// dropped starts and returns a pid. The foreground half is the control,
// so that a platform which lets setgroups through fails loudly rather
// than making the background half pass for the wrong reason.
func TestRunAsReachesABackgroundChild(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("as root the switch succeeds, and a released child's account cannot be read back portably; this test needs an unprivileged run")
	}
	me, err := user.Current()
	if err != nil {
		t.Skipf("no current account to name: %v", err)
	}
	r := &OSRunner{}
	for _, bg := range []bool{false, true} {
		cmd := helperCommand("echo")
		cmd.RunAs = me.Username
		cmd.Background = bg
		res, err := r.Run(context.Background(), cmd)
		if err == nil {
			t.Errorf("background=%v: runas %q started without a credential switch (stdout %q)", bg, me.Username, res.Stdout)
			continue
		}
		if !strings.Contains(err.Error(), "operation not permitted") {
			t.Errorf("background=%v: want the kernel's refusal of setgroups, got %v", bg, err)
		}
	}
}
