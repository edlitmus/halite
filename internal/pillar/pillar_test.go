package pillar

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

type memLoader map[string]string

func (m memLoader) Source(env, sls string) ([]byte, string, error) {
	src, ok := m[env+"|"+sls]
	if !ok {
		return nil, "", fmt.Errorf("%w: %s", state.ErrNotFound, sls)
	}
	return []byte(src), sls + ".sls", nil
}

func (m memLoader) Envs() []string { return []string{"base"} }

func (m memLoader) Templates(env string) template.Loader { return memTemplates{m, env} }

type memTemplates struct {
	m   memLoader
	env string
}

func (t memTemplates) Load(name string) (string, string, error) {
	if src, ok := t.m[t.env+"|"+name]; ok {
		return src, name, nil
	}
	return "", "", template.ErrNotFound
}

func compile(t *testing.T, files map[string]string, cfg Config) *Compiled {
	t.Helper()
	if cfg.NodeID == "" {
		cfg.NodeID = "web1.prod"
	}
	if cfg.Grains == nil {
		cfg.Grains = value.MapOf("os_family", "Debian", "os", "Ubuntu")
	}
	c := &Compiler{Loader: memLoader(files), Config: cfg}
	return c.Compile()
}

func mustCompile(t *testing.T, files map[string]string, cfg Config) *Compiled {
	t.Helper()
	out := compile(t, files, cfg)
	if err := out.Err(); err != nil {
		t.Fatalf("pillar compilation failed:\n%v", err)
	}
	return out
}

func get(t *testing.T, out *Compiled, path string) any {
	t.Helper()
	v, ok := value.Traverse(out.Pillar, path, ":")
	if !ok {
		t.Fatalf("%q did not resolve in %v", path, out.Pillar.StringKeys())
	}
	return v
}

func TestPillarTopMatchesAndMerges(t *testing.T) {
	files := map[string]string{
		"base|top": `
base:
  '*':
    - common
  'web*':
    - webserver
  'db*':
    - database
`,
		"base|common":    "shared: yes-value\ntimeout: 30\n",
		"base|webserver": "role: webserver\ntimeout: 60\n",
		"base|database":  "role: database\n",
	}
	out := mustCompile(t, files, Config{})
	if got := get(t, out, "role"); got != "webserver" {
		t.Errorf("role = %#v", got)
	}
	// A later source replaces a scalar.
	if got := get(t, out, "timeout"); got != int64(60) {
		t.Errorf("timeout = %#v; the later source should win", got)
	}
	if got := get(t, out, "shared"); got != "yes-value" {
		t.Errorf("shared = %#v", got)
	}
	// The database pillar was never delivered.
	if _, ok := value.Traverse(out.Pillar, "role", ":"); ok && get(t, out, "role") == "database" {
		t.Error("this node received the database pillar")
	}
}

// TestUntrustedGrainTargetingIsRefused is the control of SPEC section
// 12.4. A node controls its own grains, so without an allowlist it could
// claim `role: database` and be handed the database secrets.
func TestUntrustedGrainTargetingIsRefused(t *testing.T) {
	files := map[string]string{
		"base|top": `
base:
  'G@custom_role:database':
    - secrets
`,
		"base|secrets": "db_password: hunter2\n",
	}
	out := compile(t, files, Config{
		Grains: value.MapOf("custom_role", "database"),
	})
	if err := out.Err(); err == nil {
		t.Fatal("targeting on an untrusted grain must be refused")
	}
	msg := out.Err().Error()
	for _, want := range []string{"custom_role", "pillar_trusted_grains", "12.4"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, msg)
		}
	}
	// And nothing was delivered.
	if out.Pillar.Len() != 0 {
		t.Errorf("the secret was delivered anyway: %v", out.Pillar.StringKeys())
	}
}

func TestTrustedGrainTargetingWorks(t *testing.T) {
	files := map[string]string{
		"base|top": "base:\n  'G@os_family:Debian':\n    - apt\n",
		"base|apt": "package_manager: apt\n",
	}
	out := mustCompile(t, files, Config{})
	if got := get(t, out, "package_manager"); got != "apt" {
		t.Errorf("package_manager = %#v", got)
	}
}

func TestAddingAGrainToTheAllowlistIsARecordedDecision(t *testing.T) {
	files := map[string]string{
		"base|top":     "base:\n  'G@custom_role:database':\n    - secrets\n",
		"base|secrets": "db_password: hunter2\n",
	}
	cfg := Config{
		Grains:        value.MapOf("custom_role", "database"),
		TrustedGrains: append(append([]string{}, TrustedGrains...), "custom_role"),
	}
	out := mustCompile(t, files, cfg)
	if got := get(t, out, "db_password"); got != "hunter2" {
		t.Errorf("db_password = %#v", got)
	}
	// The delivery is recorded, so an unexpected one is reconstructable.
	if len(out.Audit) != 1 {
		t.Fatalf("audit = %v", out.Audit)
	}
	a := out.Audit[0]
	if a.Basis != "grain" || !strings.Contains(a.String(), "secrets") {
		t.Errorf("audit entry = %s", a.String())
	}
}

// TestPillarCannotTargetOnPillar covers the other half of section 12.4:
// pillar does not exist yet while it is being compiled, and pretending it
// does produces an ordering-dependent result.
func TestPillarCannotTargetOnPillar(t *testing.T) {
	for _, expr := range []string{"I@role:database", "J@role:^data"} {
		files := map[string]string{
			"base|top":     "base:\n  '" + expr + "':\n    - secrets\n",
			"base|secrets": "db_password: hunter2\n",
		}
		out := compile(t, files, Config{})
		if err := out.Err(); err == nil {
			t.Errorf("%s should be refused", expr)
			continue
		}
		if !strings.Contains(out.Err().Error(), "not available while pillar is being compiled") {
			t.Errorf("%s: %v", expr, out.Err())
		}
	}
}

// TestHubAuthoritativeAttributesCannotBeForged is the escape hatch the
// allowlist leaves: an attribute the hub sets, exposed under `node:`,
// which the node has no way to influence.
func TestHubAuthoritativeAttributesCannotBeForged(t *testing.T) {
	files := map[string]string{
		"base|top":     "base:\n  'G@node:role:database':\n    - secrets\n",
		"base|secrets": "db_password: hunter2\n",
	}

	// A node that claims the role in its own grains gets nothing.
	out := mustCompile(t, files, Config{
		Grains: value.MapOf("node", value.MapOf("role", "database")),
	})
	if out.Pillar.Len() != 0 {
		t.Errorf("a node forged the attribute through its grains: %v", out.Pillar.StringKeys())
	}

	// The hub setting it delivers the secret.
	out = mustCompile(t, files, Config{
		NodeAttrs: value.MapOf("role", "database"),
	})
	if got := get(t, out, "db_password"); got != "hunter2" {
		t.Errorf("db_password = %#v", got)
	}
}

func TestPillarIncludeMergesBeneathTheIncludingFile(t *testing.T) {
	files := map[string]string{
		"base|top": "base:\n  '*':\n    - site\n",
		"base|site": `include:
  - defaults

port: 8080
`,
		"base|defaults": "port: 80\nhost: localhost\n",
	}
	out := mustCompile(t, files, Config{})
	if got := get(t, out, "port"); got != int64(8080) {
		t.Errorf("port = %#v; the including file's own key should win", got)
	}
	if got := get(t, out, "host"); got != "localhost" {
		t.Errorf("host = %#v", got)
	}
	// The include directive itself never becomes pillar data.
	if out.Pillar.Has("include") {
		t.Error("the include directive leaked into the pillar")
	}
}

func TestPillarRelativeInclude(t *testing.T) {
	files := map[string]string{
		"base|top":          "base:\n  '*':\n    - web.site\n",
		"base|web.site":     "include:\n  - .defaults\n\nport: 8080\n",
		"base|web.defaults": "host: localhost\n",
	}
	out := mustCompile(t, files, Config{})
	if got := get(t, out, "host"); got != "localhost" {
		t.Errorf("host = %#v", got)
	}
}

func TestPillarIncludeCycleIsReportedNotFatal(t *testing.T) {
	files := map[string]string{
		"base|top": "base:\n  '*':\n    - a\n",
		"base|a":   "include:\n  - b\n\nfrom_a: 1\n",
		"base|b":   "include:\n  - a\n\nfrom_b: 2\n",
	}
	out := mustCompile(t, files, Config{})
	var msg string
	for _, w := range out.Diags.Warnings() {
		msg += w.String()
	}
	if !strings.Contains(msg, "pillar include cycle") {
		t.Errorf("expected a cycle warning, got: %s", msg)
	}
	// Both files still contributed.
	if !out.Pillar.Has("from_a") || !out.Pillar.Has("from_b") {
		t.Errorf("pillar = %v", out.Pillar.StringKeys())
	}
}

func TestMergeStrategies(t *testing.T) {
	files := map[string]string{
		"base|top":    "base:\n  '*':\n    - first\n    - second\n",
		"base|first":  "nested:\n  a: 1\n  b: 2\nlist:\n  - x\n",
		"base|second": "nested:\n  b: 22\n  c: 3\nlist:\n  - y\n",
	}

	// smart and recurse deep merge mappings and replace lists.
	out := mustCompile(t, files, Config{Strategy: value.Smart})
	if got := get(t, out, "nested:a"); got != int64(1) {
		t.Errorf("smart nested:a = %#v", got)
	}
	if got := get(t, out, "nested:b"); got != int64(22) {
		t.Errorf("smart nested:b = %#v", got)
	}
	if got := get(t, out, "list"); len(got.([]any)) != 1 {
		t.Errorf("smart list = %#v; lists replace by default", got)
	}

	// pillar_merge_lists concatenates.
	out = mustCompile(t, files, Config{Strategy: value.Recurse, MergeLists: true})
	if got := get(t, out, "list"); len(got.([]any)) != 2 {
		t.Errorf("with merge_lists the list = %#v", got)
	}

	// aggregate concatenates too, and still deep merges mappings.
	out = mustCompile(t, files, Config{Strategy: value.Aggregate})
	if got := get(t, out, "list"); len(got.([]any)) != 2 {
		t.Errorf("aggregate list = %#v", got)
	}
	if got := get(t, out, "nested:a"); got != int64(1) {
		t.Errorf("aggregate nested:a = %#v", got)
	}

	// overwrite replaces a top-level key wholesale.
	out = mustCompile(t, files, Config{Strategy: value.Overwrite})
	if _, ok := value.Traverse(out.Pillar, "nested:a", ":"); ok {
		t.Error("overwrite should have replaced the whole nested mapping")
	}
	if got := get(t, out, "nested:c"); got != int64(3) {
		t.Errorf("overwrite nested:c = %#v", got)
	}
}

func TestPillarTemplateSeesGrainsAndThePartialPillar(t *testing.T) {
	files := map[string]string{
		"base|top":   "base:\n  '*':\n    - first\n    - second\n",
		"base|first": "family: {{ grains['os_family'] }}\n",
		// SPEC section 10.2.7: pillar.get inside pillar rendering resolves
		// against the partially built tree, in declaration order.
		"base|second": "derived: {{ pillar.get('family', 'unknown') }}-derived\n",
	}
	out := mustCompile(t, files, Config{})
	if got := get(t, out, "family"); got != "Debian" {
		t.Errorf("family = %#v", got)
	}
	if got := get(t, out, "derived"); got != "Debian-derived" {
		t.Errorf("derived = %#v; the partial pillar should be visible", got)
	}
}

func TestMissingPillarSLSIsAnError(t *testing.T) {
	files := map[string]string{
		"base|top": "base:\n  '*':\n    - absent\n",
	}
	out := compile(t, files, Config{})
	if out.Err() == nil || !strings.Contains(out.Err().Error(), "absent") {
		t.Errorf("err = %v", out.Err())
	}
}

func TestNoTopFileDeliversNothing(t *testing.T) {
	// A pillar tree with no top file is a legitimate configuration, not
	// an error: a node simply has no pillar.
	out := mustCompile(t, map[string]string{}, Config{})
	if out.Pillar.Len() != 0 {
		t.Errorf("pillar = %v", out.Pillar.StringKeys())
	}
	if len(out.Audit) != 0 {
		t.Errorf("audit = %v", out.Audit)
	}
}

func TestMalformedPillarIsReported(t *testing.T) {
	cases := []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"base|top": "- notamapping\n"}, "must hold a mapping of environments"},
		{map[string]string{"base|top": "base: notamapping\n"}, "must hold a mapping of target expressions"},
		{map[string]string{"base|top": "base:\n  '*': notalist\n"}, "must hold a list of SLS names"},
		{
			map[string]string{
				"base|top":    "base:\n  '*':\n    - scalar\n",
				"base|scalar": "just a string\n",
			},
			"must hold a mapping",
		},
	}
	for _, c := range cases {
		out := compile(t, c.files, Config{})
		if out.Err() == nil || !strings.Contains(out.Err().Error(), c.want) {
			t.Errorf("expected %q, got: %v", c.want, out.Err())
		}
	}
}

func TestGrainNamesInExtractsEveryTarget(t *testing.T) {
	cases := map[string][]string{
		"G@os_family:Debian":         {"os_family"},
		"P@osrelease:^22":            {"osrelease"},
		"G@a:1 and G@b:2":            {"a", "b"},
		"web*":                       nil,
		"G@custom and not P@other:x": {"custom", "other"},
	}
	for expr, want := range cases {
		got := grainNamesIn(expr)
		if len(got) != len(want) {
			t.Errorf("%q -> %v, want %v", expr, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%q -> %v, want %v", expr, got, want)
				break
			}
		}
	}
}

func TestAuditRecordsEveryDelivery(t *testing.T) {
	files := map[string]string{
		"base|top": `
base:
  '*':
    - common
  'web*':
    - webserver
`,
		"base|common":    "a: 1\n",
		"base|webserver": "b: 2\n",
	}
	out := mustCompile(t, files, Config{})
	if len(out.Audit) != 2 {
		t.Fatalf("audit = %v, want one entry per matched target", out.Audit)
	}
	// Each entry names the environment, the expression, the basis, and
	// what it delivered, so an unexpected secret is reconstructable.
	for _, a := range out.Audit {
		if a.Env == "" || a.Target == "" || a.Basis == "" || len(a.SLS) == 0 {
			t.Errorf("incomplete audit entry: %+v", a)
		}
	}
	if len(out.SLS) != 2 {
		t.Errorf("contributing files = %v", out.SLS)
	}
}
