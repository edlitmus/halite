package atomicfile

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/repotree"
)

// Nothing reads a file that this package replaces with a plain `os.ReadFile`
// or `os.Open`.
//
// # Why this exists
//
// On Windows `Rename` now replaces with POSIX semantics (DIVERGENCE 5.718), so
// a replace no longer fails against a reader that shares delete -- which is
// what `Read` asks for. A reader that does not share delete is still in the
// way: `os.ReadFile` and `os.Open` open with FILE_SHARE_READ|FILE_SHARE_WRITE
// and nothing else, and while one of them holds the file the replace falls
// back to MoveFileEx and its retry, and while the replace holds it their open
// fails. windows-2022 measured that as 7 of 4,000 plain reads refused during a
// replace, and 18 of 4,000 under -race (issue 838). `OrchStore.Get` was one
// such reader and was fixed by hand (DIVERGENCE 5.714); this finds the rest,
// and the next one.
//
// On unix `Read` is `os.ReadFile`, so moving a read to it changes nothing on
// any platform but Windows.
//
// # What "the same file" means here
//
// A file is identified by how its path is *made*, not by its value, which
// nothing static can know. Each path expression is traced back, inside its
// function, to an origin:
//
//   - a call to a path-building helper, by callee and by whatever arguments
//     are literals or constants: `m.path(nodeID)` is `<hub.MineStore>.path(*)`
//     wherever it is called, and `files.Path(pki.NodeCertFile)` names that
//     one file;
//   - a struct field: `s.path`, `r.OffsetFile`;
//   - a join or concatenation with at least one literal or constant in it:
//     `filepath.Join(dir, "job.json")`, `path + ".bak"`;
//   - a package-level constant or variable: `localtimePath`.
//
// A local variable is traced to every assignment of it in the function, and
// a parameter makes the function a wrapper: a function whose parameter
// reaches a write (`writeAtomic`) is itself a write of that parameter, and
// likewise for a read, to a fixed point. Keys are compared across the whole
// tree, not per package, so that a store written in one package and read in
// another is still a pair. A receiver whose type the audit could not work out
// is written `<?>` and matches any type with the same method or field name.
//
// # What it cannot see
//
//   - **A path that arrives with no origin.** A function that is handed a path
//     by its caller and reads it, where the caller is not in this tree or the
//     path came out of a map, a slice, a channel or a range variable, has
//     nothing to compare. That is most of internal/builtin: a state's `name` is
//     the operator's file, and its read and its write are the same expression
//     only when both are spelled from the same accessor call.
//   - **Two spellings of one file.** `filepath.Join(dir, "returns", name)` and
//     `filepath.Join(dir, "returns", returnShard(id), name)` are the old and
//     new layouts of one directory and do not match each other.
//   - **Types that only go/types would know.** A receiver reached through a
//     map, an interface, or a function's result is `<?>`, which errs towards
//     a false match rather than a missed one.
//   - **Writes inside an `os.Root`** (`WriteIn`, `WriteInFrom`): archive
//     extraction, whose files nothing in this tree reads back concurrently,
//     and whose names are relative to a root this does not model.
//   - **Shadowing.** A name redeclared in an inner scope is traced to every
//     assignment of that name in the function.
//   - **Opens inside the standard library.** `zip.OpenReader` and
//     `tls.LoadX509KeyPair` open their path with os.Open, and are not sinks
//     here. The two `zip.OpenReader` calls in internal/builtin read an
//     archive that `archive.extracted` may have renamed into place; their
//     path is a field of a value the audit cannot type, so they would not be
//     matched even if they were.
//   - **A field of a value whose type it cannot see** is no origin at all,
//     rather than `<?>`: `local.Path` and `f.Path` in internal/builtin are
//     unrelated, and treating them as one file reported four archive reads
//     against logrotate.
//
// It reads every .go file whatever its build constraint, so a Windows-only
// file is audited on FreeBSD.
//
// The positive half is the log line, and the floor on it: the reads that
// already go through `Read` are found by the same matching, so an audit that
// had stopped matching would fail rather than pass quietly. Issue 838,
// DIVERGENCE 5.721.

// sharedReadExemptions are plain reads of a replaced file that stay, with the
// reason for each. Keyed by file and function.
func sharedReadExemptions() map[string]string {
	return map[string]string{
		"internal/keystore/pending.go:pendingMarkers": "lists a directory, which " +
			"Read and Open do not open: they open files. The directory is replaced " +
			"by Rename when the pending index is rebuilt, and whether a listing " +
			"on Windows is in that rename's way has not been measured",
		"internal/job/signedreplay.go:append": "an append, not a read, and the " +
			"replace it shares the file with is rewrite, which append itself calls " +
			"with the same lock held, so within one SignedReplay the two cannot meet",
		"internal/pki/store.go:WriteKey": "an exclusive create that refuses an " +
			"existing file, not a read. It matches because every file in a " +
			"pki.Files is one origin to this audit: certificates are replaced, and " +
			"keys are created once",
	}
}

// readSinks and writeSinks are the calls that open a path, keyed by
// qualified callee, giving the index of the path argument.
var (
	plainReadSinks = map[string]int{
		"os.ReadFile":        0,
		"os.Open":            0,
		"os.OpenFile":        0,
		"io/ioutil.ReadFile": 0,
	}
	sharedReadSinks = map[string]int{
		atomicfileImport + ".Read": 0,
		atomicfileImport + ".Open": 0,
	}
	writeSinks = map[string]int{
		atomicfileImport + ".Write":         0,
		atomicfileImport + ".WritePrepared": 0,
		atomicfileImport + ".Rename":        1,
	}
)

const atomicfileImport = "github.com/edlitmus/halite/internal/atomicfile"

// unknown is an origin the audit could not work out.
const unknown = "*"

type sinkRole int

const (
	plainRead sinkRole = iota
	sharedRead
	write
)

// pathUse is one path expression handed to a read or a write.
type pathUse struct {
	role sinkRole
	keys []string
	pos  token.Position
	rel  string // file, relative to the repository
	pkg  string // import path
	fn   string // enclosing function, for exemptions
}

// auditPackage is one directory's non-test Go files.
type auditPackage struct {
	importPath string
	files      []*auditFile
	// globals are the package-level constants and variables.
	globals map[string]bool
	// funcs are the package's functions and methods, by qualified name.
	funcs map[string]*ast.FuncDecl
}

type auditFile struct {
	rel     string
	fset    *token.FileSet
	ast     *ast.File
	imports map[string]string // local name -> import path
}

// auditTree is everything the audit knows about the repository.
type auditTree struct {
	module   string
	packages map[string]*auditPackage // import path -> package
	// structs maps a qualified struct type to its fields' qualified types.
	structs map[string]map[string]string
	// wrappers maps a qualified function to the roles its parameters reach.
	wrappers map[string]map[int]sinkRole
}

func TestNothingReadsAReplacedFileWithoutSharingDelete(t *testing.T) {
	root := filepath.Join("..", "..")
	tree := loadAuditTree(t, root)
	tree.findWrappers()

	uses := tree.pathUses()
	var writes, plain, shared []pathUse
	for _, u := range uses {
		switch u.role {
		case write:
			writes = append(writes, u)
		case plainRead:
			plain = append(plain, u)
		case sharedRead:
			shared = append(shared, u)
		}
	}

	exempt := sharedReadExemptions()
	used := map[string]bool{}
	for _, r := range plain {
		w, key, ok := matchingWrite(r, writes)
		if !ok {
			continue
		}
		id := r.rel + ":" + r.fn
		if reason, ok := exempt[id]; ok {
			used[id] = true
			if reason == "" {
				t.Errorf("%s is exempted with no reason", id)
			}
			continue
		}
		t.Errorf("%s:%d %s reads %s with the standard library, and %s:%d replaces "+
			"the same file through atomicfile. On Windows os.ReadFile and os.Open do "+
			"not share delete, so a replace that meets this read is refused or waits, "+
			"and this read is refused while the replace holds the file (issue 838: 7 "+
			"of 4,000 reads on windows-2022). Use atomicfile.Read, which is "+
			"os.ReadFile on unix. If this read must not share delete, say why in "+
			"sharedReadExemptions.",
			r.rel, r.pos.Line, r.fn, key, w.rel, w.pos.Line)
	}
	for id := range exempt {
		if !used[id] {
			t.Errorf("%s is exempted, but no plain read of a replaced file is left there", id)
		}
	}

	matchedShared := 0
	for _, r := range shared {
		if _, _, ok := matchingWrite(r, writes); ok {
			matchedShared++
		}
	}
	// The positive half. Before issue 838 four reads through Read were
	// matched to their writes (OrchStore.Get among them), and after it 68
	// were, because every read this audit reported was moved to Read. An
	// audit that matched none would report nothing, exactly like a clean
	// tree.
	if matchedShared < 60 {
		t.Errorf("only %d reads through atomicfile were matched to a write; 68 "+
			"were when this audit was written, so it has stopped recognising "+
			"pairs and would report nothing either way", matchedShared)
	}
	if len(writes) < 80 {
		t.Errorf("only %d writes through atomicfile were found; this tree has over "+
			"eighty, so the walk or the sinks have stopped reaching them", len(writes))
	}
	t.Logf("%d writes, %d plain reads and %d shared reads traced; %d shared reads "+
		"match a write", len(writes), len(plain), len(shared), matchedShared)
}

// matchingWrite returns a write that names the same file as r.
func matchingWrite(r pathUse, writes []pathUse) (pathUse, string, bool) {
	for _, rk := range r.keys {
		for _, w := range writes {
			for _, wk := range w.keys {
				if sameOrigin(rk, wk, r.pkg == w.pkg) {
					return w, rk, true
				}
			}
		}
	}
	return pathUse{}, "", false
}

var typeSegment = regexp.MustCompile(`<[^>]*>`)

// sameOrigin compares two keys. Within one package an unknown receiver type
// matches any; across packages it matches nothing, or every field called
// `path` in the tree would be one file.
func sameOrigin(a, b string, samePackage bool) bool {
	if a == b {
		return true
	}
	if !samePackage || (!strings.Contains(a, "<?>") && !strings.Contains(b, "<?>")) {
		return false
	}
	return typeSegment.ReplaceAllString(a, "<?>") == typeSegment.ReplaceAllString(b, "<?>")
}

func readModulePath(t *testing.T, root string) string {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("go.mod names no module")
	return ""
}

func loadAuditTree(t *testing.T, root string) *auditTree {
	t.Helper()
	tree := &auditTree{
		module:   readModulePath(t, root),
		packages: map[string]*auditPackage{},
		structs:  map[string]map[string]string{},
		wrappers: map[string]map[int]sinkRole{},
	}
	err := repotree.Walk(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// This package is the implementation: Read is os.ReadFile on unix.
		if strings.HasPrefix(rel, "internal/atomicfile/") {
			return nil
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("%s: %v", path, parseErr)
		}
		importPath := tree.module
		if dir := filepath.ToSlash(filepath.Dir(rel)); dir != "." {
			importPath += "/" + dir
		}
		pkg := tree.packages[importPath]
		if pkg == nil {
			pkg = &auditPackage{importPath: importPath, globals: map[string]bool{}, funcs: map[string]*ast.FuncDecl{}}
			tree.packages[importPath] = pkg
		}
		af := &auditFile{rel: rel, fset: fset, ast: file, imports: map[string]string{}}
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			name := p[strings.LastIndex(p, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			af.imports[name] = p
		}
		pkg.files = append(pkg.files, af)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range tree.packages {
		for _, f := range pkg.files {
			tree.indexFile(pkg, f)
		}
	}
	return tree
}

// indexFile records a file's package-level names, functions and structs.
func (tree *auditTree) indexFile(pkg *auditPackage, f *auditFile) {
	for _, decl := range f.ast.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			pkg.funcs[tree.funcName(pkg, f, d)] = d
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						pkg.globals[n.Name] = true
					}
				case *ast.TypeSpec:
					st, ok := s.Type.(*ast.StructType)
					if !ok {
						continue
					}
					fields := map[string]string{}
					for _, fl := range st.Fields.List {
						typ := tree.typeName(pkg, f, fl.Type)
						for _, n := range fl.Names {
							fields[n.Name] = typ
						}
					}
					tree.structs[pkg.importPath+"."+s.Name.Name] = fields
				}
			}
		}
	}
}

// funcName is a function's qualified name: path.F or <path.T>.m.
func (tree *auditTree) funcName(pkg *auditPackage, f *auditFile, d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return pkg.importPath + "." + d.Name.Name
	}
	return "<" + tree.typeName(pkg, f, d.Recv.List[0].Type) + ">." + d.Name.Name
}

// typeName qualifies a type expression, ignoring pointers and type
// parameters, or gives "?".
func (tree *auditTree) typeName(pkg *auditPackage, f *auditFile, e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return tree.typeName(pkg, f, x.X)
	case *ast.IndexExpr:
		return tree.typeName(pkg, f, x.X)
	case *ast.IndexListExpr:
		return tree.typeName(pkg, f, x.X)
	case *ast.Ident:
		return pkg.importPath + "." + x.Name
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if p, ok := f.imports[id.Name]; ok {
				return p + "." + x.Sel.Name
			}
		}
	}
	return "?"
}

// funcScope is what tracing needs to know about the function an expression
// is in.
type funcScope struct {
	tree   *auditTree
	pkg    *auditPackage
	file   *auditFile
	decl   *ast.FuncDecl
	params map[string]int    // parameter name -> index
	types  map[string]string // receiver and parameter name -> qualified type
	// assigns maps a local name to the expressions assigned to it; a value
	// from a multi-value call carries its index.
	assigns map[string][]assigned
}

type assigned struct {
	expr  ast.Expr
	index int // -1 when the right-hand side is one value per name
}

func (tree *auditTree) scopeOf(pkg *auditPackage, f *auditFile, d *ast.FuncDecl) *funcScope {
	s := &funcScope{tree: tree, pkg: pkg, file: f, decl: d,
		params: map[string]int{}, types: map[string]string{}, assigns: map[string][]assigned{}}
	if d.Recv != nil {
		for _, fl := range d.Recv.List {
			for _, n := range fl.Names {
				s.types[n.Name] = tree.typeName(pkg, f, fl.Type)
			}
		}
	}
	i := 0
	for _, fl := range d.Type.Params.List {
		typ := tree.typeName(pkg, f, fl.Type)
		if len(fl.Names) == 0 {
			i++
			continue
		}
		for _, n := range fl.Names {
			s.params[n.Name] = i
			s.types[n.Name] = typ
			i++
		}
	}
	ast.Inspect(d.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for li, lhs := range x.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if len(x.Rhs) == len(x.Lhs) {
					s.assigns[id.Name] = append(s.assigns[id.Name], assigned{x.Rhs[li], -1})
				} else if len(x.Rhs) == 1 {
					s.assigns[id.Name] = append(s.assigns[id.Name], assigned{x.Rhs[0], li})
				}
			}
		case *ast.ValueSpec:
			for li, id := range x.Names {
				if x.Type != nil {
					s.types[id.Name] = tree.typeName(pkg, f, x.Type)
				}
				if len(x.Values) == len(x.Names) {
					s.assigns[id.Name] = append(s.assigns[id.Name], assigned{x.Values[li], -1})
				} else if len(x.Values) == 1 {
					s.assigns[id.Name] = append(s.assigns[id.Name], assigned{x.Values[0], li})
				}
			}
		}
		return true
	})
	return s
}

// typeOf works out an expression's qualified type where the AST says it.
func (s *funcScope) typeOf(e ast.Expr, depth int) string {
	if depth > 8 {
		return "?"
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.typeOf(x.X, depth+1)
	case *ast.StarExpr:
		return s.typeOf(x.X, depth+1)
	case *ast.UnaryExpr:
		return s.typeOf(x.X, depth+1)
	case *ast.CompositeLit:
		if x.Type != nil {
			return s.tree.typeName(s.pkg, s.file, x.Type)
		}
	case *ast.Ident:
		if t, ok := s.types[x.Name]; ok {
			return t
		}
		if as := s.assigns[x.Name]; len(as) == 1 && as[0].index == -1 {
			return s.typeOf(as[0].expr, depth+1)
		}
	case *ast.SelectorExpr:
		if t := s.typeOf(x.X, depth+1); t != "?" {
			if fields, ok := s.tree.structs[t]; ok {
				if ft, ok := fields[x.Sel.Name]; ok {
					return ft
				}
			}
		}
	}
	return "?"
}

// isImport reports whether e is a package name in this file.
func (s *funcScope) isImport(e ast.Expr) (string, bool) {
	id, ok := e.(*ast.Ident)
	if !ok {
		return "", false
	}
	if _, local := s.types[id.Name]; local {
		return "", false
	}
	if _, local := s.assigns[id.Name]; local {
		return "", false
	}
	p, ok := s.file.imports[id.Name]
	return p, ok
}

// callee qualifies a call's function, or gives "".
func (s *funcScope) callee(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if _, local := s.assigns[fn.Name]; local {
			return ""
		}
		return s.pkg.importPath + "." + fn.Name
	case *ast.SelectorExpr:
		if p, ok := s.isImport(fn.X); ok {
			return p + "." + fn.Sel.Name
		}
		return "<" + s.typeOf(fn.X, 0) + ">." + fn.Sel.Name
	}
	return ""
}

// inModule reports whether a qualified callee is declared in this tree, and
// so may be a path-building helper even with no literal arguments.
func (s *funcScope) inModule(callee string) bool {
	name := strings.TrimPrefix(callee, "<")
	return strings.HasPrefix(name, s.tree.module+"/") || strings.HasPrefix(name, s.tree.module+".") ||
		strings.HasPrefix(name, "?>")
}

// origins traces a path expression to the keys that identify it.
func (s *funcScope) origins(e ast.Expr, seen map[string]bool) []string {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.origins(x.X, seen)
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			v, _ := strconv.Unquote(x.Value)
			return []string{strconv.Quote(v)}
		}
	case *ast.Ident:
		if i, ok := s.params[x.Name]; ok {
			if _, reassigned := s.assigns[x.Name]; !reassigned {
				return []string{"param:" + strconv.Itoa(i)}
			}
		}
		if as, ok := s.assigns[x.Name]; ok {
			if seen[x.Name] {
				return nil
			}
			seen[x.Name] = true
			defer delete(seen, x.Name)
			var keys []string
			for _, a := range as {
				for _, k := range s.origins(a.expr, seen) {
					if a.index > 0 {
						k += "#" + strconv.Itoa(a.index)
					}
					keys = append(keys, k)
				}
			}
			return keys
		}
		if s.pkg.globals[x.Name] {
			return []string{s.pkg.importPath + "." + x.Name}
		}
	case *ast.SelectorExpr:
		if p, ok := s.isImport(x.X); ok {
			return []string{p + "." + x.Sel.Name}
		}
		// A field of a value whose type is not known is not an origin:
		// `local.Path` and `f.Path` in one package are any two things.
		// A method is different, because a package seldom has two path
		// helpers of one name, and where it does the match is reported.
		owner := s.typeOf(x.X, 0)
		if owner == "?" {
			return nil
		}
		return []string{"field:<" + owner + ">." + x.Sel.Name}
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return nil
		}
		l, r := s.single(x.X, seen), s.single(x.Y, seen)
		if l == unknown && r == unknown {
			return nil
		}
		return []string{"cat(" + l + "," + r + ")"}
	case *ast.CallExpr:
		callee := s.callee(x)
		if callee == "" || strings.HasPrefix(callee, operatorInput) {
			return nil
		}
		helper := s.inModule(callee)
		args := make([]string, len(x.Args))
		known := false
		for i, a := range x.Args {
			args[i] = s.single(a, seen)
			// A helper's arguments say which file only when they are fixed:
			// `m.path(nodeID)` and `m.path(data.NodeID)` are one store's
			// files, and `files.Path(pki.NodeCertFile)` is one file. A
			// transform's arguments are the path itself -- `filepath.Join(
			// s.dir, crlNumberName)` is named by both -- so they are kept.
			if helper && !fixed(args[i]) {
				args[i] = unknown
			}
			if args[i] != unknown {
				known = true
			}
		}
		if !known && !helper {
			return nil
		}
		return []string{"call:" + callee + "(" + strings.Join(args, ",") + ")"}
	}
	return nil
}

// operatorInput is the package whose accessors return a state's arguments.
// `states.Str(args, "name", "")` is whatever file the operator named, and
// two states that both spell it are not naming one file.
const operatorInput = "github.com/edlitmus/halite/internal/states."

// fixed reports whether a key is a literal or a package-level name, which
// is the same file wherever it is written.
func fixed(key string) bool {
	if strings.HasPrefix(key, `"`) {
		return true
	}
	return !strings.ContainsAny(key, ":()<>,*")
}

// single is an argument's one origin, or unknown. A parameter is unknown
// inside a key, because it names the caller's path rather than a file.
func (s *funcScope) single(e ast.Expr, seen map[string]bool) string {
	keys := s.origins(e, seen)
	if len(keys) != 1 || strings.HasPrefix(keys[0], "param:") {
		return unknown
	}
	return keys[0]
}

// sinkOf says whether a call reads or writes a path, and which argument.
func (s *funcScope) sinkOf(call *ast.CallExpr) (sinkRole, int, bool) {
	callee := s.callee(call)
	if callee == "" {
		return 0, 0, false
	}
	if i, ok := plainReadSinks[callee]; ok {
		return plainRead, i, true
	}
	if i, ok := sharedReadSinks[callee]; ok {
		return sharedRead, i, true
	}
	if i, ok := writeSinks[callee]; ok {
		return write, i, true
	}
	if roles, ok := s.tree.wrappers[callee]; ok {
		for i, role := range roles {
			return role, i, true
		}
	}
	return 0, 0, false
}

// eachFunc visits every function in the tree with its scope.
func (tree *auditTree) eachFunc(visit func(*funcScope)) {
	paths := make([]string, 0, len(tree.packages))
	for p := range tree.packages {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		pkg := tree.packages[p]
		for _, f := range pkg.files {
			for _, decl := range f.ast.Decls {
				d, ok := decl.(*ast.FuncDecl)
				if !ok || d.Body == nil {
					continue
				}
				visit(tree.scopeOf(pkg, f, d))
			}
		}
	}
}

// findWrappers marks every function whose parameter reaches a read or a
// write, until no more are found: writeAtomic, and whatever wraps it.
func (tree *auditTree) findWrappers() {
	for changed := true; changed; {
		changed = false
		tree.eachFunc(func(s *funcScope) {
			name := tree.funcName(s.pkg, s.file, s.decl)
			ast.Inspect(s.decl.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				role, i, ok := s.sinkOf(call)
				if !ok || i >= len(call.Args) {
					return true
				}
				for _, k := range s.origins(call.Args[i], map[string]bool{}) {
					p, ok := strings.CutPrefix(k, "param:")
					if !ok {
						continue
					}
					idx, _ := strconv.Atoi(p)
					if tree.wrappers[name] == nil {
						tree.wrappers[name] = map[int]sinkRole{}
					}
					if _, had := tree.wrappers[name][idx]; !had {
						tree.wrappers[name][idx] = role
						changed = true
					}
				}
				return true
			})
		})
	}
}

// pathUses is every read and write in the tree with the keys of its path.
// A use whose path is only a parameter is the wrapper's own body, and is
// accounted for at its callers.
func (tree *auditTree) pathUses() []pathUse {
	var uses []pathUse
	tree.eachFunc(func(s *funcScope) {
		ast.Inspect(s.decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			role, i, ok := s.sinkOf(call)
			if !ok || i >= len(call.Args) {
				return true
			}
			var keys []string
			for _, k := range s.origins(call.Args[i], map[string]bool{}) {
				if !strings.HasPrefix(k, "param:") {
					keys = append(keys, k)
				}
			}
			if len(keys) == 0 {
				return true
			}
			uses = append(uses, pathUse{
				role: role,
				keys: keys,
				pos:  s.file.fset.Position(call.Pos()),
				rel:  s.file.rel,
				pkg:  s.pkg.importPath,
				fn:   s.decl.Name.Name,
			})
			return true
		})
	})
	return uses
}
