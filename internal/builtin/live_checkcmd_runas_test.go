//go:build unix

package builtin

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"testing"

	hexec "github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/value"
)

// **A checker run as another account can still read its private file**
// (issue 321, section 5). The file is 0600 now, so it is given to the
// account; `cat` as nobody must read it, and the state must succeed. As
// root, with a TMPDIR nobody cannot enter, as macOS gives and a node
// started under sudo inherits.
func TestLiveCheckCmdRunsAsAnotherAccount(t *testing.T) {
	if os.Getenv("HALITE_SYSTEM_LIVE") != "1" {
		t.Skip("set HALITE_SYSTEM_LIVE=1; this runs a checker as another account")
	}
	if os.Geteuid() != 0 {
		t.Skip("running a checker as another account needs root; run it under sudo")
	}
	if _, err := user.Lookup("nobody"); err != nil {
		t.Skip("this machine has no `nobody` account")
	}
	t.Setenv("TMPDIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "secret")
	c := &hexec.Context{
		Ctx:      context.Background(),
		Grains:   value.MapOf("os", "Ubuntu", "os_family", "Debian"),
		Pillar:   value.NewMap(0),
		Config:   value.NewMap(0),
		NodeID:   "test.node",
		Env:      "base",
		CheckCmd: []string{"cat"},
		RunAs:    "nobody",
		Runner:   &hexec.OSRunner{},
	}
	res, err := New().States.Call(c, "file.managed", value.MapOf("name", path, "contents", "hunter2\n", "mode", "0600"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Succeeded() {
		t.Fatalf("a checker run as nobody could not read its file: %s", res.Comment)
	}
}
