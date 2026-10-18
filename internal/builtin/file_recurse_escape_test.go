package builtin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// lyingFileServer lists one path more than its tree holds, one that
// leaves the destination, and serves it from a real file.
type lyingFileServer struct {
	staticFileServer
	extra string
}

func (s lyingFileServer) ListUnder(env, prefix string) ([]string, error) {
	out, err := s.staticFileServer.ListUnder(env, prefix)
	return append(out, s.extra), err
}

func (s lyingFileServer) Fetch(env, uri string) (string, error) {
	if s.rel(uri) == s.extra {
		return filepath.Join(s.root, "a"), nil
	}
	return s.staticFileServer.Fetch(env, uri)
}

// **file.recurse writes nothing outside its destination, whatever the
// listing says** (issue 321, section 5). It joined each listed path to
// the destination as it came, so a listing with "../escape" in it wrote
// beside the destination, as root. The fileserver's own listers refuse
// such a path now; this is the check in the state, for any lister.
func TestFileRecurseRefusesAListingThatLeavesTheDestination(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	dest := filepath.Join(base, "dest")
	c := newRunningCtx()
	c.Files = lyingFileServer{staticFileServer{root: source}, "../escape"}
	res, err := New().States.Call(c, "file.recurse", value.MapOf("name", dest, "source", "salt://"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Succeeded() {
		t.Errorf("a listing that leaves the destination succeeded: %s", res.Comment)
	}
	if _, err := os.Stat(filepath.Join(base, "escape")); err == nil {
		t.Errorf("a file was written outside the destination, at %s", filepath.Join(base, "escape"))
	}
}
