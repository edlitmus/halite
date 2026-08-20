package state

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/signature"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// mapLoader serves SLS files from memory, keyed by "env|sls".
type mapLoader struct {
	files map[string]string
	envs  []string
}

func newLoader(files map[string]string, envs ...string) *mapLoader {
	if len(envs) == 0 {
		envs = []string{"base"}
	}
	return &mapLoader{files: files, envs: envs}
}

func (m *mapLoader) Source(env, sls string) ([]byte, string, error) {
	if src, ok := m.files[env+"|"+sls]; ok {
		return []byte(src), env + "/" + strings.ReplaceAll(sls, ".", "/") + ".sls", nil
	}
	return nil, "", fmt.Errorf("%w: %s", ErrNotFound, sls)
}

func (m *mapLoader) Envs() []string { return m.envs }

func (m *mapLoader) Templates(env string) template.Loader { return templateFiles{m, env} }

type templateFiles struct {
	m   *mapLoader
	env string
}

func (t templateFiles) Load(name string) (string, string, error) {
	if src, ok := t.m.files[t.env+"|"+name]; ok {
		return src, name, nil
	}
	return "", "", template.ErrNotFound
}

// testRegistry is a small state module registry covering the modules the
// compiler tests declare.
func testRegistry() *signature.Registry {
	r := signature.NewRegistry()
	r.Add(
		signature.Signature{Module: "pkg", Function: "installed", Mutates: true, Params: []signature.Param{
			{Name: "name", Type: signature.String},
			{Name: "version", Type: signature.String},
			{Name: "pkgs", Type: signature.List},
			{Name: "refresh", Type: signature.Bool},
		}},
		signature.Signature{Module: "pkg", Function: "removed", Mutates: true, Params: []signature.Param{
			{Name: "name", Type: signature.String},
		}},
		signature.Signature{Module: "file", Function: "managed", Mutates: true, Params: []signature.Param{
			{Name: "name", Type: signature.Path},
			{Name: "source", Type: signature.String},
			{Name: "mode", Type: signature.Mode},
			{Name: "user", Type: signature.String},
			{Name: "contents", Type: signature.String},
			{Name: "template", Type: signature.String},
			{Name: "context", Type: signature.Map},
		}},
		signature.Signature{Module: "file", Function: "directory", Mutates: true, Params: []signature.Param{
			{Name: "name", Type: signature.Path},
			{Name: "mode", Type: signature.Mode},
		}},
		signature.Signature{Module: "service", Function: "running", Mutates: true, Params: []signature.Param{
			{Name: "name", Type: signature.String},
			{Name: "enable", Type: signature.Bool},
		}},
		signature.Signature{Module: "cmd", Function: "run", Mutates: true, ArbitraryCode: true,
			TestMode: signature.TestUnreliable, Params: []signature.Param{
				{Name: "name", Type: signature.String},
			}},
		signature.Signature{Module: "test", Function: "nop", Params: []signature.Param{
			{Name: "name", Type: signature.String},
		}},
	)
	return r
}

func compile(t *testing.T, files map[string]string, names ...string) *Compiled {
	t.Helper()
	return compileWith(t, files, Config{NodeID: "web1.prod"}, names...)
}

func compileWith(t *testing.T, files map[string]string, cfg Config, names ...string) *Compiled {
	t.Helper()
	if cfg.Grains == nil {
		cfg.Grains = value.MapOf("os_family", "Debian", "os", "Ubuntu")
	}
	c := &Compiler{Loader: newLoader(files), Registry: testRegistry(), Config: cfg}
	if len(names) == 0 {
		return c.CompileHighstate()
	}
	return c.CompileSLS(names)
}

func mustCompile(t *testing.T, files map[string]string, names ...string) *Compiled {
	t.Helper()
	out := compile(t, files, names...)
	if err := out.Err(); err != nil {
		t.Fatalf("compilation failed:\n%v", err)
	}
	return out
}

// runOrder renders the low state as a readable sequence of IDs.
func runOrder(c *Compiled) []string {
	out := make([]string, len(c.Low))
	for i, ch := range c.Low {
		out[i] = ch.ID
	}
	return out
}

func errText(c *Compiled) string {
	if err := c.Err(); err != nil {
		return err.Error()
	}
	return ""
}

// ---- declaration forms, SPEC section 11.1 ----

func TestDeclarationForms(t *testing.T) {
	files := map[string]string{
		"base|web": `
list_form:
  pkg.installed:
    - name: nginx
    - version: '1.24'

dict_form:
  pkg.installed:
    name: apache2

split_form:
  pkg:
    - installed
    - name: haproxy

name_defaults_to_id:
  test.nop: []
`,
	}
	out := mustCompile(t, files, "web")
	if len(out.Low) != 4 {
		t.Fatalf("got %d chunks, want 4: %v", len(out.Low), runOrder(out))
	}
	byID := map[string]*Chunk{}
	for _, ch := range out.Low {
		byID[ch.ID] = ch
	}
	if got, _ := byID["list_form"].Args.Get("version"); got != "1.24" {
		t.Errorf("list form version = %#v", got)
	}
	if byID["dict_form"].Name != "apache2" {
		t.Errorf("dict form name = %q", byID["dict_form"].Name)
	}
	if byID["split_form"].Fun != "installed" || byID["split_form"].Name != "haproxy" {
		t.Errorf("split form = %+v", byID["split_form"])
	}
	if byID["name_defaults_to_id"].Name != "name_defaults_to_id" {
		t.Errorf("name should default to the ID, got %q", byID["name_defaults_to_id"].Name)
	}
}

func TestLowStateKeyFormatIsPreserved(t *testing.T) {
	// Ugly and load-bearing: every dashboard and returner parses it.
	files := map[string]string{"base|web": "nginx_installed:\n  pkg.installed:\n    - name: nginx\n"}
	out := mustCompile(t, files, "web")
	if got := out.Low[0].Key(); got != "pkg_|-nginx_installed_|-nginx_|-installed" {
		t.Errorf("low state key = %q", got)
	}
}

func TestNamesExpansion(t *testing.T) {
	files := map[string]string{
		"base|web": `
tools:
  pkg.installed:
    - names:
      - curl
      - jq
    - refresh: true
`,
	}
	out := mustCompile(t, files, "web")
	if len(out.Low) != 2 {
		t.Fatalf("names should expand to one chunk per name, got %d", len(out.Low))
	}
	for i, want := range []string{"curl", "jq"} {
		if out.Low[i].Name != want {
			t.Errorf("chunk %d name = %q, want %q", i, out.Low[i].Name, want)
		}
		if got, _ := out.Low[i].Args.Get("refresh"); got != true {
			t.Errorf("chunk %d lost the shared argument", i)
		}
		if got, _ := out.Low[i].Args.Get("name"); got != want {
			t.Errorf("chunk %d name argument = %#v", i, got)
		}
	}
}

func TestNamesWithPerNameArguments(t *testing.T) {
	files := map[string]string{
		"base|web": `
sites:
  file.managed:
    - names:
      - /etc/a.conf:
          mode: '0600'
      - /etc/b.conf
    - user: root
`,
	}
	out := mustCompile(t, files, "web")
	if len(out.Low) != 2 {
		t.Fatalf("got %d chunks", len(out.Low))
	}
	if got, _ := out.Low[0].Args.Get("mode"); got != "0600" {
		t.Errorf("per-name argument lost: %#v", got)
	}
	if got, _ := out.Low[1].Args.Get("user"); got != "root" {
		t.Errorf("shared argument lost: %#v", got)
	}
	if out.Low[1].Name != "/etc/b.conf" {
		t.Errorf("name = %q", out.Low[1].Name)
	}
}

// ---- include, extend, exclude ----

func TestIncludeIsDepthFirst(t *testing.T) {
	files := map[string]string{
		"base|top_level": "include:\n  - dep\n\nlast:\n  test.nop: []\n",
		"base|dep":       "first:\n  test.nop: []\n",
	}
	out := mustCompile(t, files, "top_level")
	if got := runOrder(out); got[0] != "first" || got[1] != "last" {
		t.Errorf("include order = %v; an included file's states come first", got)
	}
}

func TestRelativeInclude(t *testing.T) {
	files := map[string]string{
		"base|web.nginx":  "include:\n  - .common\n\nn:\n  test.nop: []\n",
		"base|web.common": "c:\n  test.nop: []\n",
	}
	out := mustCompile(t, files, "web.nginx")
	if got := runOrder(out); len(got) != 2 || got[0] != "c" {
		t.Errorf("relative include did not resolve: %v", got)
	}
}

// TestIncludeCycleReportsThePath covers SPEC section 11.2 step 3, which
// asks for the cycle path to be reported. It is a warning rather than an
// error: mutual includes exist in trees that work today, because Salt's
// visited set absorbs them silently, and the useful half of the change is
// making the cycle visible without breaking those trees.
func TestIncludeCycleReportsThePath(t *testing.T) {
	files := map[string]string{
		"base|a": "include:\n  - b\n\naa:\n  test.nop: []\n",
		"base|b": "include:\n  - a\n\nbb:\n  test.nop: []\n",
	}
	out := compile(t, files, "a")
	if err := out.Err(); err != nil {
		t.Fatalf("a mutual include should still compile: %v", err)
	}
	var msg string
	for _, w := range out.Diags.Warnings() {
		msg += w.String() + "\n"
	}
	if !strings.Contains(msg, "include cycle") {
		t.Fatalf("expected a cycle warning, got:\n%s", msg)
	}
	if !strings.Contains(msg, "a -> b -> a") {
		t.Errorf("the cycle should be printed as a path: %s", msg)
	}
	// Both files' states are still compiled.
	if got := runOrder(out); len(got) != 2 {
		t.Errorf("run = %v, want both states", got)
	}
}

func TestDuplicateIDNamesBothFiles(t *testing.T) {
	files := map[string]string{
		"base|one": "include:\n  - two\n\nshared:\n  test.nop: []\n",
		"base|two": "shared:\n  test.nop: []\n",
	}
	out := compile(t, files, "one")
	msg := errText(out)
	if !strings.Contains(msg, "duplicate state ID") {
		t.Fatalf("expected a duplicate ID error, got:\n%s", msg)
	}
	if !strings.Contains(msg, "two") || !strings.Contains(msg, "one") {
		t.Errorf("the error should name both files: %s", msg)
	}
}

func TestExtendMergesAndAppends(t *testing.T) {
	files := map[string]string{
		"base|site": `
include:
  - web

extend:
  nginx_conf:
    file.managed:
      - user: www-data
      - require:
        - pkg: extra_pkg

extra_pkg:
  pkg.installed:
    - name: extra
`,
		"base|web": `
nginx_conf:
  file.managed:
    - name: /etc/nginx.conf
    - mode: '0644'
    - require:
      - pkg: base_pkg

base_pkg:
  pkg.installed:
    - name: nginx
`,
	}
	out := mustCompile(t, files, "site")
	var conf *Chunk
	for _, ch := range out.Low {
		if ch.ID == "nginx_conf" {
			conf = ch
		}
	}
	if conf == nil {
		t.Fatal("nginx_conf is missing")
	}
	// A scalar the extend supplies is added.
	if got, _ := conf.Args.Get("user"); got != "www-data" {
		t.Errorf("extend did not add user: %#v", got)
	}
	// A scalar the original set survives.
	if got, _ := conf.Args.Get("mode"); got != "0644" {
		t.Errorf("extend clobbered mode: %#v", got)
	}
	// A list is appended rather than replaced, which is what makes the
	// idiomatic extend-plus-require work.
	if len(conf.Reqs) != 2 {
		t.Fatalf("requisites = %d, want the original plus the extended one", len(conf.Reqs))
	}
}

func TestExtendUnknownIDIsAnError(t *testing.T) {
	files := map[string]string{
		"base|web": "extend:\n  nope:\n    test.nop: []\n\nreal:\n  test.nop: []\n",
	}
	out := compile(t, files, "web")
	if !strings.Contains(errText(out), "extend names") {
		t.Errorf("expected an error naming the missing ID:\n%s", errText(out))
	}
}

func TestExtendCanAddANewModule(t *testing.T) {
	files := map[string]string{
		"base|site": `
include:
  - web

extend:
  nginx:
    service.running:
      - enable: true
`,
		"base|web": "nginx:\n  pkg.installed:\n    - name: nginx\n",
	}
	out := mustCompile(t, files, "site")
	if len(out.Low) != 2 {
		t.Fatalf("extend should have added a second chunk, got %v", runOrder(out))
	}
}

func TestExclude(t *testing.T) {
	files := map[string]string{
		"base|site": `
include:
  - web
  - db

exclude:
  - sls: db
  - id: optional_thing

keep:
  test.nop: []
`,
		"base|web": "web_state:\n  test.nop: []\noptional_thing:\n  test.nop: []\n",
		"base|db":  "db_state:\n  test.nop: []\n",
	}
	out := mustCompile(t, files, "site")
	got := runOrder(out)
	for _, gone := range []string{"db_state", "optional_thing"} {
		for _, id := range got {
			if id == gone {
				t.Errorf("%s should have been excluded, got %v", gone, got)
			}
		}
	}
	if len(got) != 2 {
		t.Errorf("run = %v, want web_state and keep", got)
	}
}

// ---- requisites, SPEC section 11.3 ----

func TestRequisiteOrdersTheRun(t *testing.T) {
	files := map[string]string{
		"base|web": `
conf:
  file.managed:
    - name: /etc/nginx.conf
    - require:
      - pkg: install

install:
  pkg.installed:
    - name: nginx
`,
	}
	out := mustCompile(t, files, "web")
	if got := runOrder(out); got[0] != "install" || got[1] != "conf" {
		t.Errorf("run order = %v; the requisite target must run first", got)
	}
}

func TestAllRequisiteKindsParse(t *testing.T) {
	kinds := []string{
		"require", "require_any", "watch", "watch_any",
		"onchanges", "onchanges_any", "onfail", "onfail_any", "onfail_all",
		"prereq", "use", "listen",
	}
	for _, kind := range kinds {
		files := map[string]string{
			"base|web": fmt.Sprintf(`
target:
  pkg.installed:
    - name: nginx

dependent:
  service.running:
    - name: nginx
    - %s:
      - pkg: target
`, kind),
		}
		out := compile(t, files, "web")
		if err := out.Err(); err != nil {
			t.Errorf("%s failed to compile: %v", kind, err)
			continue
		}
		var dep *Chunk
		for _, ch := range out.Low {
			if ch.ID == "dependent" {
				dep = ch
			}
		}
		if dep == nil || len(dep.Reqs) != 1 {
			t.Errorf("%s: requisites = %+v", kind, dep)
			continue
		}
		if dep.Reqs[0].Kind.String() != kind {
			t.Errorf("%s parsed as %s", kind, dep.Reqs[0].Kind)
		}
		if len(dep.Reqs[0].Resolved) != 1 {
			t.Errorf("%s did not resolve", kind)
		}
	}
}

func TestInverseRequisitesResolveIntoForwardForm(t *testing.T) {
	files := map[string]string{
		"base|web": `
install:
  pkg.installed:
    - name: nginx
    - require_in:
      - file: conf

conf:
  file.managed:
    - name: /etc/nginx.conf
`,
	}
	out := mustCompile(t, files, "web")
	if got := runOrder(out); got[0] != "install" || got[1] != "conf" {
		t.Errorf("run order = %v; require_in must order the run", got)
	}
	var conf *Chunk
	for _, ch := range out.Low {
		if ch.ID == "conf" {
			conf = ch
		}
	}
	if len(conf.Reqs) != 1 || conf.Reqs[0].Kind != Require {
		t.Errorf("the inverse form should have become a forward require on conf: %+v", conf.Reqs)
	}
}

func TestRequisiteTargetForms(t *testing.T) {
	files := map[string]string{
		"base|other": "in_other_sls:\n  test.nop: []\n",
		"base|web": `
include:
  - other

install:
  pkg.installed:
    - name: nginx

by_module_and_id:
  test.nop:
    - require:
      - pkg: install

by_id_alone:
  test.nop:
    - require:
      - install

by_explicit_id:
  test.nop:
    - require:
      - id: install

by_sls:
  test.nop:
    - require:
      - sls: other
`,
	}
	out := mustCompile(t, files, "web")
	for _, ch := range out.Low {
		if !strings.HasPrefix(ch.ID, "by_") {
			continue
		}
		if len(ch.Reqs) != 1 || len(ch.Reqs[0].Resolved) == 0 {
			t.Errorf("%s did not resolve its requisite: %+v", ch.ID, ch.Reqs)
		}
	}
}

func TestAmbiguousRequisiteIsAnError(t *testing.T) {
	files := map[string]string{
		"base|web": `
nginx:
  pkg.installed:
    - name: nginx
  service.running:
    - name: nginx

dependent:
  test.nop:
    - require:
      - nginx
`,
	}
	out := compile(t, files, "web")
	msg := errText(out)
	if !strings.Contains(msg, "more than one module") {
		t.Fatalf("an ambiguous requisite must be an error:\n%s", msg)
	}
	if !strings.Contains(msg, "pkg") || !strings.Contains(msg, "service") {
		t.Errorf("the error should name the candidates: %s", msg)
	}
}

func TestUnresolvedRequisiteIsAnError(t *testing.T) {
	files := map[string]string{
		"base|web": "a:\n  test.nop:\n    - require:\n      - pkg: nope\n",
	}
	if !strings.Contains(errText(compile(t, files, "web")), "not declared in this run") {
		t.Errorf("an unresolved requisite must be reported:\n%s", errText(compile(t, files, "web")))
	}
}

func TestUseInheritsArguments(t *testing.T) {
	files := map[string]string{
		"base|web": `
template_state:
  file.managed:
    - name: /etc/a.conf
    - user: root
    - mode: '0644'

derived:
  file.managed:
    - name: /etc/b.conf
    - mode: '0600'
    - use:
      - file: template_state
`,
	}
	out := mustCompile(t, files, "web")
	var derived *Chunk
	for _, ch := range out.Low {
		if ch.ID == "derived" {
			derived = ch
		}
	}
	if got, _ := derived.Args.Get("user"); got != "root" {
		t.Errorf("use did not inherit user: %#v", got)
	}
	// The deriving state's own argument wins.
	if got, _ := derived.Args.Get("mode"); got != "0600" {
		t.Errorf("use overwrote an explicit argument: %#v", got)
	}
	// Identity is never inherited.
	if derived.Name != "/etc/b.conf" {
		t.Errorf("use overwrote the name: %q", derived.Name)
	}
}

// ---- ordering, SPEC section 11.4 ----

func TestUnconstrainedStatesRunInDeclarationOrder(t *testing.T) {
	// Existing trees rely on this far more than they should.
	files := map[string]string{
		"base|web": "third:\n  test.nop: []\nfirst:\n  test.nop: []\nsecond:\n  test.nop: []\n",
	}
	out := mustCompile(t, files, "web")
	want := []string{"third", "first", "second"}
	if got := runOrder(out); !equal(got, want) {
		t.Errorf("run order = %v, want declaration order %v", got, want)
	}
}

func TestExplicitOrder(t *testing.T) {
	files := map[string]string{
		"base|web": `
c:
  test.nop:
    - order: last
a:
  test.nop: []
b:
  test.nop:
    - order: 1
`,
	}
	out := mustCompile(t, files, "web")
	want := []string{"b", "a", "c"}
	if got := runOrder(out); !equal(got, want) {
		t.Errorf("run order = %v, want %v", got, want)
	}
}

func TestRequisiteCyclePrintsThePath(t *testing.T) {
	files := map[string]string{
		"base|web": `
a:
  test.nop:
    - require:
      - test: c
b:
  test.nop:
    - watch:
      - test: a
c:
  test.nop:
    - require:
      - test: b
`,
	}
	out := compile(t, files, "web")
	msg := errText(out)
	if !strings.Contains(msg, "requisite cycle") {
		t.Fatalf("expected a cycle error, got:\n%s", msg)
	}
	for _, id := range []string{"a", "b", "c"} {
		if !strings.Contains(msg, id) {
			t.Errorf("the cycle should name %s: %s", id, msg)
		}
	}
	if !strings.Contains(msg, "->") {
		t.Errorf("the cycle should be printed as a path: %s", msg)
	}
}

func TestPrereqRunsBeforeItsTarget(t *testing.T) {
	// B declares prereq: A. B runs before A. SPEC section 11.5.
	files := map[string]string{
		"base|web": `
a_target:
  pkg.installed:
    - name: nginx

b_prep:
  test.nop:
    - prereq:
      - pkg: a_target
`,
	}
	out := mustCompile(t, files, "web")
	if got := runOrder(out); got[0] != "b_prep" || got[1] != "a_target" {
		t.Errorf("run order = %v; a prereq runs before its target", got)
	}
}

func TestPrereqOnUnreliableTestModeWarns(t *testing.T) {
	files := map[string]string{
		"base|web": `
runner:
  cmd.run:
    - name: /usr/bin/true

prep:
  test.nop:
    - prereq:
      - cmd: runner
`,
	}
	out := mustCompile(t, files, "web")
	warnings := out.Diags.Warnings()
	found := false
	for _, w := range warnings {
		if strings.Contains(w.Msg, "test mode is unreliable") {
			found = true
		}
	}
	if !found {
		t.Errorf("a prereq on an unreliable target must warn: %v", warnings)
	}
}

// ---- validation, SPEC section 11.2 step 10 ----

func TestUnknownStateModuleAndFunction(t *testing.T) {
	files := map[string]string{
		"base|web": "a:\n  nosuchmodule.thing: []\nb:\n  pkg.nosuchfunction: []\n",
	}
	msg := errText(compile(t, files, "web"))
	if !strings.Contains(msg, "nosuchmodule") {
		t.Errorf("an unknown module must be reported: %s", msg)
	}
	if !strings.Contains(msg, "has no function") || !strings.Contains(msg, "installed") {
		t.Errorf("an unknown function should name the ones that exist: %s", msg)
	}
}

func TestEveryErrorIsReportedTogether(t *testing.T) {
	// Salt reports the first and stops, which makes fixing a large tree an
	// iterative grind.
	files := map[string]string{
		"base|web": `
a:
  nosuchmodule.thing: []
b:
  pkg.nosuchfunction: []
c:
  file.managed:
    - name: /etc/f
    - mode: 644
d:
  test.nop:
    - require:
      - pkg: missing_target
`,
	}
	out := compile(t, files, "web")
	errs := out.Diags.Errors()
	if len(errs) < 4 {
		t.Fatalf("got %d errors, want one for each of the four problems:\n%s", len(errs), errText(out))
	}
	msg := errText(out)
	for _, want := range []string{"nosuchmodule", "nosuchfunction", "mode", "missing_target"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the report is missing %q:\n%s", want, msg)
		}
	}
}

func TestUnquotedModeIsCaughtAtCompileTime(t *testing.T) {
	files := map[string]string{
		"base|web": "f:\n  file.managed:\n    - name: /etc/f\n    - mode: 0644\n",
	}
	msg := errText(compile(t, files, "web"))
	if !strings.Contains(msg, "quoted") {
		t.Errorf("an unquoted mode must be refused with advice:\n%s", msg)
	}
}

// ---- top file, SPEC section 11.2 step 1 ----

func TestHighstateResolvesTheTopFile(t *testing.T) {
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
		"base|common":    "c:\n  test.nop: []\n",
		"base|webserver": "w:\n  test.nop: []\n",
		"base|database":  "d:\n  test.nop: []\n",
	}
	out := compileWith(t, files, Config{NodeID: "web1.prod"})
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	got := runOrder(out)
	if !equal(got, []string{"c", "w"}) {
		t.Errorf("highstate = %v, want the common and webserver states only", got)
	}
}

func TestTopFileGrainMatching(t *testing.T) {
	files := map[string]string{
		"base|top": `
base:
  'G@os_family:Debian':
    - apt_things
  'G@os_family:RedHat':
    - yum_things
`,
		"base|apt_things": "a:\n  test.nop: []\n",
		"base|yum_things": "y:\n  test.nop: []\n",
	}
	out := compileWith(t, files, Config{
		NodeID: "web1.prod",
		Grains: value.MapOf("os_family", "Debian"),
	})
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	if got := runOrder(out); !equal(got, []string{"a"}) {
		t.Errorf("grain matching = %v", got)
	}
}

func TestTopFileMatchDirective(t *testing.T) {
	files := map[string]string{
		"base|top": `
base:
  'os_family:Debian':
    - match: grain
    - apt_things
`,
		"base|apt_things": "a:\n  test.nop: []\n",
	}
	out := compileWith(t, files, Config{NodeID: "web1.prod", Grains: value.MapOf("os_family", "Debian")})
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	if got := runOrder(out); !equal(got, []string{"a"}) {
		t.Errorf("the match directive was not honoured: %v", got)
	}
}

func TestMissingTopFileIsReported(t *testing.T) {
	out := compileWith(t, map[string]string{}, Config{NodeID: "web1.prod"})
	if !strings.Contains(errText(out), "no top file") {
		t.Errorf("a highstate with no top file must say so:\n%s", errText(out))
	}
}

// ---- templating ----

func TestTemplatedSLSSeesGrainsAndPillar(t *testing.T) {
	files := map[string]string{
		"base|web": `
{% for pkg in pillar['packages'] %}
install_{{ pkg }}:
  pkg.installed:
    - name: {{ pkg }}
{% endfor %}

conf:
  file.managed:
    - name: /etc/{{ grains['os']|lower }}.conf
`,
	}
	out := compileWith(t, files, Config{
		NodeID: "web1.prod",
		Grains: value.MapOf("os", "Ubuntu"),
		Pillar: value.MapOf("packages", []any{"nginx", "curl"}),
	}, "web")
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"install_nginx", "install_curl", "conf"}
	if got := runOrder(out); !equal(got, want) {
		t.Errorf("run order = %v, want %v", got, want)
	}
	if out.Low[2].Name != "/etc/ubuntu.conf" {
		t.Errorf("templated name = %q", out.Low[2].Name)
	}
}

func TestStateAllowAndDenyLists(t *testing.T) {
	files := map[string]string{
		"base|web": "w:\n  test.nop: []\n",
		"base|db":  "d:\n  test.nop: []\n",
	}
	out := compileWith(t, files, Config{NodeID: "n", StateDenylist: []string{"db"}}, "web", "db")
	if !strings.Contains(errText(out), "excluded by state_allowlist or state_denylist") {
		t.Errorf("the denylist should have refused db:\n%s", errText(out))
	}
}

func TestPerStateOptionsAreParsed(t *testing.T) {
	files := map[string]string{
		"base|web": `
guarded:
  cmd.run:
    - name: /usr/bin/setup
    - unless: test -f /etc/done
    - creates: /etc/done
    - retry:
        attempts: 3
        interval: 10
    - timeout: 60
    - parallel: true
    - reload_modules: true
`,
	}
	out := mustCompile(t, files, "web")
	ch := out.Low[0]
	if len(ch.Opts.Unless) != 1 {
		t.Errorf("unless = %v", ch.Opts.Unless)
	}
	if len(ch.Opts.Creates) != 1 || ch.Opts.Creates[0] != "/etc/done" {
		t.Errorf("creates = %v", ch.Opts.Creates)
	}
	if ch.Opts.Retry == nil || ch.Opts.Retry.Attempts != 3 || ch.Opts.Retry.Interval.Seconds() != 10 {
		t.Errorf("retry = %+v", ch.Opts.Retry)
	}
	if ch.Opts.Timeout.Seconds() != 60 {
		t.Errorf("timeout = %v", ch.Opts.Timeout)
	}
	if !ch.Opts.Parallel || !ch.Opts.ReloadModules {
		t.Errorf("flags = %+v", ch.Opts)
	}
	// A runner option must not reach the module as an argument.
	for _, name := range []string{"unless", "creates", "retry", "timeout", "parallel", "reload_modules"} {
		if ch.Args.Has(name) {
			t.Errorf("%s leaked into the module arguments", name)
		}
	}
}

func equal(a, b []string) bool {
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
