package pillar

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

// fakeExt is one external pillar source, for the compiler's side of
// SPEC 12.7.
type fakeExt struct {
	name string
	// give is what it contributes.
	give *value.Map
	// fail is what it fails with.
	fail error
	soft bool
	// saw records the pillar it was handed.
	saw *value.Map
}

func (f *fakeExt) Name() string   { return f.name }
func (f *fakeExt) FailSoft() bool { return f.soft }

func (f *fakeExt) Pillar(_ context.Context, req ExtRequest) (*value.Map, error) {
	f.saw = req.Pillar
	if f.fail != nil {
		return nil, f.fail
	}
	return f.give, nil
}

// An external source is merged over the tree, so a value it supplies
// wins where the two collide -- which is what a source consulted for a
// live secret has to do.
func TestAnExternalSourceMergesOverTheTree(t *testing.T) {
	tree := map[string]string{
		"base|top": "base:\n  '*':\n    - db\n",
		"base|db":  "db:\n  host: db.example\n  password: placeholder\n",
	}
	src := &fakeExt{name: "fake", give: value.MapOf("db", value.MapOf("password", "real"))}
	out := compileWithExt(t, tree, src)
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	if got, _ := value.Traverse(out.Pillar, "db:password", ":"); got != "real" {
		t.Errorf("the password is %v", got)
	}
	// And the tree's own keys survive the merge.
	if got, _ := value.Traverse(out.Pillar, "db:host", ":"); got != "db.example" {
		t.Errorf("the host is %v", got)
	}
	if len(out.Ext) != 1 || out.Ext[0] != "fake" {
		t.Errorf("the contributing sources are %v", out.Ext)
	}
}

// A source sees what the tree and the sources before it produced.
func TestASourceSeesThePillarSoFar(t *testing.T) {
	tree := map[string]string{
		"base|top": "base:\n  '*':\n    - db\n",
		"base|db":  "db:\n  host: db.example\n",
	}
	first := &fakeExt{name: "first", give: value.MapOf("extra", "one")}
	second := &fakeExt{name: "second"}
	compileWithExt(t, tree, first, second)

	if second.saw == nil {
		t.Fatal("the second source saw no pillar")
	}
	if _, ok := second.saw.Get("db"); !ok {
		t.Error("it did not see the tree")
	}
	if got, _ := second.saw.Get("extra"); got != "one" {
		t.Error("it did not see what the first source contributed")
	}
}

// A failure is a hard error by default. Salt logs and carries on, which
// hands the run a pillar missing the half that held the credentials.
func TestAnExternalFailureIsHardByDefault(t *testing.T) {
	tree := map[string]string{
		"base|top": "base:\n  '*':\n    - db\n",
		"base|db":  "db:\n  host: db.example\n",
	}
	src := &fakeExt{name: "vault", fail: errors.New("the token expired")}
	out := compileWithExt(t, tree, src)

	err := out.Err()
	if err == nil {
		t.Fatal("a failed external source compiled successfully")
	}
	if !strings.Contains(err.Error(), "vault") || !strings.Contains(err.Error(), "the token expired") {
		t.Errorf("the error is %q", err)
	}
}

// `ext_pillar_fail: ignore` turns it into a warning, for a source that
// is genuinely optional -- and the warning says what the node is now
// missing rather than reporting success.
func TestAFailSoftSourceWarnsAndTheRestCompiles(t *testing.T) {
	tree := map[string]string{
		"base|top": "base:\n  '*':\n    - db\n",
		"base|db":  "db:\n  host: db.example\n",
	}
	optional := &fakeExt{name: "optional", fail: errors.New("unreachable"), soft: true}
	required := &fakeExt{name: "required", give: value.MapOf("ok", true)}
	out := compileWithExt(t, tree, optional, required)

	if err := out.Err(); err != nil {
		t.Fatalf("an ignored failure failed the compilation: %v", err)
	}
	if got, _ := out.Pillar.Get("ok"); got != true {
		t.Error("the source after the failed one did not run")
	}
	var warned bool
	for _, d := range out.Diags {
		if strings.Contains(d.Msg, "optional") && strings.Contains(d.Msg, "ext_pillar_fail is ignore") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning said what was missing; diags are %v", out.Diags)
	}
}

// A source that contributes nothing is not recorded as having
// contributed, so `Ext` says what actually reached the pillar.
func TestASourceThatGivesNothingIsNotRecorded(t *testing.T) {
	tree := map[string]string{
		"base|top": "base:\n  '*':\n    - db\n",
		"base|db":  "db:\n  host: db.example\n",
	}
	out := compileWithExt(t, tree, &fakeExt{name: "quiet"})
	if len(out.Ext) != 0 {
		t.Errorf("it recorded %v", out.Ext)
	}
}

// compileWithExt compiles a small tree with the given external sources.
func compileWithExt(t *testing.T, files map[string]string, sources ...ExtSource) *Compiled {
	t.Helper()
	ext := make([]ExtSource, 0, len(sources))
	ext = append(ext, sources...)
	return compile(t, files, Config{Ext: ext})
}
