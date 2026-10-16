package buildpolicy

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The record this test holds every shipped extension to. One line per
// extension: its command directory, the Version it was last recorded at,
// and the digest of its behaviour-bearing source at that version.
const extVersionRecord = "testdata/extension-versions.txt"

// Every shipped extension's Version moves when its behaviour does.
//
// Each extension says so in the comment on its `Version`: it is what the
// handshake reports and what a pin names, so it "changes when behaviour
// does". Nothing held that, and it did not happen. aws_secrets_manager
// changed behaviour twice at 1.0.0 -- the SPEC 24.3 resource limits and
// the not-secret setting -- and then a change to internal/awsauth, a
// package the KMS signer also links, changed what that signer does with
// a malformed region while its version stayed where it was (DIVERGENCE
// 5.324). Every one of those was a change somebody reviewed. The
// version was simply not a thing anybody was looking at, because it
// lives in a file the change did not touch.
//
// So the test does not ask whether behaviour changed, which it cannot
// know. It asks whether the code that could carry behaviour changed, and
// makes that a decision somebody has to take on purpose: bump Version,
// or say in the commit why this change cannot alter what the extension
// does, and update the digest. Either way the record moves in the same
// change, in a file a reviewer sees.
//
// What the digest covers, and why each choice:
//
//   - The extension's own package and every package of this module it
//     imports, transitively. The KMS signer's behaviour moved through
//     internal/awsauth; a digest of cmd/halite-ext-signer-kms alone
//     would have passed.
//   - Every .go file that is not a test, on every platform. Imports are
//     followed by parsing, not by `go list`, which answers for the
//     platform running the test: an extension's FreeBSD behaviour lives
//     in files a Linux CI leg does not build, and FreeBSD is most of the
//     estate.
//   - Comments are dropped, so a change that only rewords a comment does
//     not demand a decision -- this codebase's comments are long and
//     edited often, and a check that fires on every one of those edits
//     gets its digest updated by reflex, which is a check nobody reads.
//     Build constraints and other `//go:` directives are comments to the
//     parser but decide what is compiled, so they are kept.
//   - The digest is of the token stream, so whitespace and gofmt churn
//     do not count either. See canonicalSource for why it is not the
//     parsed file printed back.
//   - A dependency outside this module is recorded by its module version
//     from vendor/modules.txt. The standard library and the toolchain
//     are not covered: a Go upgrade can change an extension's behaviour
//     and this test will not notice. That is a known hole, not an
//     oversight.
func TestEveryShippedExtensionVersionMovesWithItsCode(t *testing.T) {
	root := repoRoot(t)
	commands, err := filepath.Glob(filepath.Join(root, "cmd", "halite-ext-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) == 0 {
		t.Fatal("no extensions were found; this check has stopped checking")
	}
	record := readExtVersionRecord(t)

	seen := map[string]bool{}
	for _, dir := range commands {
		name := filepath.Base(dir)
		seen[name] = true
		version := extensionVersion(t, dir)
		digest := extensionDigest(t, root, dir)
		line := fmt.Sprintf("%s %s %s", name, version, digest)

		was, ok := record[name]
		switch {
		case !ok:
			t.Errorf("%s is not in internal/buildpolicy/%s. A new extension starts its record here; add:\n\t%s",
				name, extVersionRecord, line)
		case was.version != version:
			if !versionIncreases(was.version, version) {
				t.Errorf("%s went from %s to %s. A pin names the version, so it only ever "+
					"increases: an older number is a different extension to a node that has "+
					"seen the newer one", name, was.version, version)
				continue
			}
			t.Errorf("%s is now %s; the record says %s. Record the bump in internal/buildpolicy/%s:\n\t%s",
				name, version, was.version, extVersionRecord, line)
		case was.digest != digest:
			t.Errorf("%s changed, but its Version is still %s.\n"+
				"Its source, or a package it imports, is not what it was when %s was recorded. "+
				"Either:\n"+
				"  - the change can alter what the extension does: bump Version in %s/main.go "+
				"(a pin names it), then record the new line; or\n"+
				"  - it cannot (a rename, a refactor with no observable effect): keep Version, "+
				"record the new digest, and say in the commit why the behaviour is the same.\n"+
				"The line to record in internal/buildpolicy/%s is:\n\t%s",
				name, version, version, filepath.Join("cmd", name), extVersionRecord, line)
		}
	}
	for name := range record {
		if !seen[name] {
			t.Errorf("%s names %s, which is not in cmd/ any more; remove its line",
				extVersionRecord, name)
		}
	}
	t.Logf("checked %d extension(s)", len(commands))
}

type extVersionEntry struct{ version, digest string }

func readExtVersionRecord(t *testing.T) map[string]extVersionEntry {
	t.Helper()
	f, err := os.Open(extVersionRecord)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]extVersionEntry{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("%s:%d: want `name version digest`, got %q", extVersionRecord, n, line)
		}
		if _, dup := out[fields[0]]; dup {
			t.Fatalf("%s:%d: %s is recorded twice", extVersionRecord, n, fields[0])
		}
		out[fields[0]] = extVersionEntry{fields[1], fields[2]}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// extensionVersion reads the `Version` constant from the extension's
// package, parsed rather than searched for the same reason as
// callsConfineFromMain: a commented-out line contains the same text.
func extensionVersion(t *testing.T, dir string) string {
	t.Helper()
	fset := token.NewFileSet()
	var found []string
	for _, path := range goSources(t, dir) {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, ident := range vs.Names {
					if ident.Name != "Version" || i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						t.Fatalf("%s: Version is not a string literal, so this test cannot read it", path)
					}
					v, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatal(err)
					}
					found = append(found, v)
				}
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: want one `const Version`, found %d", dir, len(found))
	}
	return found[0]
}

// extensionDigest is the digest described on the test.
func extensionDigest(t *testing.T, root, dir string) string {
	t.Helper()
	const module = "github.com/edlitmus/halite"
	vendored := vendoredModules(t, root)

	h := sha256.New()
	visited := map[string]bool{}
	external := map[string]bool{}
	queue := []string{dir}
	var order []string
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if visited[d] {
			continue
		}
		visited[d] = true
		order = append(order, d)
		for _, path := range goSources(t, d) {
			for _, imp := range importsOf(t, path) {
				switch {
				case imp == module || strings.HasPrefix(imp, module+"/"):
					queue = append(queue, filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(imp, module))))
				case strings.Contains(strings.SplitN(imp, "/", 2)[0], "."):
					// Outside the standard library and outside this module.
					external[imp] = true
				}
			}
		}
	}

	sort.Strings(order)
	for _, d := range order {
		rel, err := filepath.Rel(root, d)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range goSources(t, d) {
			fmt.Fprintf(h, "file %s/%s\n", filepath.ToSlash(rel), filepath.Base(path))
			h.Write(canonicalSource(t, path))
		}
	}
	var mods []string
	for imp := range external {
		mod, ok := moduleOf(imp, vendored)
		if !ok {
			t.Fatalf("%s imports %s, which is not in vendor/modules.txt", dir, imp)
		}
		mods = append(mods, mod)
	}
	sort.Strings(mods)
	for _, m := range dedupe(mods) {
		fmt.Fprintf(h, "module %s\n", m)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// goSources lists a package directory's non-test Go files, every
// platform's, sorted. Not recursive: a subdirectory is another package,
// reached only if something imports it.
func goSources(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatalf("%s has no Go source", dir)
	}
	return out
}

func importsOf(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, spec := range file.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

// canonicalSource is the file's token stream: every token the compiler
// reads, and nothing it does not, plus its `//go:` directives, which are
// comments to the scanner and instructions to the compiler.
//
// Tokens rather than the parsed file printed back. The first version of
// this printed the AST with its comment list cleared. A comment inserted
// above a function still changed the digest, because doc comments hang
// off the declarations themselves and the printer prints them. A
// commented-out `const Version` line did the same. Either would have
// made the test fire on prose edits, and a check that fires on prose
// gets updated by reflex. The scanner has no such attachments. Whitespace
// and comments are simply not tokens, and a string literal, raw or not,
// is one token whose text is kept exactly.
//
// An automatically inserted semicolon is hashed as the same token as a
// written one. The scanner reports the first with the literal "\n" and
// the second with ";", and the compiler does not tell them apart.
func canonicalSource(t *testing.T, path string) []byte {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file := fset.AddFile(path, fset.Base(), len(src))
	var s scanner.Scanner
	var scanErr error
	s.Init(file, src, func(pos token.Position, msg string) {
		if scanErr == nil {
			scanErr = fmt.Errorf("%s: %s", pos, msg)
		}
	}, scanner.ScanComments)
	var buf bytes.Buffer
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		switch tok {
		case token.COMMENT:
			if strings.HasPrefix(lit, "//go:") {
				buf.WriteString("directive " + strings.TrimRight(lit, "\r") + "\n")
			}
			continue
		case token.SEMICOLON:
			lit = ""
		}
		fmt.Fprintf(&buf, "%d %q\n", tok, lit)
	}
	if scanErr != nil {
		t.Fatal(scanErr)
	}
	return buf.Bytes()
}

// vendoredModules reads `# module version` lines from vendor/modules.txt.
func vendoredModules(t *testing.T, root string) map[string]string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "vendor", "modules.txt"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "#" {
			out[fields[1]] = fields[2]
		}
	}
	return out
}

// moduleOf finds the vendored module an import path belongs to, by the
// longest module path that prefixes it.
func moduleOf(imp string, vendored map[string]string) (string, bool) {
	best := ""
	for mod := range vendored {
		if (imp == mod || strings.HasPrefix(imp, mod+"/")) && len(mod) > len(best) {
			best = mod
		}
	}
	if best == "" {
		return "", false
	}
	return best + " " + vendored[best], true
}

func dedupe(sorted []string) []string {
	var out []string
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// versionIncreases compares two `major.minor.patch` versions.
func versionIncreases(from, to string) bool {
	a, okA := semverTriple(from)
	b, okB := semverTriple(to)
	if !okA || !okB {
		return false
	}
	for i := range a {
		if b[i] != a[i] {
			return b[i] > a[i]
		}
	}
	return false
}

func semverTriple(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p != strconv.Itoa(n) {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
