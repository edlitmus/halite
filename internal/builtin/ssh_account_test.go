//go:build unix

package builtin

import (
	"os"
	"path/filepath"
	"testing"
)

// pathInHome decides which `config` paths go through the account's root
// (issue 321, A10), and the live test that shows the root refusing a link
// needs root and a throwaway account. The decision itself does not, and
// the case it is easiest to get wrong is the second name for the home:
// /home/deploy reached as /usr/home/deploy on a FreeBSD host whose /home
// is a link, which a lexical comparison misses and so sends by path.
func TestPathInHome(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home", "deploy")
	if err := os.MkdirAll(filepath.Join(home, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(filepath.Join(dir, "home"), alias); err != nil {
		t.Fatal(err)
	}
	// The account's own link back to its home, inside it.
	if err := os.Symlink(home, filepath.Join(home, "loop")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		path string
		rel  string
		in   bool
	}{
		{filepath.Join(home, ".ssh", "authorized_keys"), filepath.Join(".ssh", "authorized_keys"), true},
		{filepath.Join(home, "a", "..", ".ssh", "known_hosts"), filepath.Join(".ssh", "known_hosts"), true},
		{filepath.Join(alias, "deploy", ".ssh", "authorized_keys"), filepath.Join(".ssh", "authorized_keys"), true},
		// The shallowest match is the home, so the account's link stays
		// in rel, where the write refuses it.
		{filepath.Join(alias, "deploy", "loop", "real", "keys"), filepath.Join("loop", "real", "keys"), true},
		{filepath.Join(home, "..", "other", ".ssh", "authorized_keys"), "", false},
		{filepath.Join(outside, "authorized_keys"), "", false},
		{filepath.Join(dir, "home", "deployx", "authorized_keys"), "", false},
	} {
		rel, in := pathInHome(home, c.path)
		if in != c.in || rel != c.rel {
			t.Errorf("pathInHome(%s) = %q, %v; want %q, %v", c.path, rel, in, c.rel, c.in)
		}
	}
}
