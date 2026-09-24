package docsaudit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/cli"
)

// Every flag in a command the reference calls "works" is a flag the
// binary takes.
//
// # Why this exists
//
// `docs/command-reference.md` is a table of Salt commands beside their
// halite equivalents, and the third column says `works`. That is a
// claim about a string a reader will paste into a shell. Two of them
// were false:
//
//   - `halite-hub migrate /srv/salt --cmd-default-shell`. The flag was
//     renamed to `--no-cmd-default-shell` when the `cmd.run` shell
//     default inverted (DIVERGENCE 5.81) and the row was never touched.
//   - `halite-hub runner reactor.test --tag … --data …`. A runner takes
//     its arguments as `key=value` pairs, never as flags; `--data` is
//     refused outright and `--tag` is accepted only because `event
//     listen` documents a flag by that name.
//
// Both fail immediately, with `--data is not a flag of
// `halite-hub runner“, which is the good case: the reader loses a
// minute rather than an afternoon. But the reference had been telling
// them to type it for months, and nothing noticed.
//
// # Why it can be checked at all
//
// `cli.RejectUnknownFlags` decides what a flag is by reading the usage
// text — a flag is accepted because it is described and described
// because it is accepted. That makes the accepted set a property of a
// string constant in the source, so this audit builds the same set the
// program builds and asks the document's flags against it.
//
// It therefore inherits the CLI's own coarseness: the hub judges a flag
// against its whole usage text plus the subcommand's, so `--tag` passes
// on `runner` because `event listen` documents it. This audit is no
// stricter than the program, deliberately — it measures the claim
// "this works", and that one does.
//
// DIVERGENCE 5.139.
func TestEveryWorkingCommandsFlagsAreRealFlags(t *testing.T) {
	root := repoRoot(t)
	ref := readDoc(t, filepath.Join(root, "docs", "command-reference.md"))

	accepted := map[string]map[string]bool{}
	for _, binary := range []string{"halite-node", "halite-hub", "halite-api"} {
		accepted[binary] = flagsAcceptedBy(t, root, binary)
	}

	var commands, flags int
	for lineNumber, line := range strings.Split(ref, "\n") {
		if !strings.Contains(line, "| works |") {
			continue
		}
		for _, invocation := range backtickedSpans(line) {
			binary, sub, given := invocationFlags(invocation)
			known, ours := accepted[binary]
			if !ours {
				continue
			}
			commands++
			for _, flag := range given {
				flags++
				if known[flag] || known[sub+" "+flag] {
					continue
				}
				t.Errorf("docs/command-reference.md:%d says `%s` works, and %s "+
					"refuses --%s: no usage text of that binary documents it, so "+
					"`cli.RejectUnknownFlags` stops the command before it starts. "+
					"A reader pasting this row gets an error, not a result.",
					lineNumber+1, invocation, binary, flag)
			}
		}
	}
	if commands == 0 || flags == 0 {
		t.Fatalf("read %d command(s) carrying %d flag(s) out of the reference's "+
			"`works` rows; this audit has stopped checking anything", commands, flags)
	}
	t.Logf("checked %d flag(s) across %d command(s) the reference calls `works`",
		flags, commands)
}

// flagsAcceptedBy builds the set of flag names a binary will accept,
// from the same usage strings the binary passes to the flag check.
//
// The set is keyed two ways: a bare name, for a flag the program's own
// usage documents and so accepts everywhere, and `<sub> <name>` for one
// that only a subcommand's usage carries. The caller asks both, because
// the hub's check is the union of the two and this has to match it
// rather than improve on it.
func flagsAcceptedBy(t *testing.T, root, binary string) map[string]bool {
	t.Helper()
	dir := filepath.Join(root, "cmd", binary)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("%s: %v", dir, err)
	}

	texts := map[string]string{}
	subUsage := map[string][]string{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for name, text := range usageStrings(file) {
			texts[name] = text
		}
		for sub, names := range subUsageMap(file) {
			subUsage[sub] = names
		}
	}

	program, ok := texts["usage"]
	if !ok {
		t.Fatalf("cmd/%s has no `usage` string, so this audit cannot tell what "+
			"flags it takes", binary)
	}
	accepted := map[string]bool{}
	for name := range cli.FlagNames(program) {
		accepted[name] = true
	}
	for sub, names := range subUsage {
		for _, name := range names {
			text, ok := texts[name]
			if !ok {
				t.Fatalf("cmd/%s's subUsage names %s, which is not a usage string "+
					"this audit found", binary, name)
			}
			for flag := range cli.FlagNames(text) {
				accepted[sub+" "+flag] = true
			}
		}
	}
	if len(accepted) == 0 {
		t.Fatalf("no flags were read out of cmd/%s's usage text", binary)
	}
	return accepted
}

// usageStrings returns the file's string-valued declarations whose name
// is `usage` or ends in `Usage`, with their literals concatenated.
func usageStrings(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.VAR && gen.Tok != token.CONST) {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) != 1 || len(valueSpec.Values) != 1 {
				continue
			}
			name := valueSpec.Names[0].Name
			if name != "usage" && !strings.HasSuffix(name, "Usage") {
				continue
			}
			if text, ok := concatenatedString(valueSpec.Values[0]); ok {
				out[name] = text
			}
		}
	}
	return out
}

// unreadable stands in for a term of a usage expression that is not a
// string literal, so that what surrounds it can still be read.
const unreadable = "\uFFFF"

// concatenatedString flattens a `"a" + "b"` chain into the usage text.
//
// Two of these expressions splice in a constant -- the node's usage
// writes `--pki-dir <dir>  key material, default ` + config.DefaultPKIDir
// -- and a constant's value is a path, not a flag name, because every
// flag in this tree is written out literally beside its description. So
// a term this function cannot evaluate becomes a marker and the rest is
// read around it.
//
// That would be an assumption rather than a fact if nothing checked it,
// so it is checked: a marker sitting immediately after a dash would be
// completing a flag name, and there the whole expression is declared
// unreadable instead. A usage text built that way would make this audit
// quietly accept fewer flags than the program does, which is the one
// failure worth refusing to guess at.
func concatenatedString(e ast.Expr) (string, bool) {
	joined, ok := spliced(e)
	if !ok {
		return "", false
	}
	if strings.Contains(joined, "-"+unreadable) {
		return "", false
	}
	return strings.ReplaceAll(joined, unreadable, ""), true
}

func spliced(e ast.Expr) (string, bool) {
	switch t := e.(type) {
	case *ast.BasicLit:
		if t.Kind != token.STRING {
			return unreadable, true
		}
		s, err := strconv.Unquote(t.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.BinaryExpr:
		if t.Op != token.ADD {
			return "", false
		}
		left, okLeft := spliced(t.X)
		right, okRight := spliced(t.Y)
		if !okLeft || !okRight {
			return "", false
		}
		return left + right, true
	}
	return unreadable, true
}

// subUsageMap reads a `subUsage` map literal: subcommand name to the
// usage variables that document its flags.
func subUsageMap(file *ast.File) map[string][]string {
	out := map[string][]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) != 1 ||
				valueSpec.Names[0].Name != "subUsage" || len(valueSpec.Values) != 1 {
				continue
			}
			literal, ok := valueSpec.Values[0].(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, element := range literal.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				sub, ok := concatenatedString(pair.Key)
				if !ok {
					continue
				}
				values, ok := pair.Value.(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, v := range values.Elts {
					if ident, ok := v.(*ast.Ident); ok {
						out[sub] = append(out[sub], ident.Name)
					}
				}
			}
		}
	}
	return out
}

var backticked = regexp.MustCompile("`([^`]+)`")

// backtickedSpans returns the code spans of one line of Markdown.
func backtickedSpans(line string) []string {
	var out []string
	for _, m := range backticked.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	return out
}

// invocationFlags splits a command span into the binary, the
// subcommand, and the long flags it passes.
//
// Only long flags are read. A single-letter flag in this reference is
// always `-h` or a Salt command's, and a bare `-` appears inside
// arguments often enough that reading them would cost more in false
// positives than it could find.
func invocationFlags(invocation string) (binary, sub string, flags []string) {
	fields := strings.Fields(invocation)
	if len(fields) == 0 {
		return "", "", nil
	}
	binary = fields[0]
	for _, field := range fields[1:] {
		if !strings.HasPrefix(field, "--") {
			if sub == "" {
				sub = field
			}
			continue
		}
		name, _, _ := strings.Cut(strings.TrimPrefix(field, "--"), "=")
		if name = strings.TrimRight(name, ".,:;"); name != "" {
			flags = append(flags, name)
		}
	}
	sort.Strings(flags)
	return binary, sub, flags
}
