package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/value"
)

// `file.recurse` copies a nested tree, against a real local file server.
//
// Separate from the conformance case on purpose: the harness checks test
// mode and idempotence, and a recurse that copied only the top level would
// satisfy both. This checks the thing recurse is for.
//
// It needs no hub and no machine. The state refuses without an
// `exec.FileLister`, saying it wants "a tree, which a node running against
// a hub or its own roots has" -- and the roots half is
// `fileserver.Fetcher`, whose ListUnder works over a local tree. So a
// directory this test writes is a file server. DIVERGENCE 5.157.
func TestFileRecurseCopiesANestedTree(t *testing.T) {
	r := New()
	roots, dest := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(roots, "tree", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, b := range map[string]string{
		filepath.Join(roots, "tree", "one.conf"):           "one\n",
		filepath.Join(roots, "tree", "nested", "two.conf"): "two\n",
	} {
		if err := os.WriteFile(p, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := newCtx(false)
	c.Files = fileserver.NewFetcher(fileserver.NewRoots(map[string][]string{"base": {roots}}))
	var _ exec.FileLister = c.Files.(exec.FileLister)

	out := filepath.Join(dest, "copied")
	res, err := r.States.Call(c, "file.recurse",
		value.MapOf("name", out, "source", "salt://tree"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Result == nil || !*res.Result {
		t.Fatalf("recurse did not succeed: %+v", res)
	}
	var found []string
	_ = filepath.Walk(out, func(p string, _ os.FileInfo, _ error) error {
		rel, _ := filepath.Rel(out, p)
		found = append(found, rel)
		return nil
	})
	for _, want := range []string{"one.conf", filepath.Join("nested", "two.conf")} {
		if !strings.Contains(strings.Join(found, "|"), want) {
			t.Errorf("the copy is missing %s; it holds %v", want, found)
		}
	}
}
