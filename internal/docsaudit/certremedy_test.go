package docsaudit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/doctor"
)

// Every command a certificate remedy names is one the program it names
// answers to: the subcommand is a case of that binary's dispatch switch,
// and every further word and flag is a quoted word in its package.
//
// The remedy is the sentence somebody acts on when `doctor` warns, and
// issue 813 was a remedy that named real commands for the wrong
// certificate. The other half of that mistake -- a command spelled
// slightly wrong, or one since renamed -- would be as wrong and look as
// right, and nothing ran the sentence. The words are held as loosely as
// TestEverySubcommandAManualPageNamesExists holds a manual page, and for
// the same reason: a quoted word proves the program knows the word, not
// that the pairing is valid. That the command is the right one for the
// certificate is internal/doctor's TestEachCertificateKindHasItsOwnRemedy.
func TestEveryCommandACertificateRemedyNamesExists(t *testing.T) {
	root := repoRoot(t)
	span := regexp.MustCompile("`(halite-(?:node|hub|api)) ([^`]*)`")

	top := map[string]map[string]bool{}
	words := map[string]map[string]bool{}
	checked := 0
	for _, kind := range doctor.CertificateKinds() {
		remedy := doctor.CertificateRemedy(kind)
		for _, m := range span.FindAllStringSubmatch(remedy, -1) {
			binary, rest := m[1], strings.Fields(m[2])
			if top[binary] == nil {
				top[binary] = map[string]bool{}
				for _, sub := range subcommandsOf(t, root, binary) {
					top[binary][sub] = true
				}
				words[binary] = quotedWordsIn(t, filepath.Join(root, "cmd", binary))
			}
			checked++
			if len(rest) == 0 || !top[binary][rest[0]] {
				t.Errorf("certificate kind %d's remedy names %s, and %s's dispatch has no "+
					"such subcommand:\n%s", kind, m[0], binary, remedy)
				continue
			}
			for _, word := range rest[1:] {
				if strings.HasPrefix(word, "<") {
					continue // an operand the reader fills in
				}
				bare := strings.TrimLeft(word, "-")
				if !words[binary][bare] {
					t.Errorf("certificate kind %d's remedy names %s, and %q appears nowhere "+
						"in %s's package:\n%s", kind, m[0], bare, binary, remedy)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no command was read out of any certificate remedy; this audit has stopped checking")
	}
	t.Logf("checked %d command(s) named by certificate remedies", checked)
}

// Every certificate a program's doctor checks says what kind it is.
//
// The zero kind gets a remedy that says nobody recorded one, which is
// better than a remedy for some other certificate and is still a defect:
// it is what a certificate added to a doctor without a Kind would print.
// Read from the source, every `doctor.Expected{...}` literal in the three
// programs, because they build them in a dozen branches and a test that
// ran each branch would be a test of the fixtures.
func TestEveryCheckedCertificateHasAKind(t *testing.T) {
	root := repoRoot(t)
	found := 0
	for _, binary := range []string{"halite-node", "halite-hub", "halite-api"} {
		dir := filepath.Join(root, "cmd", binary)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			file, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Expected" {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "doctor" {
					return true
				}
				found++
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Kind" {
							return true
						}
					}
				}
				t.Errorf("%s: a doctor.Expected with no Kind, so its remedy says nobody "+
					"recorded who renews it", fset.Position(lit.Pos()))
				return true
			})
		}
	}
	if found == 0 {
		t.Fatal("no doctor.Expected literal was found in the programs; this audit has stopped checking")
	}
	t.Logf("checked %d doctor.Expected literal(s)", found)
}
