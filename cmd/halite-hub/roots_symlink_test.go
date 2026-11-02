package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// A file root that is a symlink to the hub's own state is refused, and so is
// a hub whose state directory is a link to a served tree. The check compared
// absolute paths, which a link defeats. DIVERGENCE 5.540.
func TestAFileRootThatIsALinkToTheHubsStateIsRefused(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	tree := filepath.Join(root, "tree")
	for _, d := range []string{state, tree} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	toState := filepath.Join(root, "srv-salt")
	if err := os.Symlink(state, toState); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}
	stateLink := filepath.Join(root, "state-link")
	if err := os.Symlink(tree, stateLink); err != nil {
		t.Skipf("cannot make a symlink here: %v", err)
	}

	for name, cfg := range map[string]struct {
		stateDir string
		served   string
	}{
		"a served link to the state directory": {state, toState},
		"a state directory that is a link":     {stateLink, tree},
	} {
		if err := os.WriteFile(filepath.Join(root, "hub.yaml"), []byte("state_dir: "+cfg.stateDir+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		h := openHubForConfig(&cli.Args{Flags: map[string]string{"root": root}})
		if err := checkRootsAreNotTheHubsOwn(h, map[string][]string{"base": {cfg.served}}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// A link to somewhere else is still fine.
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	fine := filepath.Join(root, "fine")
	if err := os.Symlink(other, fine); err != nil {
		t.Skip(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hub.yaml"), []byte("state_dir: "+state+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := openHubForConfig(&cli.Args{Flags: map[string]string{"root": root}})
	if err := checkRootsAreNotTheHubsOwn(h, map[string][]string{"base": {fine}}); err != nil {
		t.Errorf("a link to a separate tree was refused: %v", err)
	}
}
