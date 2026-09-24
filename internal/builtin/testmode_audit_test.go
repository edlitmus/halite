package builtin

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/exec"
	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/value"
)

// Every mutating execution function is held to the claim its signature
// makes about `--test`.
//
// # Why this exists
//
// `exec.Context.Test` says, in the field's own comment, "a run that must
// change nothing. Every function that mutates the system checks it." That
// was a claim about 271 functions and nothing checked it, and
// `signature.TestReliable` is the **zero value** — so a module that never
// thought about test mode makes the strongest of the three promises by
// saying nothing. That is the opposite of the evidence model in
// internal/exec, where the zero value deliberately means "nobody looked".
//
// Twenty-one functions were violating it when this was written: every
// mutating function in the language-manager modules (`pip install`,
// `npm install --global`, `gem install`, `cargo install`, `go install`,
// `cpan -i`, `composer require`, `mvn`, `virtualenv <path>`), `pkg.purge`,
// `pkg.upgrade`, `pkg.hold`, `pkg.unhold`, `service.force_reload`, and the
// seven `saltutil.sync_*` functions, which declared that test mode did not
// apply to them and fetched extension bundles under a dry run. Seven
// scheduling functions had gone the same way a day earlier (DIVERGENCE
// 5.130). The pattern in every case: one function in the module checked,
// so the shared signature's claim was believed for its neighbours.
//
// # How it decides
//
// Two independent checks, and a function has to fail both to be reported.
//
//   - *Dynamically*: the function is called **once, with Test set**,
//     with synthesised arguments, against a recording runner and a
//     directory of its own. Whatever it did, it did during a dry run.
//   - *Statically*: the source is searched for a reference to the
//     context's Test field inside the registration, following the named
//     functions it calls.
//
// Neither alone is enough. A function that ran a command under a dry run
// may have been reading, which is how a prediction is made:
// `acl.wipe`, `at.atrm`, `ps.kill_pid`, `ps.pkill`, `reboot.cancel`,
// `firewall.disable` and `swap.off` all run one read and stop, and all
// seven are correct. The static check cannot see a mutation behind an
// interface it never reaches, and it cannot tell a `Test` reference that
// guards the mutation from one that only phrases a message. Together they
// name the ones that are wrong and nothing else.
//
// # Why there is no second, wet run
//
// The first version of this called each function again with Test unset, to
// prove the arguments had reached the mutating path — otherwise "the dry
// run did nothing" could mean "it never got there". That was wrong twice
// over.
//
// It was unsafe: a unit suite must not change the machine it runs on, and
// this one ran every mutating function for real. Most of them go through
// the recorder and could not execute, but the handful that write a fixed
// system path directly — `pam.set_module` on /etc/pam.d, `mount.set_fstab`
// on /etc/fstab, `system.set_system_date` on the clock — would have done
// it, as root, on whatever machine the suite was running on. All three
// turn out to honour `--test`, which is luck rather than design on the
// part of the audit.
//
// And it was unnecessary. **A dry run that did something is its own proof
// that the code was reached**, which is the only thing the wet run
// established. A function that did nothing under a dry run is not a
// violation whether it withheld the work or never arrived at it, so the
// verdict does not need to tell those apart — only the coverage number
// does, and that is reported as what it is.
//
// # What it does not cover
//
// The functions it cannot reach with synthesised arguments, which on any
// one machine is most of them: a module gated to another platform refuses
// before it runs anything. The count is reported so that a run on a
// different platform can be compared with this one, and the honest
// summary is that this audit is as broad as the machine it runs on.
func TestEveryMutatingFunctionHonoursItsTestModeClaim(t *testing.T) {
	r := New()
	checked := sourceChecksTest(t)

	var violations []string
	acted, quiet := 0, 0
	for _, name := range r.Exec.Signatures().Names() {
		sig, _ := r.Exec.Signatures().Lookup(name)
		if !sig.Mutates {
			continue
		}
		if sig.TestMode != signature.TestReliable {
			// Only the reliable claim promises that nothing changes.
			// `cmd.run` and its family declare unreliable and do run
			// under a dry run, which is Salt's behaviour and is what
			// `onlyif`, `unless` and `creates` are built on: a
			// conditional that did not run during a test would answer
			// for a machine nobody looked at. See the note in
			// DIVERGENCE about what `unreliable` is doing for two
			// different meanings.
			continue
		}
		dryCmds, dryFiles := callUnderAudit(t, r, name, sig, true)
		if len(dryCmds) == 0 && len(dryFiles) == 0 {
			// Either it withheld the work or it never reached it. Not a
			// violation either way.
			quiet++
			continue
		}
		acted++
		if checked[name] {
			// It consults Test, so what it did under the dry run is the
			// reading a prediction is made from.
			continue
		}
		violations = append(violations, name+" — dry run did: "+strings.Join(dryCmds, " | ")+
			filesSuffix(dryFiles))
	}

	for _, v := range violations {
		t.Errorf("%s\n\tIts signature says it honours `--test` and it did the same thing either "+
			"way. A dry run that acts is worse than one that fails: the operator has been told "+
			"nothing happened.", v)
	}
	if acted == 0 {
		t.Fatal("not one mutating function did anything under a dry run, which cannot be right: " +
			"this audit has stopped reaching the code")
	}
	// Both numbers, because the second is the audit's blind spot and
	// saying only the first would read as coverage. A run on another
	// platform moves functions from the second number to the first.
	t.Logf("on %s: %d of the %d mutating functions that claim reliable did something under a "+
		"dry run and were judged; %d did nothing, which is either correct or unreached",
		hostPlatform(), acted, acted+quiet, quiet)
}

// A mutating function may not say that test mode does not apply to it.
//
// The same rule internal/builtin has held state modules to since it was
// written, applied to execution modules, where nothing said it and the
// seven `saltutil.sync_*` functions took the gap.
func TestNoMutatingFunctionSaysTestModeDoesNotApply(t *testing.T) {
	r := New()
	for _, name := range r.Exec.Signatures().Names() {
		sig, _ := r.Exec.Signatures().Lookup(name)
		if sig.Mutates && sig.TestMode == signature.TestNotApplicable {
			t.Errorf("%s changes the system, so its test mode cannot be not_applicable: "+
				"either it honours `--test` (reliable), or it cannot predict what it would do "+
				"(unreliable), and both of those are answers", name)
		}
	}
}

func hostPlatform() string {
	if v := os.Getenv("GOOS"); v != "" {
		return v
	}
	return "this host"
}

// callUnderAudit runs one function and reports what it did.
//
// The temporary directory is substituted out of the recorded commands: the
// two runs get different ones, and a command carrying a path would
// otherwise never compare equal — which would make every path-taking
// function look as though it had withheld something. Found by reading a
// result that said exactly that.
func callUnderAudit(t *testing.T, r *Registries, name string, sig signature.Signature, test bool) ([]string, []string) { //nolint:unparam // test is always true now; kept named for what it means
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seed"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := auditSnapshot(dir)

	runner := &exec.RecordingRunner{Default: exec.Result{Code: 0}}
	c := newCtx(test)
	c.Runner = runner
	c.Ctx = context.Background()

	func() {
		// A function handed arguments it did not expect may panic, and a
		// panic here would say nothing about test mode.
		defer func() { _ = recover() }()
		_, _ = r.Exec.Call(c, name, auditArgs(sig, dir))
	}()

	ran := runner.RanCommands()
	for i := range ran {
		ran[i] = strings.ReplaceAll(ran[i], dir, "<dir>")
	}
	return ran, auditChanged(before, auditSnapshot(dir))
}

// auditArgs makes a plausible value for every required parameter, so that
// a call reaches the code rather than failing validation — which would
// look exactly like a function that changes nothing.
func auditArgs(sig signature.Signature, dir string) *value.Map {
	args := value.NewMap(len(sig.Params))
	for _, p := range sig.Params {
		if !p.Required {
			continue
		}
		args.Set(p.Name, auditValue(p, dir))
	}
	return args
}

func auditValue(p signature.Param, dir string) any {
	if len(p.Choices) > 0 {
		return p.Choices[0]
	}
	switch p.Type {
	case signature.Int:
		return int64(1)
	case signature.Float:
		return 1.0
	case signature.Bool:
		return false
	case signature.List:
		return []any{"halite-audit"}
	case signature.Map:
		return value.MapOf("halite-audit", "1")
	case signature.Mode:
		return "0644"
	case signature.Duration:
		return "1s"
	case signature.Path:
		return filepath.Join(dir, "target")
	}
	switch name := strings.ToLower(p.Name); {
	case name == "name", name == "source", name == "target",
		strings.Contains(name, "path"), strings.Contains(name, "file"),
		strings.Contains(name, "dir"):
		return filepath.Join(dir, "target")
	}
	return "halite-audit"
}

func auditSnapshot(dir string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			out[path] = "unreadable"
			return nil
		}
		out[path] = info.Mode().String() + ":" + string(rune(len(data)))
		return nil
	})
	return out
}

func auditChanged(before, after map[string]string) []string {
	var out []string
	for path, state := range after {
		if before[path] != state {
			out = append(out, filepath.Base(path))
		}
	}
	for path := range before {
		if _, ok := after[path]; !ok {
			out = append(out, "removed:"+filepath.Base(path))
		}
	}
	sort.Strings(out)
	return out
}

// sameStrings compares two recordings. swap_test.go already has an
// equalStrings, so this one is named for what it is asked here.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func filesSuffix(files []string) string {
	if len(files) == 0 {
		return ""
	}
	return " [wrote " + strings.Join(files, ", ") + "]"
}

// sourceChecksTest reports, per `module.function`, whether the source of
// its registration consults the context's Test field — following the
// package's own named functions, because the body is often one of those
// rather than a closure in the registration.
func sourceChecksTest(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("reading this package's source: %v", err)
	}
	named := map[string]*ast.FuncDecl{}
	var files []*ast.File
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			files = append(files, f)
			for _, decl := range f.Decls {
				if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil {
					named[fd.Name.Name] = fd
				}
			}
		}
	}

	out := map[string]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if sel, ok := lit.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Module" {
				return true
			}
			module, function := "", ""
			var fn ast.Expr
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				switch kv.Key.(*ast.Ident).Name {
				case "Sig":
					module, function = moduleAndFunction(kv.Value)
				case "Fn":
					fn = kv.Value
				}
			}
			if module == "" || function == "" || fn == nil {
				return true
			}
			if consultsTest(fn, named, 0) {
				out[module+"."+function] = true
			}
			return true
		})
	}
	if len(out) == 0 {
		t.Fatal("no registration was found to consult Test, which cannot be right: " +
			"this audit's source reader has stopped reading")
	}
	return out
}

// moduleAndFunction reads the `Module:` and `Function:` fields out of a
// signature literal. A signature built by a helper names only the
// function, and the helper supplies the module — those are reported as
// unknown and fall to the dynamic check alone.
func moduleAndFunction(sig ast.Expr) (string, string) {
	lit, ok := sig.(*ast.CompositeLit)
	if !ok {
		return "", ""
	}
	module, function := "", ""
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		bl, ok := kv.Value.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			continue
		}
		text := strings.Trim(bl.Value, `"`)
		switch kv.Key.(*ast.Ident).Name {
		case "Module":
			module = text
		case "Function":
			function = text
		}
	}
	return module, function
}

func consultsTest(e ast.Expr, named map[string]*ast.FuncDecl, depth int) bool {
	if e == nil || depth > 4 {
		return false
	}
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		switch t := n.(type) {
		case *ast.SelectorExpr:
			if t.Sel.Name == "Test" {
				found = true
				return false
			}
		case *ast.Ident:
			if fd, ok := named[t.Name]; ok && fd.Body != nil && consultsBody(fd, named, depth+1) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func consultsBody(fd *ast.FuncDecl, named map[string]*ast.FuncDecl, depth int) bool {
	if depth > 4 || fd.Body == nil {
		return false
	}
	found := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if found {
			return false
		}
		switch t := n.(type) {
		case *ast.SelectorExpr:
			if t.Sel.Name == "Test" {
				found = true
				return false
			}
		case *ast.Ident:
			if inner, ok := named[t.Name]; ok && inner != fd && inner.Body != nil &&
				consultsBody(inner, named, depth+1) {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
