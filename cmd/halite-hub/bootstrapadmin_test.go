package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

// The bootstrap policy is given to its directory's owner. Run as root,
// as the handbook says, it was left root's and 0600, and a hub running
// as its own account could not read it and would not start.
//
// The chown itself needs root, so this sees it asked for. That
// keystore.InheritOwner does what it says is held by the key store's
// own tests (DIVERGENCE 5.285), and the whole of it was run as root on
// beastie.
func TestTheBootstrapPolicyTakesItsDirectorysOwner(t *testing.T) {
	root := t.TempDir()
	cfg, err := config.Load(config.Hub, config.LoadOptions{Root: root, AllowMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	var gotPath, gotDir string
	saved := adoptOwner
	adoptOwner = func(path, dir string) error {
		gotPath, gotDir = path, dir
		return nil
	}
	t.Cleanup(func() { adoptOwner = saved })

	path, err := bootstrapAdmin(cfg, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if gotPath != path || gotDir != filepath.Dir(path) {
		t.Errorf("the policy at %s was not given to its directory's owner (asked for %q in %q)",
			path, gotPath, gotDir)
	}
}
