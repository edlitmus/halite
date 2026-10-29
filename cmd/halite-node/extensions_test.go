package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/config"
)

// --root moves the configuration root and what is written in terms of
// it; state_dir, and so the extension cache, is under /var and does not
// follow it (DIVERGENCE 5.143, `docs/configuration.md`). Issue 312 read
// the cache warning from a `--root <tmpdir>` run as a defect; this holds
// that it is the design, and that setting state_dir is how a run is
// isolated from the real cache.
func TestTheExtensionCacheDoesNotFollowRoot(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	load := func(extra string) *node {
		path := filepath.Join(t.TempDir(), "node.yaml")
		if err := os.WriteFile(path, []byte(extra), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load(config.Node, config.LoadOptions{Path: path, Root: root})
		if err != nil {
			t.Fatal(err)
		}
		return &node{cfg: cfg}
	}
	if got, want := load("{}\n").extensionDir(), filepath.Join(config.DefaultStateDir, "ext"); got != want {
		t.Errorf("extensionDir() under --root %s = %q, want %q: --root must not relocate it", root, got, want)
	}
	if got, want := load("state_dir: "+state+"\n").extensionDir(), filepath.Join(state, "ext"); got != want {
		t.Errorf("extensionDir() with state_dir = %q, want %q", got, want)
	}
}
