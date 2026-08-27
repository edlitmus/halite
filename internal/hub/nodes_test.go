package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MkdirAll is satisfied by a directory that already exists, whoever owns
// it — so a cache left behind by a hand-run as root was opened without
// complaint by a hub running as its service account, which could then
// read nothing in it. Every target matched no node, because a node whose
// cached data cannot be read is skipped, and the only clue was a warning
// in a log the operator was not reading.
func TestOpeningAnUnusableNodeCacheFailsAtOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nodes")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := OpenNodeCache(dir); err == nil {
		t.Fatal("a node cache this process cannot write was opened without complaint")
	} else if !strings.Contains(err.Error(), dir) {
		t.Errorf("the failure does not name the directory: %v", err)
	}

	// And a usable one still opens, leaving nothing behind.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
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
