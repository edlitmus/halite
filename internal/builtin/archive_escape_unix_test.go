//go:build unix

package builtin

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
)

// The ownership pass runs as root over the archive's names. A directory
// on the way that is a link out of the destination must not hand what it
// points at to `group:`. Checked with a group change, which an
// unprivileged account can make on its own files, so this runs without
// root.
func TestArchiveOwnershipIsNotAppliedThroughASymlink(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip("no current user")
	}
	gids, err := me.GroupIds()
	if err != nil {
		t.Skipf("cannot list this account's groups: %v", err)
	}

	base, dest := nestedDest(t)
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret")
	if err := os.WriteFile(secret, []byte("s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(secret)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("no unix ownership on this platform")
	}
	var other string
	for _, g := range gids {
		if g != strconv.FormatUint(uint64(st.Gid), 10) {
			other = g
			break
		}
	}
	if other == "" {
		t.Skip("this account is in only one group, so there is no group change to attempt")
	}
	if err := os.Symlink(outside, filepath.Join(dest, "evil")); err != nil {
		t.Fatal(err)
	}

	_, err = enforceArchiveOwnership(&exec.Context{}, dest, []string{"evil/secret"}, "", other)
	if err == nil {
		t.Error("ownership was enforced through a link out of the destination without complaint")
	}
	after, statErr := os.Lstat(secret)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if got := after.Sys().(*syscall.Stat_t).Gid; got != st.Gid {
		t.Fatalf("the file outside the destination changed group from %d to %d", st.Gid, got)
	}
}
