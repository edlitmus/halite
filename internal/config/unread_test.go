package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A declared setting that nothing reads is the defect this project keeps
// finding in itself: `cmd_default_shell` was one, the per-state `timeout`
// was one, and the `salt` dispatcher was the same shape a layer down.
// Each was plumbed the whole way and never populated, so a tree could
// set it, `--help` could document it, the configuration reference could
// list it, and nothing would happen.
//
// This walks the other way: every key in Keys must be mentioned somewhere
// outside keys.go, or be listed below with the reason it is not.
//
// The list is enforced in both directions. A key that starts being read
// must come off it, because a stale entry here hides the next real one.
// waiverFor answers why a declared key is unread, and whether anything
// accounts for it at all.
//
// Two maps, both in the package rather than in this test: InertKeys are
// requests the services refuse out loud at startup, UnreadKeys are the
// rest. They used to be one map here, and its reasons cited phases —
// "phase 2: there is no job cache" on a build where phase 2 had
// shipped. An expired excuse in a test file is invisible, which is why
// the guard that reads them now lives beside the phase list.
func waiverFor(name string) (string, bool) {
	if effect, ok := InertKeys[name]; ok {
		return "inert: " + effect, true
	}
	reason, ok := UnreadKeys[name]
	return reason, ok
}

func TestEveryDeclaredKeyIsReadOrRecorded(t *testing.T) {
	root := ".."
	var body strings.Builder
	err := filepath.Walk(filepath.Join(root, ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// keys.go declares them and shim.go names the halite spelling a
		// Salt key maps onto. Neither reads one, and counting either as
		// a use is how `log_level_file` looked read when nothing reads
		// it.
		//
		// Tests are excluded for the same reason and it is the sharper
		// one: a test may set a key to prove the loader carries it,
		// which says nothing about whether anything acts on it.
		// `log_level` looked read because a configuration test mentions
		// it, and nothing in the node has ever consulted it.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		switch filepath.Base(path) {
		case "keys.go", "shim.go":
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		body.Write(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	text := body.String()

	var unread []string
	for _, k := range Keys {
		mentioned := readSomewhere(text, k.Name)
		reason, recorded := waiverFor(k.Name)
		switch {
		case mentioned && recorded:
			t.Errorf("%q is read now, and is still recorded as unread (%q). Remove its row: "+
				"a stale entry hides the next setting that does nothing.", k.Name, reason)
		case !mentioned && !recorded:
			unread = append(unread, k.Name)
		}
	}
	sort.Strings(unread)
	for _, k := range unread {
		t.Errorf("%q is declared, documented, and read by nothing. Either act on it or "+
			"add it to InertKeys if a service should warn about it, or to UnreadKeys with the reason.", k)
	}

	for name := range allWaivers() {
		if _, ok := keyIndex[name]; !ok {
			t.Errorf("a waiver names %q, which is not a declared setting", name)
		}
	}
}

// readSomewhere reports whether a key is passed to a configuration
// accessor anywhere.
//
// Looking for the bare string was wrong twice. A key mentioned in a test
// looked read, and a test that proves the loader carries a key says
// nothing about whether anything acts on it. And a *module parameter* of
// the same name looked like a read of the setting: `hash_type` is
// declared on `file.managed` and is a configuration key, and neither was
// read by anything.
func readSomewhere(text, key string) bool {
	for _, accessor := range configAccessors() {
		if strings.Contains(text, fmt.Sprintf(".%s(%q", accessor, key)) {
			return true
		}
	}
	return false
}

// configAccessors are the methods on *Config that take a setting name,
// read out of the type rather than listed here.
//
// The list used to be written by hand, with a comment saying that adding
// an accessor to Config meant adding it here. That happened twice and
// both times the omission was found by this guard reporting a setting as
// unread: `Float` and `IsSet` arrived with `tracing_sample_rate`, and
// `PathUnderRoot` arrived with the `--root` fix and made `policy` look
// read by nothing (DIVERGENCE 5.140).
//
// Twice is enough. A hand-maintained list of a type's own methods is a
// second copy of the type, and the failure it produces is a *false*
// report from a guard people trust -- which is worse than a missed one,
// because the next person spends an afternoon on a setting that is read
// perfectly well. So this parses the methods instead: every method on
// `*Config` whose first parameter is a string is an accessor, and
// `Root()` and `Redacted()`, which take nothing, are not.
func configAccessors() []string {
	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		panic(fmt.Sprintf("reading this package's own accessors: %v", err))
	}
	var out []string
	for name, p := range pkg {
		if strings.HasSuffix(name, "_test") {
			continue
		}
		for _, file := range p.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
					continue
				}
				star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				ident, ok := star.X.(*ast.Ident)
				if !ok || ident.Name != "Config" {
					continue
				}
				if !firstParamIsString(fn.Type.Params) {
					continue
				}
				out = append(out, fn.Name.Name)
			}
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		panic("no accessor was read off *Config; this guard has stopped checking")
	}
	return out
}

func firstParamIsString(params *ast.FieldList) bool {
	if params == nil || len(params.List) == 0 {
		return false
	}
	ident, ok := params.List[0].Type.(*ast.Ident)
	return ok && ident.Name == "string"
}

// allWaivers is both maps, for the check that no waiver names a setting
// the table does not declare.
func allWaivers() map[string]string {
	out := make(map[string]string, len(InertKeys)+len(UnreadKeys))
	for k, v := range InertKeys {
		out[k] = v
	}
	for k, v := range UnreadKeys {
		out[k] = v
	}
	return out
}
