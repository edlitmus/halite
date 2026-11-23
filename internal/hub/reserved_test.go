package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/fileserver"
)

// The hub refuses an environment whose `_halite/` holds anything but the
// signed manifest and its signature, from every node, listing and files
// alike, naming the file, and says so in its log once.
func TestTheHubRefusesATreeWithAnythingElseInHalite(t *testing.T) {
	l := newLab(t).withFiles(t, map[string]string{
		"top.sls":               "base:\n  '*':\n    - web\n",
		"_halite/manifest.json": "{}\n",
		"_halite/manifest.sig":  "x\n",
	})
	node := l.enrolled(t, "web1.example")
	read := func() error {
		_, err := fileserver.NewRemote(node, t.TempDir(), nil).Read("base", "top.sls")
		return err
	}
	list := func() error {
		_, err := fileserver.NewRemote(node, t.TempDir(), nil).ManifestFor("base", "")
		return err
	}
	if err := read(); err != nil {
		t.Fatalf("a _halite/ holding the two files: %v", err)
	}
	if err := os.WriteFile(filepath.Join(l.root, "_halite", "notes.txt"), []byte("hidden\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, ask := range []func() error{read, list, read} {
		err := ask()
		if err == nil || !strings.Contains(err.Error(), "notes.txt") || !strings.Contains(err.Error(), "refuses to serve") {
			t.Fatalf("got %v", err)
		}
	}
	if n := strings.Count(l.logs.String(), "refusing to serve an environment whose _halite/"); n != 1 {
		t.Errorf("the hub logged the refusal %d times", n)
	}
	if err := os.Remove(filepath.Join(l.root, "_halite", "notes.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(l.root, "_halite", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := read(); err == nil || !strings.Contains(err.Error(), "holds sub") {
		t.Fatalf("a directory inside _halite/: %v", err)
	}
}
