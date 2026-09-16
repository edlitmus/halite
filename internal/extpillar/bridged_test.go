package extpillar

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/edlitmus/halite/ext"
	"github.com/edlitmus/halite/internal/extension"
	"github.com/edlitmus/halite/internal/pillar"
	"github.com/edlitmus/halite/internal/value"
)

// fakeCaller stands in for a loaded extension.
type fakeCaller struct {
	reply string
	err   error
	// saw records what the extension was handed.
	saw     request
	sawFunc string
	sawCtx  *ext.CallContext
}

func (f *fakeCaller) Call(_ context.Context, function string, _, kwargs any,
	callCtx *ext.CallContext) (json.RawMessage, error) {

	f.sawFunc = function
	f.sawCtx = callCtx
	if req, ok := kwargs.(request); ok {
		f.saw = req
	}
	if f.err != nil {
		return nil, f.err
	}
	return json.RawMessage(f.reply), nil
}

func source(f *fakeCaller) *Bridged {
	return &Bridged{SourceName: "test_source", Ext: f}
}

// The extension is handed the node's grains, the pillar so far, and its
// own configuration block — which is what Salt's
// `ext_pillar(minion_id, pillar, *args)` received. // lexicon:allow
func TestTheExtensionIsHandedTheNodeAndTheTree(t *testing.T) {
	f := &fakeCaller{reply: `{"aws_secrets":{"k":"v"}}`}
	src := source(f)
	src.Config = value.MapOf("region", "us-east-1")

	_, err := src.Pillar(context.Background(), pillar.ExtRequest{
		NodeID: "web1.prod",
		Env:    "base",
		Grains: value.MapOf("os", "Ubuntu", "region", "us-gov-east-1"),
		Pillar: value.MapOf("java_home", "/usr/lib/jdk17"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if f.sawFunc != EntryPoint {
		t.Errorf("it called %q", f.sawFunc)
	}
	if f.saw.NodeID != "web1.prod" || f.saw.Env != "base" {
		t.Errorf("it was told %+v", f.saw)
	}
	if !strings.Contains(string(f.saw.Grains), "us-gov-east-1") {
		t.Errorf("grains = %s", f.saw.Grains)
	}
	if !strings.Contains(string(f.saw.Pillar), "jdk17") {
		t.Errorf("pillar = %s", f.saw.Pillar)
	}
	if f.sawCtx == nil || f.sawCtx.NodeID != "web1.prod" {
		t.Errorf("call context = %+v", f.sawCtx)
	}
}

// Both objects always arrive, even when the hub has nothing to put in
// them: an extension written against `grains.os` and handed a null
// would have to guard every read.
func TestGrainsAndPillarAreAlwaysObjects(t *testing.T) {
	f := &fakeCaller{reply: `{}`}
	if _, err := source(f).Pillar(context.Background(), pillar.ExtRequest{}); err != nil {
		t.Fatal(err)
	}
	if string(f.saw.Grains) != "{}" || string(f.saw.Pillar) != "{}" {
		t.Errorf("grains=%s pillar=%s", f.saw.Grains, f.saw.Pillar)
	}
}

// What the extension answers becomes pillar.
func TestTheAnswerBecomesPillar(t *testing.T) {
	f := &fakeCaller{reply: `{"aws_secrets":{"database":{"password":"s3cr3t"}}}`}
	out, err := source(f).Pillar(context.Background(), pillar.ExtRequest{NodeID: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := value.Traverse(out, "aws_secrets:database:password", ":")
	if !ok || got != "s3cr3t" {
		t.Errorf("it produced %v", out)
	}
}

// Everything a source returns is treated as secret. The hub cannot tell
// which of an out-of-process source's values are credentials, so the
// safe reading is that what arrived on the pillar path is pillar.
func TestEveryReturnedStringReachesTheRedactor(t *testing.T) {
	f := &fakeCaller{reply: `{"a":"top","b":{"c":"nested"},"d":["in-a-list"],"n":7}`}
	var offered []string
	src := source(f)
	src.OnSecret = func(v string) { offered = append(offered, v) }

	if _, err := src.Pillar(context.Background(), pillar.ExtRequest{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"top", "nested", "in-a-list"} {
		var seen bool
		for _, got := range offered {
			if got == want {
				seen = true
			}
		}
		if !seen {
			t.Errorf("%q never reached the redactor; offered %v", want, offered)
		}
	}
}

// An extension that fails fails the source, and the error survives for
// the compiler to report.
func TestAFailingExtensionFailsTheSource(t *testing.T) {
	f := &fakeCaller{err: errors.New("the role cannot read that secret")}
	_, err := source(f).Pillar(context.Background(), pillar.ExtRequest{})
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "cannot read that secret") {
		t.Errorf("the error is %q", err)
	}
}

// An extension that answers with something other than a mapping is an
// error rather than a silently empty contribution.
func TestANonMappingAnswerIsAnError(t *testing.T) {
	for _, reply := range []string{`["a","list"]`, `"a string"`, `not json at all`} {
		f := &fakeCaller{reply: reply}
		if _, err := source(f).Pillar(context.Background(), pillar.ExtRequest{}); err == nil {
			t.Errorf("%s was accepted", reply)
		}
	}
}

// An extension with nothing to say contributes nothing, which is not an
// error: a source configured for secrets this node does not have is a
// normal thing.
func TestANullAnswerContributesNothing(t *testing.T) {
	for _, reply := range []string{`null`, ``} {
		f := &fakeCaller{reply: reply}
		out, err := source(f).Pillar(context.Background(), pillar.ExtRequest{})
		if err != nil {
			t.Fatalf("%q: %v", reply, err)
		}
		if out != nil && out.Len() != 0 {
			t.Errorf("%q produced %v", reply, out)
		}
	}
}

// A source whose extension is missing says so rather than pretending.
func TestAnUnloadedExtensionIsAnError(t *testing.T) {
	src := &Bridged{SourceName: "gone"}
	if _, err := src.Pillar(context.Background(), pillar.ExtRequest{}); err == nil {
		t.Fatal("a source with no extension behind it answered")
	}
}

// Every source `Sources` builds carries the callback it was given.
//
// The hub passed nil here for as long as external pillar has existed,
// so every string an external source returned -- which for
// `aws_secrets_manager` is the whole point of the source -- reached the
// hub's logs and comments unrecorded. DIVERGENCE 5.110.
func TestSourcesAttachTheRedactorCallback(t *testing.T) {
	// No executable is needed: Sources reads the manifest and builds a
	// source, and nothing here runs the extension.
	rt := &extension.Runtime{PoolSize: 1}
	t.Cleanup(rt.Close)
	err := rt.Add(&extension.Bundle{
		Dir:      t.TempDir(),
		Manifest: &extension.Manifest{Name: "vault", Version: "1.0.0", Kind: "pillar"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var seen []string
	sources, err := Sources([]Spec{{Name: "vault"}}, rt, func(v string) { seen = append(seen, v) })
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("built %d sources", len(sources))
	}
	b, ok := sources[0].(*Bridged)
	if !ok {
		t.Fatalf("a source is %T", sources[0])
	}
	if b.OnSecret == nil {
		t.Fatal("the source records nothing it returns")
	}
	b.OnSecret("a-value-from-the-source")
	if len(seen) != 1 || seen[0] != "a-value-from-the-source" {
		t.Errorf("the callback that arrived is not the one given: %v", seen)
	}
}
