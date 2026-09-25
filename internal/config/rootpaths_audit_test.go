package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// A setting documented as living under the configuration root resolves
// against the root the operator named.
//
// # What was wrong
//
// `--root <dir>` is documented, on all three binaries and in all three
// manual pages, as "the configuration root". It relocated the
// configuration file and its drop-in directory, and nothing that the
// configuration file describes:
//
//	$ halite-hub policy show --root /tmp/staging
//	policy /usr/local/etc/halite/policy.yaml
//
//	$ halite-hub keys list --root /tmp/staging
//	halite: there is no enrollment CA in /usr/local/etc/halite/pki
//
// Both measured on a host this project manages, so the first one printed
// the live estate's rules while being asked about a throwaway directory.
// An operator comparing a staging policy against production reads
// production twice and finds no difference.
//
// The cause is the shape this project keeps finding: two things that must
// agree, with nothing holding them together. `pki_dir` is documented as
// `<config root>/pki` and `policy` as `<config root>/policy.yaml`, and in
// the code both were written as constants -- `config.DefaultPKIDir`,
// `config.DefaultPolicy` -- fixed at build time from the *platform's*
// root. `Load` took the root as an argument and discarded it, so nothing
// could have resolved them correctly even in principle.
//
// # What this checks
//
// The behavioural half is TestPathUnderRootFollowsTheLoadedRoot below.
// This half is static, and it is the one that matters over time: a new
// call site written the old way compiles, runs, and is wrong only when
// somebody passes `--root`. It reads every two-argument Config accessor
// in the tree and fails on a fallback derived from the build-time root.
//
// `DefaultStateDir`, `DefaultCacheDir`, `DefaultLogDir` and
// `DefaultSocketDir` are deliberately not in the forbidden set: those
// live under `/var`, not under the configuration root, and `--root` is not
// documented as moving them. Only the two that SPEC 27.3 places under the
// root are wrong to use this way.
//
// DIVERGENCE 5.143.
func TestNoRootDerivedConstantIsAConfigFallback(t *testing.T) {
	var checked int
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "bin", "dist", "testdata", "contrib":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s: %v", path, parseErr)
		}
		rel, _ := filepath.Rel(root, path)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "String" {
				return true
			}
			key, ok := stringLit(call.Args[0])
			if !ok {
				return true
			}
			checked++
			line := fset.Position(call.Pos()).Line

			if name, why := rootConstant(call.Args[1]); name != "" {
				t.Errorf("%s:%d falls back to config.%s for %q, which is the root this "+
					"binary was built for and not the one --root named. %s, so use "+
					"cfg.PathUnderRoot(%q, …) instead.", rel, line, name, key, why, key)
				return true
			}
			if joinsDefaultRoot(call.Args[1]) {
				t.Errorf("%s:%d falls back to a path joined onto config.DefaultRoot for "+
					"%q, which ignores --root. Use cfg.PathUnderRoot(%q, …), which joins "+
					"onto the root this configuration was loaded with.", rel, line, key, key)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no Config.String call was read; this audit has stopped checking")
	}
	t.Logf("read %d two-argument Config.String call(s) for a build-time root fallback", checked)
}

// rootConstant names the forbidden constant an expression is, or "".
func rootConstant(e ast.Expr) (name, why string) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	switch sel.Sel.Name {
	case "DefaultPKIDir":
		return "DefaultPKIDir", "`pki_dir` is documented as `<config root>/pki`"
	case "DefaultPolicy":
		return "DefaultPolicy", "`policy` is documented as `<config root>/policy.yaml`"
	}
	return "", ""
}

// joinsDefaultRoot reports whether an expression is filepath.Join with
// DefaultRoot as a term, which is the longhand of the same mistake.
func joinsDefaultRoot(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Join" {
		return false
	}
	for _, arg := range call.Args {
		if inner, ok := arg.(*ast.SelectorExpr); ok && inner.Sel.Name == "DefaultRoot" {
			return true
		}
		if ident, ok := arg.(*ast.Ident); ok && ident.Name == "DefaultRoot" {
			return true
		}
	}
	return false
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// PathUnderRoot follows the root a configuration was loaded with.
//
// The direct half of the same check: the audit above stops a call site
// being written the old way, and this one stops the helper it is told to
// use from being wrong.
func TestPathUnderRootFollowsTheLoadedRoot(t *testing.T) {
	staging := t.TempDir()
	cfg, err := Load(Hub, LoadOptions{Root: staging, AllowMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Root(); got != staging {
		t.Errorf("Root() = %q, want the root it was loaded with, %q", got, staging)
	}
	for _, c := range []struct{ key, name string }{
		{"pki_dir", "pki"},
		{"policy", "policy.yaml"},
	} {
		want := filepath.Join(staging, c.name)
		if got := cfg.PathUnderRoot(c.key, c.name); got != want {
			t.Errorf("PathUnderRoot(%q, %q) = %q, want %q", c.key, c.name, got, want)
		}
	}

	// A setting in the file still wins, because --root supplies the
	// default and a configuration that names a path means it.
	named := filepath.Join(t.TempDir(), "elsewhere")
	overrides := value.MapOf("pki_dir", named)
	cfg, err = Load(Hub, LoadOptions{Root: staging, AllowMissing: true, Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.PathUnderRoot("pki_dir", "pki"); got != named {
		t.Errorf("PathUnderRoot with pki_dir set = %q, want the setting %q", got, named)
	}

	// A Config built by a struct literal rather than by Load -- which
	// tests in this tree do -- answers the platform default rather than
	// an empty path joined to a name.
	bare := &Config{Role: Hub}
	if got := bare.PathUnderRoot("pki_dir", "pki"); got != filepath.Join(DefaultRoot, "pki") {
		t.Errorf("a Config with no root resolved to %q", got)
	}
}
