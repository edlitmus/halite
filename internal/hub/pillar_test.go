package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/builtin"
	"github.com/edlitmus/halite/internal/fileserver"
	"github.com/edlitmus/halite/internal/transport"
	"github.com/edlitmus/halite/internal/value"
)

// withPillar gives the lab a pillar tree to compile from.
func (l *lab) withPillar(t *testing.T, files map[string]string) *lab {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l.server.Pillar = &PillarOptions{
		Roots:         fileserver.NewRoots(map[string][]string{"base": {root}}),
		TrustedGrains: []string{"os", "role"},
		Registry:      builtin.New().Exec,
		ConfigValues:  value.NewMap(0),
	}
	return l
}

func askPillar(t *testing.T, client *transport.Client, nodeID, grains string) *value.Map {
	t.Helper()
	res, err := client.Pillar(context.Background(), transport.PillarRequest{
		NodeID: nodeID, Env: "base", Grains: json.RawMessage(grains),
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := value.DecodeJSON(res.Pillar)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := decoded.(*value.Map)
	if !ok {
		t.Fatalf("the hub sent %T, not a mapping", decoded)
	}
	return m
}

// The reason pillar belongs on the hub: a node gets its own and cannot
// see another's, whatever it asks for.
func TestTheHubCompilesEachNodesOwnPillar(t *testing.T) {
	l := newLab(t).withPillar(t, map[string]string{
		"top.sls": `base:
  'web*':
    - web
  'db*':
    - db
`,
		"web.sls": "role: web\nsecret: web-only\n",
		"db.sls":  "role: db\nsecret: db-only\n",
	})
	web := l.enrolled(t, "web1.example")
	db := l.enrolled(t, "db1.example")

	got := askPillar(t, web, "web1.example", `{"os":"FreeBSD"}`)
	if v, _ := got.Get("secret"); v != "web-only" {
		t.Errorf("web1 got %v", v)
	}
	if _, ok := got.Get("role"); !ok {
		t.Error("web1 got no role")
	}

	got = askPillar(t, db, "db1.example", `{"os":"Linux"}`)
	if v, _ := got.Get("secret"); v != "db-only" {
		t.Errorf("db1 got %v", v)
	}

	// The body cannot ask for someone else's.
	_, err := db.Pillar(context.Background(), transport.PillarRequest{
		NodeID: "web1.example", Env: "base",
	})
	if err == nil {
		t.Fatal("db1 asked for web1's pillar and got it")
	}
	if !strings.Contains(err.Error(), "db1.example") {
		t.Errorf("the refusal should name the certificate's identity: %v", err)
	}
}

// SPEC 12.4: a node controls its own grains, so a top file may target
// only on the allowlist. Compiling on the hub does not change that --
// the grains still arrive from the node.
//
// The compiler refuses the *expression* rather than quietly failing to
// match it, which is the stronger behaviour: a top file that targets on
// a grain nobody vetted is a mistake in the tree, and an estate that
// discovers it as "the pillar is mysteriously empty" discovers it late.
func TestAnUntrustedGrainCannotSelectPillar(t *testing.T) {
	l := newLab(t).withPillar(t, map[string]string{
		"top.sls": `base:
  'role:privileged':
    - match: grain
    - secrets
`,
		"secrets.sls": "root_password: hunter2\n",
	})
	l.server.Pillar.TrustedGrains = []string{"os"}
	node := l.enrolled(t, "web1.example")

	_, err := node.Pillar(context.Background(), transport.PillarRequest{
		NodeID: "web1.example", Env: "base",
		Grains: json.RawMessage(`{"os":"FreeBSD","role":"privileged"}`),
	})
	if err == nil {
		t.Fatal("a top file targeting an unvetted grain compiled")
	}
	// And the node is not told what the tree contains.
	if strings.Contains(err.Error(), "root_password") || strings.Contains(err.Error(), "privileged") {
		t.Errorf("the refusal carried the tree's contents: %v", err)
	}

	// With the grain vetted, the same request delivers it: the
	// allowlist is the control, not an accident of matching.
	l.server.Pillar.TrustedGrains = []string{"os", "role"}
	got := askPillar(t, node, "web1.example", `{"os":"FreeBSD","role":"privileged"}`)
	if v, _ := got.Get("root_password"); v != "hunter2" {
		t.Errorf("a vetted grain did not deliver the pillar: %v", v)
	}

	// A node that does not carry the grain gets nothing, which is the
	// ordinary case and not an error.
	plain := askPillar(t, node, "web1.example", `{"os":"FreeBSD"}`)
	if _, ok := plain.Get("root_password"); ok {
		t.Error("a node without the grain received the pillar behind it")
	}
}

// A hub with no pillar roots says so rather than answering with an
// empty pillar, which would look like a successful compilation of
// nothing.
func TestAHubWithNoPillarSaysSo(t *testing.T) {
	l := newLab(t)
	node := l.enrolled(t, "web1.example")
	_, err := node.Pillar(context.Background(), transport.PillarRequest{NodeID: "web1.example"})
	if err == nil {
		t.Fatal("a hub with no pillar roots answered with a pillar")
	}
	if !strings.Contains(err.Error(), "pillar_roots") {
		t.Errorf("the answer should say what is missing: %v", err)
	}
}

// A pillar file that will not compile must not become an empty pillar
// on the node: SPEC 12.7 says a partial pillar is worse than none.
func TestABrokenPillarIsAnErrorAndNotAnEmptyOne(t *testing.T) {
	l := newLab(t).withPillar(t, map[string]string{
		"top.sls":    "base:\n  '*':\n    - broken\n",
		"broken.sls": "{{ pillar['nope'] }}\n",
	})
	node := l.enrolled(t, "web1.example")
	_, err := node.Pillar(context.Background(), transport.PillarRequest{NodeID: "web1.example"})
	if err == nil {
		t.Fatal("a pillar that does not compile came back as a pillar")
	}
	// The node is told that it failed and not what is in someone
	// else's tree.
	if strings.Contains(err.Error(), "nope") {
		t.Errorf("the refusal carried the tree's contents: %v", err)
	}
}

// The hub is where hub-side pillar is decrypted, so it is where the
// redactor has to learn the values. `OnSecret` was declared on the
// compiler, wired on the node, and left off the hub, so a hub opened
// GPG blocks and told its own redactor nothing -- and a compilation
// warning naming what was inside one went out unredacted.
//
// Nothing failed, because nothing looks at a callback that is never
// called. This asserts the seam instead: what the hub is given is what
// the compiler gets.
func TestTheHubPassesOnSecretToTheCompiler(t *testing.T) {
	var seen []string
	opts := &PillarOptions{
		OnSecret: func(v string) { seen = append(seen, v) },
	}
	cfg := pillarConfigFor(opts, "web1.prod", "base", value.NewMap(0))
	if cfg.OnSecret == nil {
		t.Fatal("the compiler was given no OnSecret, so nothing the hub decrypts is recorded")
	}
	cfg.OnSecret("a-decrypted-value")
	if len(seen) != 1 || seen[0] != "a-decrypted-value" {
		t.Errorf("the callback that arrived is not the one given: %v", seen)
	}
}

// And a hub with no redactor stays workable: the compiler takes a nil
// callback and the render path checks it.
func TestTheHubMayHaveNoOnSecret(t *testing.T) {
	cfg := pillarConfigFor(&PillarOptions{}, "web1.prod", "base", value.NewMap(0))
	if cfg.OnSecret != nil {
		t.Error("an OnSecret appeared from nowhere")
	}
}

// The rest of the options travel too. A seam test that only checked the
// field it was written for would pass while the next one went missing.
func TestThePillarOptionsReachTheCompiler(t *testing.T) {
	opts := &PillarOptions{
		TrustedGrains:    []string{"role"},
		MergeLists:       true,
		Nondeterministic: true,
		Renderer:         []string{"jinja", "yaml"},
	}
	cfg := pillarConfigFor(opts, "web1.prod", "prod", value.NewMap(0))
	if cfg.NodeID != "web1.prod" || cfg.Env != "prod" {
		t.Errorf("node or env lost: %q %q", cfg.NodeID, cfg.Env)
	}
	if len(cfg.TrustedGrains) != 1 || cfg.TrustedGrains[0] != "role" {
		t.Errorf("TrustedGrains = %v", cfg.TrustedGrains)
	}
	if !cfg.MergeLists || !cfg.Nondeterministic {
		t.Error("a boolean option was dropped")
	}
	if strings.Join(cfg.Renderer, "|") != "jinja|yaml" {
		t.Errorf("Renderer = %v", cfg.Renderer)
	}
	// The hub never compiles as though it were a node's own tree.
	if cfg.Local {
		t.Error("the hub compiled as Local")
	}
}
