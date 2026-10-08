package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/repotree"
	"github.com/edlitmus/halite/internal/value"
)

// Effective answers for a key the file does not set with the value the
// code applies, which is what `config.get` and `opts` are documented to
// give. Before, a node with no `pki_dir` line answered None for it while
// keeping its keys in `<config root>/pki`. DIVERGENCE 5.242.
func TestEffectiveFillsInTheDefaults(t *testing.T) {
	staging := t.TempDir()
	cfg, err := Load(Node, LoadOptions{Root: staging, AllowMissing: true,
		Overrides: value.MapOf("log_level", "debug")})
	if err != nil {
		t.Fatal(err)
	}
	eff := cfg.Effective(Node)
	get := func(k string) any { v, _ := eff.Get(k); return v }

	// Under the root this configuration was loaded with, not the
	// platform's.
	if got := get("pki_dir"); got != filepath.Join(staging, "pki") {
		t.Errorf("pki_dir = %v, want %s", got, filepath.Join(staging, "pki"))
	}
	// Typed as the same text written in the file would be.
	if got := get("hub_port"); got != int64(4510) {
		t.Errorf("hub_port = %#v, want int64 4510", got)
	}
	if got := get("evidence"); got != true {
		t.Errorf("evidence = %#v, want true", got)
	}
	if got := get("tracing_sample_rate"); got != 0.1 {
		t.Errorf("tracing_sample_rate = %#v, want 0.1", got)
	}
	if got := get("hub_alive_interval"); got != "30s" {
		t.Errorf("hub_alive_interval = %#v, want \"30s\"", got)
	}
	// What the file sets wins over the default.
	if got := get("log_level"); got != "debug" {
		t.Errorf("log_level = %v, want the configured debug", got)
	}
	// A description is not a value, and an empty default stays absent so
	// that config.get's own default argument applies.
	if _, ok := eff.Get("timezone"); ok {
		t.Error("timezone's default is a description in angle brackets; it was filled in")
	}
	if _, ok := eff.Get("node_id"); ok {
		t.Error("node_id has no default; it was filled in")
	}
	// A key for another role is not this role's configuration.
	if _, ok := eff.Get("evidence_anchor_rate"); ok {
		t.Error("a hub key appeared in a node's effective configuration")
	}
	// Redacted is untouched: it is still exactly what the file says.
	if _, ok := cfg.Redacted().Get("pki_dir"); ok {
		t.Error("Effective changed what Redacted returns")
	}
}

// Every default in the table that is not a description must read back
// as something; a default that typedDefault mangles would be handed to
// every tree.
func TestEveryNodeAndHubDefaultIsFilledIn(t *testing.T) {
	for _, role := range []Role{Node, Hub, API} {
		cfg, err := Load(role, LoadOptions{Root: t.TempDir(), AllowMissing: true})
		if err != nil {
			t.Fatal(err)
		}
		eff := cfg.Effective(role)
		for _, k := range Keys {
			if !k.appliesTo(role) || k.Default == "" || strings.HasPrefix(k.Default, "<") ||
				isSecretKey(strings.ToLower(k.Name)) {
				continue
			}
			v, ok := eff.Get(k.Name)
			if !ok || v == nil {
				t.Errorf("role %d: %s has default %q and Effective has nothing", role, k.Name, k.Default)
			}
		}
	}
}

// rootRelative and the PathUnderRoot call sites must name the same keys
// with the same names under the root. A key read through PathUnderRoot
// but missing from rootRelative would be reported as the platform's path
// while the node used the root's; a pair that disagreed would report the
// wrong file.
func TestEveryRootRelativeKeyIsKnownToEffective(t *testing.T) {
	seen := map[string]bool{}
	root := filepath.Join("..", "..")
	err := repotree.Walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("%s: %v", path, perr)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "PathUnderRoot" {
				return true
			}
			key, ok1 := stringLit(call.Args[0])
			name, ok2 := stringLit(call.Args[1])
			if !ok1 || !ok2 {
				return true
			}
			seen[key] = true
			if want, ok := rootRelative[key]; !ok {
				t.Errorf("%s:%d reads %q with PathUnderRoot, and rootRelative does not "+
					"know it, so config.get reports the platform's path for it",
					path, fset.Position(call.Pos()).Line, key)
			} else if want != name {
				t.Errorf("%s:%d puts %q at %q under the root; rootRelative says %q",
					path, fset.Position(call.Pos()).Line, key, name, want)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("no PathUnderRoot call was read; this audit has stopped checking")
	}
	for key := range rootRelative {
		if !seen[key] {
			t.Errorf("rootRelative names %q, and nothing reads it with PathUnderRoot", key)
		}
	}
}
