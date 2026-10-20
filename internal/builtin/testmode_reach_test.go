package builtin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/signature"
)

// Every mutating execution function's body reaches the context's Test
// field, on every platform, whatever its signature claims.
//
// # Why the dynamic audit was not enough
//
// TestEveryMutatingFunctionHonoursItsTestModeClaim calls each function
// under a dry run and watches what it did, and it let three families
// through that acted under `--test`:
//
//   - `service.mask` and `service.unmask` masked units for real. On the
//     machine the audit ran on there was no masking provider, so both
//     refused before acting and landed in the bucket it cannot judge.
//   - `file.chmod`, `mkdir`, `makedirs`, `rmdir`, `move`, `hardlink`,
//     `truncate`, `chown` and `chgrp` all acted. The audit handed them a
//     path that named nothing, so each failed on the missing file before
//     the change and read as "withheld".
//   - `pro.attach`/`detach`/`enable`/`disable`, the `dnf_module`
//     mutators and `dnsutil.hosts_append`/`hosts_remove` acted too, and
//     the dynamic audit skips every function that does not claim
//     reliable, by design. Those are also gated to Linux, so on any
//     other machine it could not have reached them anyway.
//
// And the static half it already had could not help, because it found a
// registration's name by reading `Module:` and `Function:` string
// literals out of the source, and a registration built by a helper --
// `maskModule(r, "mask", ...)`, dnf_module's `mutating(...)`, the
// service module's `simple(...)` -- has a variable where the function's
// name goes, so it was never in the map at all.
//
// # How this one finds the code
//
// From the registry rather than the source. Each mutating function's Fn
// is looked up by name, and the runtime says which file and line its code
// starts on -- a closure inside a helper included, which is exactly the
// case the literal-reading approach lost. That line is found in this
// package's parsed source, and the body is searched for a `.Test`
// reference, following the package's own named functions the way the
// dynamic audit's static half already does.
//
// It is platform-independent, which is the point: `pro.detach` is
// checked on a Mac as surely as on Ubuntu.
//
// # What it cannot see
//
// A reference to `.Test` is not proof the reference guards the mutation;
// a function that reads the flag to phrase a message and then acts would
// pass here. That is what the dynamic audit is for, and the two together
// are the claim. It also follows only package-level functions, not
// methods on a provider, so a check that lives only inside a provider's
// method is not seen -- such a function goes on the list below with the
// reason, rather than the walk growing until it proves nothing.
func TestEveryMutatingFunctionReachesTheTestFlag(t *testing.T) {
	r := New()
	idx := indexFunctionSource(t)

	var missing, unlocated []string
	seenAllowed := map[string]bool{}
	reached := 0
	for _, name := range r.Exec.Signatures().Names() {
		sig, _ := r.Exec.Signatures().Lookup(name)
		if !sig.Mutates {
			continue
		}
		fn, ok := r.Exec.Func(name)
		if !ok {
			t.Errorf("%s has a signature and no function", name)
			continue
		}
		body, where := idx.locate(reflect.ValueOf(fn).Pointer())
		if _, allowed := actsUnderTestByDesign[name]; allowed {
			seenAllowed[name] = true
			if sig.TestMode != signature.TestUnreliable {
				t.Errorf("%s acts under a dry run by design and its signature claims %s; only "+
					"`unreliable` may, or an operator reading the claim is told nothing happens",
					name, sig.TestMode)
			}
			if body != nil && (consultsTest(body, idx.named, 0) || idx.delegatesReach(name)) {
				t.Errorf("%s is on the list of functions that act under a dry run by design, "+
					"and its code now reads Test; take it off the list so a regression is caught", name)
			}
			continue
		}
		if body == nil {
			unlocated = append(unlocated, name+" ("+where+")")
			continue
		}
		if consultsTest(body, idx.named, 0) || idx.delegatesReach(name) {
			reached++
			continue
		}
		missing = append(missing, name+" — "+where)
	}

	for _, m := range missing {
		t.Errorf("%s\n\tchanges the system and nothing it runs reads the context's Test field, so a "+
			"dry run of it acts for real. Give it a test branch; or, if acting under `--test` is "+
			"the design (cmd.run's family), add it to actsUnderTestByDesign with the reason.", m)
	}
	for _, u := range unlocated {
		t.Errorf("%s: its code could not be found in this package's source, so this audit "+
			"cannot say whether it reads Test", u)
	}
	for name := range actsUnderTestByDesign {
		if !seenAllowed[name] {
			t.Errorf("actsUnderTestByDesign names %s, which is not a mutating function here; "+
				"a stale entry is an exemption waiting for something new to take its name", name)
		}
	}
	if reached == 0 {
		t.Fatal("no mutating function was found to read Test, which cannot be right: " +
			"this audit has stopped reading the source")
	}
	t.Logf("%d mutating functions reach Test; %d act under a dry run by design",
		reached, len(actsUnderTestByDesign))
}

// actsUnderTestByDesign is the whole list of mutating functions whose code
// never reads Test, each with the reason. It is a closed list: a new
// entry is a decision somebody has to write down here, which is the
// point, and an entry that stops being true fails the test above.
var actsUnderTestByDesign = map[string]string{
	"cmd.exec_code":      cmdRunsUnderTest,
	"cmd.retcode":        cmdRunsUnderTest,
	"cmd.run":            cmdRunsUnderTest,
	"cmd.run_all":        cmdRunsUnderTest,
	"cmd.run_bg":         cmdRunsUnderTest,
	"cmd.run_stderr":     cmdRunsUnderTest,
	"cmd.run_stdout":     cmdRunsUnderTest,
	"cmd.script":         cmdRunsUnderTest,
	"cmd.script_retcode": cmdRunsUnderTest,
	"cmd.shell":          cmdRunsUnderTest,
}

const cmdRunsUnderTest = "runs its command under a dry run, as Salt does: `onlyif`, `unless` " +
	"and `creates` are built on it, and a conditional that did not run during a test would " +
	"answer for a machine nobody looked at. Declared TestUnreliable, which is the second " +
	"meaning DIVERGENCE records that word carrying."

// funcSourceIndex maps where a function's code starts to its AST.
type funcSourceIndex struct {
	at    map[string]map[int][]ast.Node // file base name -> line -> FuncDecl/FuncLit
	named map[string]*ast.FuncDecl
	fset  *token.FileSet
	// delegates holds, per `module.function`, the functions a wrapper
	// registration hands its work to. See indexDelegates.
	delegates map[string][]ast.Expr
}

// delegatesReach reports whether a registration whose own body is a
// wrapper hands its work only to functions that read Test.
func (idx funcSourceIndex) delegatesReach(name string) bool {
	ds := idx.delegates[name]
	if len(ds) == 0 {
		return false
	}
	for _, d := range ds {
		if !consultsTest(d, idx.named, 0) {
			return false
		}
	}
	return true
}

func indexFunctionSource(t *testing.T) funcSourceIndex {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("reading this package's source: %v", err)
	}
	idx := funcSourceIndex{
		at:        map[string]map[int][]ast.Node{},
		named:     map[string]*ast.FuncDecl{},
		fset:      fset,
		delegates: map[string][]ast.Expr{},
	}
	add := func(n ast.Node) {
		p := fset.Position(n.Pos())
		base := filepath.Base(p.Filename)
		if idx.at[base] == nil {
			idx.at[base] = map[int][]ast.Node{}
		}
		idx.at[base][p.Line] = append(idx.at[base][p.Line], n)
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				switch d := n.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil {
						idx.named[d.Name.Name] = d
					}
					add(d)
				case *ast.FuncLit:
					add(d)
				}
				return true
			})
		}
	}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			idx.indexDelegates(f)
		}
	}
	return idx
}

// indexDelegates finds what a wrapper registration hands its work to.
//
// Two shapes are in this package, and a closure that calls a function
// it was given cannot be followed by reading the closure alone:
//
//   - a decorator in the registration itself, `Fn: withBeacons(func…)`,
//     where the Module and Function are literals and the work is the
//     argument; and
//   - a helper that builds the whole registration, `maskModule(r,
//     "mask", …, func…)` or file's `edit("touch", …, fileTouchExec)`, where
//     the Function is one of the helper's parameters and the work is
//     another. The helper's call sites name the function as a string
//     literal, which is how the two are paired.
//
// A delegate that is a method expression (`serviceProvider.Start`) is
// not followed; a registration whose only delegates are those falls back
// to its own body.
func (idx funcSourceIndex) indexDelegates(f *ast.File) {
	type helper struct {
		module     string
		fnIdx      int
		delegateIx []int
	}
	helpers := map[string]helper{}
	sigHelpers := signatureHelpers(f)

	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if sel, ok := lit.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Module" {
			return true
		}
		var sigExpr, fnExpr ast.Expr
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				continue
			}
			switch key.Name {
			case "Sig":
				sigExpr = kv.Value
			case "Fn":
				fnExpr = kv.Value
			}
		}
		if sigExpr == nil || fnExpr == nil {
			return true
		}
		module, function := moduleAndFunction(sigExpr)
		if module == "" {
			module, function = sigHelpers.resolve(sigExpr)
		}
		if module == "" {
			return true
		}
		if function != "" {
			if call, ok := fnExpr.(*ast.CallExpr); ok {
				idx.delegates[module+"."+function] = append(idx.delegates[module+"."+function],
					idx.funcArgs(call.Args)...)
			}
			return true
		}
		// The helper shape: find the enclosing function and its name.
		param := functionParamIdent(sigExpr)
		if param == "" {
			return true
		}
		var ftype *ast.FuncType
		name := ""
		for i := len(stack) - 2; i >= 0 && ftype == nil; i-- {
			switch e := stack[i].(type) {
			case *ast.FuncDecl:
				ftype, name = e.Type, e.Name.Name
			case *ast.FuncLit:
				ftype = e.Type
				if i > 0 {
					if as, ok := stack[i-1].(*ast.AssignStmt); ok && len(as.Lhs) == 1 {
						if id, ok := as.Lhs[0].(*ast.Ident); ok {
							name = id.Name
						}
					}
				}
			}
		}
		if ftype == nil || name == "" {
			return true
		}
		params := paramNames(ftype)
		h := helper{module: module, fnIdx: -1}
		for i, p := range params {
			if p == param {
				h.fnIdx = i
			}
		}
		ast.Inspect(fnExpr, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				for i, p := range params {
					if p == id.Name {
						h.delegateIx = append(h.delegateIx, i)
					}
				}
			}
			return true
		})
		if h.fnIdx >= 0 && len(h.delegateIx) > 0 {
			helpers[name] = h
		}
		return true
	})

	if len(helpers) == 0 {
		return
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		h, ok := helpers[id.Name]
		if !ok || h.fnIdx >= len(call.Args) {
			return true
		}
		bl, ok := call.Args[h.fnIdx].(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		name := h.module + "." + strings.Trim(bl.Value, `"`)
		for _, i := range h.delegateIx {
			if i < len(call.Args) {
				idx.delegates[name] = append(idx.delegates[name], idx.funcArgs(call.Args[i:i+1])...)
			}
		}
		return true
	})
}

// sigHelperTable maps a function that builds a signature -- beacons'
// and schedule's `sig("add", …)` -- to the module it fills in and which
// of its parameters is the function's name.
type sigHelperTable map[string]struct {
	module string
	param  int
}

func signatureHelpers(f *ast.File) sigHelperTable {
	out := sigHelperTable{}
	record := func(name string, ft *ast.FuncType, body ast.Node) {
		ast.Inspect(body, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if sel, ok := lit.Type.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Signature" {
				return true
			}
			module, _ := moduleAndFunction(lit)
			param := functionParamIdent(lit)
			if module == "" || param == "" {
				return true
			}
			for i, p := range paramNames(ft) {
				if p == param {
					if prev, dup := out[name]; dup && prev.module != module {
						// Two helpers of one name in one file filling
						// different modules: ambiguous, so neither is
						// used and those registrations fall back to
						// their own bodies.
						out[name] = struct {
							module string
							param  int
						}{"", -1}
						return false
					}
					out[name] = struct {
						module string
						param  int
					}{module, i}
				}
			}
			return true
		})
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				record(d.Name.Name, d.Type, d.Body)
			}
		case *ast.AssignStmt:
			if len(d.Lhs) == 1 && len(d.Rhs) == 1 {
				id, ok1 := d.Lhs[0].(*ast.Ident)
				fl, ok2 := d.Rhs[0].(*ast.FuncLit)
				if ok1 && ok2 {
					record(id.Name, fl.Type, fl.Body)
				}
			}
		}
		return true
	})
	return out
}

// resolve reads `sig("add", …)`, or the same wrapped in another call
// such as schedule's `arbitrary(sig(…))`.
func (t sigHelperTable) resolve(e ast.Expr) (string, string) {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return "", ""
	}
	if id, ok := call.Fun.(*ast.Ident); ok {
		if h, ok := t[id.Name]; ok && h.module != "" && h.param < len(call.Args) {
			if bl, ok := call.Args[h.param].(*ast.BasicLit); ok && bl.Kind == token.STRING {
				return h.module, strings.Trim(bl.Value, `"`)
			}
		}
	}
	for _, a := range call.Args {
		if m, f := t.resolve(a); m != "" {
			return m, f
		}
	}
	return "", ""
}

// funcArgs keeps the arguments that are code this check can read: a
// function literal, or the name of one of this package's functions.
func (idx funcSourceIndex) funcArgs(args []ast.Expr) []ast.Expr {
	var out []ast.Expr
	for _, a := range args {
		switch e := a.(type) {
		case *ast.FuncLit:
			out = append(out, e)
		case *ast.Ident:
			if _, ok := idx.named[e.Name]; ok {
				out = append(out, e)
			}
		}
	}
	return out
}

// functionParamIdent returns the identifier a signature literal's
// `Function:` is set from, when it is one rather than a string.
func functionParamIdent(sig ast.Expr) string {
	lit, ok := sig.(*ast.CompositeLit)
	if !ok {
		return ""
	}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Function" {
			if id, ok := kv.Value.(*ast.Ident); ok {
				return id.Name
			}
		}
	}
	return ""
}

func paramNames(ft *ast.FuncType) []string {
	var out []string
	if ft.Params == nil {
		return out
	}
	for _, field := range ft.Params.List {
		if len(field.Names) == 0 {
			out = append(out, "")
			continue
		}
		for _, n := range field.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// locate finds the code a function value runs. The runtime reports the
// file and line of the function's entry, which for a declaration and a
// literal alike is the line its `func` keyword is on.
//
// File names are compared by base name: under -trimpath the runtime
// reports a module path rather than this directory, and every file in
// this package has a distinct base name anyway.
func (idx funcSourceIndex) locate(pc uintptr) (ast.Expr, string) {
	f := runtime.FuncForPC(pc)
	if f == nil {
		return nil, "no runtime record"
	}
	file, line := f.FileLine(f.Entry())
	where := filepath.Base(file) + ":" + strconv.Itoa(line) + " " + f.Name()
	nodes := idx.at[filepath.Base(file)][line]
	if len(nodes) == 0 {
		return nil, where
	}
	// More than one function starting on a line is possible in
	// principle; reading all of them over-approximates, which is the
	// direction this check can afford -- the dynamic audit is the
	// under-approximation.
	if len(nodes) == 1 {
		return funcBody(nodes[0]), where
	}
	var bodies []ast.Stmt
	for _, n := range nodes {
		if b := funcBody(n); b != nil {
			bodies = append(bodies, &ast.ExprStmt{X: b})
		}
	}
	return &ast.FuncLit{Type: &ast.FuncType{}, Body: &ast.BlockStmt{List: bodies}}, where
}

func funcBody(n ast.Node) ast.Expr {
	switch d := n.(type) {
	case *ast.FuncLit:
		return d
	case *ast.FuncDecl:
		if d.Body == nil {
			return nil
		}
		return &ast.FuncLit{Type: d.Type, Body: d.Body}
	}
	return nil
}
