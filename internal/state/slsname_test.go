package state

import (
	"fmt"
	"path"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/template"
)

// CanonicalSLS is the one spelling. DIVERGENCE 5.290.
func TestCanonicalSLS(t *testing.T) {
	for _, ok := range []string{"top", "secret.x", "web.nginx.app", "a-b.c_d", "x1.2"} {
		if got, err := CanonicalSLS(ok); err != nil || got != ok {
			t.Errorf("CanonicalSLS(%q) = %q, %v; want it accepted unchanged", ok, got, err)
		}
	}
	for bad, suggest := range map[string]string{
		"secret..x":  "secret.x",
		"secret/x":   "secret.x",
		".secret.x":  "secret.x",
		"secret/./x": "secret.x",
		"secret.x.":  "secret.x",
		"/secret/x":  "secret.x",
		`secret\x`:   "secret.x",
		"..":         "",
		".":          "",
	} {
		_, err := CanonicalSLS(bad)
		if err == nil {
			t.Errorf("CanonicalSLS(%q) accepted a non-canonical name", bad)
			continue
		}
		if got := suggestSLS(bad); got != suggest {
			t.Errorf("suggestSLS(%q) = %q, want %q", bad, got, suggest)
		}
	}
	if _, err := CanonicalSLS(""); err == nil {
		t.Error("CanonicalSLS accepted the empty name")
	}
}

// foldingLoader folds spellings the way a file server does -- dots to
// slashes, then a path clean -- and checks nothing itself. It stands for
// any Loader that is not fileserver.Roots, so that the compiler's own
// check is tested without the file server's second one behind it.
type foldingLoader struct{ files map[string]string }

func (f foldingLoader) Source(env, sls string) ([]byte, string, error) {
	rel := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(strings.ReplaceAll(sls, "\\", "/"), ".", "/")), "/")
	if src, ok := f.files[rel]; ok {
		return []byte(src), rel + ".sls", nil
	}
	return nil, "", fmt.Errorf("%w: %s", ErrNotFound, sls)
}
func (f foldingLoader) Envs() []string                   { return []string{"base"} }
func (f foldingLoader) Templates(string) template.Loader { return nil }

func TestTheCompilerRefusesANonCanonicalNameWhateverTheLoader(t *testing.T) {
	l := foldingLoader{files: map[string]string{"secret/x": "secret_x:\n  test.nop: []\n"}}
	for _, name := range []string{"secret..x", "secret/x", ".secret.x", "secret/./x"} {
		c := &Compiler{Loader: l, Registry: testRegistry(), Config: Config{NodeID: "n", StateDenylist: []string{"secret.x"}}}
		out := c.CompileSLS([]string{name})
		if _, ok := out.High.Lookup("secret_x"); ok {
			t.Errorf("state.sls %q reached secret/x through a folding loader under state_denylist [secret.x]", name)
		}
	}
}
