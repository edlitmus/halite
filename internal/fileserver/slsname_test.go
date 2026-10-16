package fileserver

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/value"
)

// The compiler's state_denylist and the file server's loader are a pair
// that must agree on what an SLS name means, and they did not: the
// denylist matched the name as written, and the loader turned dots into
// slashes and cleaned the result. So a denylist entry for `secret.x`
// refused `secret.x` and nothing else, while `secret..x`, `secret/x`,
// `.secret.x` and `secret/./x` all loaded `secret/x.sls`. DIVERGENCE
// 5.294.
//
// These tests run the real compiler against the real Roots on a real
// directory, because the in-memory loader the state package's tests use
// is keyed on the name as written and cannot fold spellings -- which is
// exactly why the hole was invisible there.

// slsTree lays out a small tree with one secret, a relative include in
// a package (`web/init.sls` -> `.app`), a relative include in a flat
// file, and files that try to reach the secret by include.
func slsTree(t *testing.T) *Roots {
	t.Helper()
	root := filepath.Join(t.TempDir(), "srv", "salt")
	files := map[string]string{
		"secret/x.sls":   "secret_x:\n  test.nop: []\n",
		"ok.sls":         "ok:\n  test.nop: []\n",
		"web/init.sls":   "include:\n  - .app\n\nweb:\n  test.nop: []\n",
		"web/app.sls":    "web_app:\n  test.nop: []\n",
		"web/nginx.sls":  "include:\n  - .app\n\nnginx:\n  test.nop: []\n",
		"secret/y.sls":   "include:\n  - .x\n\nsecret_y:\n  test.nop: []\n",
		"sneak/dots.sls": "include:\n  - secret..x\n\nsneak:\n  test.nop: []\n",
		"sneak/path.sls": "include:\n  - secret/x\n\nsneak:\n  test.nop: []\n",
		"sneak/rel.sls":  "include:\n  - ..secret..x\n\nsneak:\n  test.nop: []\n",
	}
	for name, body := range files {
		write(t, filepath.Join(root, filepath.FromSlash(name)), body)
	}
	return NewRoots(map[string][]string{"base": {root}})
}

func slsRegistry() *signature.Registry {
	r := signature.NewRegistry()
	r.Add(signature.Signature{Module: "test", Function: "nop", Params: []signature.Param{
		{Name: "name", Type: signature.String},
	}})
	return r
}

func compileTree(roots *Roots, cfg state.Config, names ...string) *state.Compiled {
	cfg.NodeID = "n"
	cfg.Grains = value.MapOf("os_family", "Debian", "os", "Ubuntu")
	c := &state.Compiler{Loader: roots, Registry: slsRegistry(), Config: cfg}
	if len(names) == 0 {
		return c.CompileHighstate()
	}
	return c.CompileSLS(names)
}

func declares(c *state.Compiled, id string) bool {
	_, ok := c.High.Lookup(id)
	return ok
}

func TestEverySpellingOfADeniedSLSIsRefused(t *testing.T) {
	roots := slsTree(t)
	deny := state.Config{StateDenylist: []string{"secret.x"}}

	// The spelling the denylist names, and every other spelling the
	// loader used to fold onto the same file.
	for _, name := range []string{"secret.x", "secret..x", "secret/x", ".secret.x", "secret/./x", "secret.x.", "/secret/x", `secret\x`} {
		out := compileTree(roots, deny, name)
		if out.Err() == nil {
			t.Errorf("state.sls %q compiled under state_denylist [secret.x]", name)
		}
		if declares(out, "secret_x") {
			t.Errorf("state.sls %q declared secret_x, which is secret/x.sls, under state_denylist [secret.x]", name)
		}
	}

	// Through include:, written absolutely in two other spellings, and
	// relatively in a spelling with doubled dots.
	for _, name := range []string{"sneak.dots", "sneak.path", "sneak.rel"} {
		out := compileTree(roots, deny, name)
		if declares(out, "secret_x") {
			t.Errorf("%s reached secret/x.sls by include under state_denylist [secret.x]:\n%v", name, out.Err())
		}
		if out.Err() == nil {
			t.Errorf("%s compiled without error, but its include names secret/x.sls in a refused spelling", name)
		}
	}

	// A relative include resolves to the absolute name before the
	// denylist sees it: `.x` inside `secret.y` is `secret.x`, and is
	// refused as `secret.x`.
	out := compileTree(roots, deny, "secret.y")
	if declares(out, "secret_x") {
		t.Errorf("secret.y reached secret.x by a relative include under state_denylist [secret.x]")
	}
	if !strings.Contains(out.Err().Error(), "excluded by state_allowlist or state_denylist") {
		t.Errorf("secret.y's relative include was not refused by the denylist:\n%v", out.Err())
	}
}

// The top file is the third way a name arrives, and it goes through the
// same check.
func TestATopFileSpellingOfADeniedSLSIsRefused(t *testing.T) {
	roots := slsTree(t)
	write(t, filepath.Join(roots.Dirs["base"][0], "top.sls"), "base:\n  '*':\n    - ok\n    - secret/x\n")
	out := compileTree(roots, state.Config{StateDenylist: []string{"secret.x"}})
	if declares(out, "secret_x") {
		t.Errorf("a top file entry `secret/x` reached secret/x.sls under state_denylist [secret.x]")
	}
	if out.Err() == nil {
		t.Errorf("a top file naming `secret/x` compiled without error")
	}
}

// What must still work: plain names, packages, and Salt's relative
// includes in both a package and a flat file.
func TestCanonicalNamesAndRelativeIncludesStillCompile(t *testing.T) {
	roots := slsTree(t)
	deny := state.Config{StateDenylist: []string{"secret.x"}}
	for name, want := range map[string][]string{
		"ok":        {"ok"},
		"web":       {"web", "web_app"},
		"web.nginx": {"nginx", "web_app"},
	} {
		out := compileTree(roots, deny, name)
		if err := out.Err(); err != nil {
			t.Errorf("%s did not compile: %v", name, err)
			continue
		}
		for _, id := range want {
			if !declares(out, id) {
				t.Errorf("%s did not declare %s", name, id)
			}
		}
	}

	// The allowlist direction already failed closed; it still admits
	// the canonical names it names.
	out := compileTree(roots, state.Config{StateAllowlist: []string{"web*"}}, "web")
	if err := out.Err(); err != nil || !declares(out, "web_app") {
		t.Errorf("state_allowlist [web*] refused web or its relative include: %v", err)
	}
}

// The loader refuses a non-canonical name on its own, so a caller that
// reaches Source without the compiler cannot fold spellings either.
func TestRootsSourceRefusesANonCanonicalName(t *testing.T) {
	roots := slsTree(t)
	if _, _, err := roots.Source("base", "secret.x"); err != nil {
		t.Fatalf("Source(secret.x) = %v; the canonical name must load", err)
	}
	for _, name := range []string{"secret..x", "secret/x", ".secret.x", "secret/./x", ""} {
		if _, _, err := roots.Source("base", name); err == nil {
			t.Errorf("Source(%q) loaded a file; a non-canonical name must be refused", name)
		}
	}
}
