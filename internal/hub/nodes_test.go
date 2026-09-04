package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/fileperm/permtest"
)

// MkdirAll is satisfied by a directory that already exists, whoever owns
// it — so a cache left behind by a hand-run as root was opened without
// complaint by a hub running as its service account, which could then
// read nothing in it. Every target matched no node, because a node whose
// cached data cannot be read is skipped, and the only clue was a warning
// in a log the operator was not reading.
func TestOpeningAnUnusableNodeCacheFailsAtOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nodes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Made unwritable the way the platform does it. A chmod is not that
	// on Windows: os.Chmod(dir, 0o500) returned nil, changed nothing,
	// and this test asserted a refusal that never came.
	permtest.DenyWrite(t, dir)

	if _, err := OpenNodeCache(dir); err == nil {
		t.Fatal("a node cache this process cannot write was opened without complaint")
	} else if !strings.Contains(err.Error(), dir) {
		t.Errorf("the failure does not name the directory: %v", err)
	}

	// And a usable one still opens, leaving nothing behind.
	permtest.AllowWrite(t, dir)
	if _, err := OpenNodeCache(dir); err != nil {
		t.Fatalf("a usable node cache was refused: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("opening the cache left %d file(s) behind", len(entries))
	}
}
