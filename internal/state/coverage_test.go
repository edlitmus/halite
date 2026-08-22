package state

import (
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/value"
)

// These tests reach the compiler paths the behavioural suite does not: the
// option parsers, the diagnostic renderings, the alternate include and
// requisite spellings, and the top file's match aliases.

func TestDiagRendering(t *testing.T) {
	d := Diag{
		Pos: value.Pos{File: "web.sls", Line: 4, Col: 2},
		SLS: "web", ID: "nginx",
		Msg: "something is wrong",
		Related: []Related{
			{Pos: value.Pos{File: "other.sls", Line: 9}, Msg: "first declared here"},
		},
	}
	got := d.String()
	for _, want := range []string{"web.sls:4:2", "[nginx]", "something is wrong", "first declared here"} {
		if !strings.Contains(got, want) {
			t.Errorf("Diag.String is missing %q:\n%s", want, got)
		}
	}
	if d.Error() != d.String() {
		t.Error("Error and String should agree")
	}

	// Without a position the SLS name stands in; without either, a
	// placeholder, so a diagnostic is never anonymous.
	if got := (Diag{SLS: "web", Msg: "m"}).String(); !strings.HasPrefix(got, "web:") {
		t.Errorf("SLS fallback = %q", got)
	}
	if got := (Diag{Msg: "m"}).String(); !strings.HasPrefix(got, "<state>") {
		t.Errorf("bare diagnostic = %q", got)
	}
}

func TestDiagsPartitioning(t *testing.T) {
	var d Diags
	d.Add(value.Pos{File: "b.sls", Line: 2}, "b", "", "an error")
	d.Warn(value.Pos{File: "a.sls", Line: 1}, "a", "", "a warning")
	d.AddRelated(value.Pos{File: "a.sls", Line: 5}, "a", "id",
		[]Related{{Pos: value.Pos{File: "a.sls", Line: 1}, Msg: "here"}}, "another error")

	if !d.HasErrors() {
		t.Error("HasErrors = false with two errors present")
	}
	if len(d.Errors()) != 2 || len(d.Warnings()) != 1 {
		t.Errorf("errors=%d warnings=%d", len(d.Errors()), len(d.Warnings()))
	}
	// Sorted reads top to bottom through the tree.
	sorted := d.Sorted()
	if sorted[0].Pos.File != "a.sls" || sorted[0].Pos.Line != 1 {
		t.Errorf("first sorted = %v", sorted[0].Pos)
	}
	msg := d.Err().Error()
	if !strings.Contains(msg, "2 error(s)") {
		t.Errorf("Err = %q", msg)
	}
	// A collection of warnings alone is not a failure.
	var warnOnly Diags
	warnOnly.Warn(value.Pos{}, "a", "", "just a warning")
	if warnOnly.HasErrors() || warnOnly.Err() != nil {
		t.Error("warnings alone should not fail a compilation")
	}
}

func TestChunkAccessors(t *testing.T) {
	files := map[string]string{"base|web": "nginx:\n  pkg.installed:\n    - name: nginx\n"}
	out := mustCompile(t, files, "web")
	ch := out.Low[0]
	if ch.Func() != "pkg.installed" {
		t.Errorf("Func = %q", ch.Func())
	}
	if !strings.Contains(ch.Describe(), "nginx") || !strings.Contains(ch.Describe(), "web") {
		t.Errorf("Describe = %q", ch.Describe())
	}
	if out.High.Len() != 1 {
		t.Errorf("High.Len = %d", out.High.Len())
	}
	if _, ok := out.High.Lookup("nginx"); !ok {
		t.Error("Lookup missed a declaration it holds")
	}
	if _, ok := out.High.Lookup("absent"); ok {
		t.Error("Lookup found a declaration it does not hold")
	}
}

func TestRequisiteRefRendering(t *testing.T) {
	cases := []struct {
		ref  ReqRef
		want string
	}{
		{ReqRef{ID: "nginx"}, "nginx"},
		{ReqRef{State: "pkg", ID: "nginx"}, "pkg: nginx"},
		{ReqRef{SLS: "web.common"}, "sls: web.common"},
	}
	for _, c := range cases {
		if got := c.ref.String(); got != c.want {
			t.Errorf("%+v = %q, want %q", c.ref, got, c.want)
		}
	}
	// Every requisite kind renders its own name, which is what a
	// diagnostic prints.
	for kind, want := range map[ReqKind]string{
		Require: "require", RequireAny: "require_any",
		Watch: "watch", WatchAny: "watch_any",
		OnChanges: "onchanges", OnChangesAny: "onchanges_any",
		OnFail: "onfail", OnFailAny: "onfail_any", OnFailAll: "onfail_all",
		Prereq: "prereq", Use: "use", Listen: "listen",
	} {
		if got := kind.String(); got != want {
			t.Errorf("ReqKind(%d) = %q, want %q", kind, got, want)
		}
	}
}

func TestIsRequisiteAndOptionArgs(t *testing.T) {
	for _, name := range []string{"require", "watch_in", "onfail_all", "prereq_in", "use_in"} {
		if !IsRequisiteArg(name) {
			t.Errorf("IsRequisiteArg(%q) = false", name)
		}
	}
	for _, name := range []string{"name", "mode", "source"} {
		if IsRequisiteArg(name) {
			t.Errorf("IsRequisiteArg(%q) = true", name)
		}
	}
	for _, name := range []string{"unless", "onlyif", "retry", "order", "names"} {
		if !IsOptionArg(name) {
			t.Errorf("IsOptionArg(%q) = false", name)
		}
	}
	if IsOptionArg("source") {
		t.Error("IsOptionArg(source) = true")
	}
}

func TestIncludeSpellings(t *testing.T) {
	files := map[string]string{
		"base|web.deep.leaf": "leaf:\n  test.nop: []\n",
		"base|web.sibling":   "sibling:\n  test.nop: []\n",
		"base|top_of_tree":   "root_level:\n  test.nop: []\n",
		"base|web.deep.here": `include:
  - .leaf
  - ..sibling
  - top_of_tree

here:
  test.nop: []
`,
	}
	out := mustCompile(t, files, "web.deep.here")
	ids := runOrder(out)
	for _, want := range []string{"leaf", "sibling", "root_level", "here"} {
		found := false
		for _, id := range ids {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s was not included: %v", want, ids)
		}
	}
}

func TestIncludeFromAnotherEnvironment(t *testing.T) {
	loader := newLoader(map[string]string{
		"base|web":    "include:\n  - prod:\n    - shared\n\nweb:\n  test.nop: []\n",
		"prod|shared": "shared:\n  test.nop: []\n",
	}, "base", "prod")
	c := &Compiler{
		Loader:   loader,
		Registry: testRegistry(),
		Config:   Config{NodeID: "n", Grains: value.NewMap(0)},
	}
	out := c.CompileSLS([]string{"web"})
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	if got := runOrder(out); len(got) != 2 || got[0] != "shared" {
		t.Errorf("run = %v; the cross-environment include should have come first", got)
	}
}

func TestMalformedIncludeAndExcludeAreReported(t *testing.T) {
	cases := []struct{ src, want string }{
		{"include: notalist\n\na:\n  test.nop: []\n", "include must hold a list"},
		{"include:\n  - 1\n\na:\n  test.nop: []\n", "must be an SLS name"},
		{"exclude: notalist\n\na:\n  test.nop: []\n", "exclude must hold a list"},
		{"exclude:\n  - nope: x\n\na:\n  test.nop: []\n", "`- sls: name` or `- id: name`"},
		{"extend: notamapping\n\na:\n  test.nop: []\n", "extend must hold a mapping"},
	}
	for _, c := range cases {
		out := compile(t, map[string]string{"base|web": c.src}, "web")
		if !strings.Contains(errText(out), c.want) {
			t.Errorf("%q should report %q, got:\n%s", c.src, c.want, errText(out))
		}
	}
}

func TestMalformedDeclarationsAreReported(t *testing.T) {
	cases := []struct{ src, want string }{
		// A bare string under an ID is Salt's short declaration, so this
		// is now read as a module with no function rather than as the
		// wrong type. Salt refuses it too, saying "The type a in
		// notamapping is not formatted as a dictionary".
		{"a: notamapping\n", "names a module with no function"},
		{"a: 3\n", "must hold a mapping of module.function"},
		{"a:\n  pkg: notalist\n", "must be a list or a mapping"},
		{"a:\n  pkg:\n    - installed\n    - 1\n", "must be `- name: value`"},
		{"a:\n  pkg: []\n", "names a module with no function"},
		{"a: {}\n", "declares no module.function"},
		{"a:\n  pkg.installed:\n    - name: nginx\n    - name: other\n", "is given twice"},
		{"a:\n  file.managed:\n    - name: 1\n", "name must be a string"},
		{"a:\n  pkg.installed:\n    - names: notalist\n", "names must hold a list"},
	}
	for _, c := range cases {
		out := compile(t, map[string]string{"base|web": c.src}, "web")
		if !strings.Contains(errText(out), c.want) {
			t.Errorf("%q should report %q, got:\n%s", c.src, c.want, errText(out))
		}
	}
}

func TestRequisiteSpellingVariants(t *testing.T) {
	// A single requisite written without a list.
	files := map[string]string{
		"base|web": `
target:
  test.nop: []

single:
  test.nop:
    - require: target

single_mapping:
  test.nop:
    - require:
        test: target
`,
	}
	out := mustCompile(t, files, "web")
	for _, ch := range out.Low {
		if !strings.HasPrefix(ch.ID, "single") {
			continue
		}
		if len(ch.Reqs) != 1 || len(ch.Reqs[0].Resolved) != 1 {
			t.Errorf("%s did not resolve its requisite: %+v", ch.ID, ch.Reqs)
		}
	}
}

func TestMalformedRequisitesAreReported(t *testing.T) {
	cases := []struct{ src, want string }{
		{"a:\n  test.nop:\n    - require: 1\n", "must hold a list of requisites"},
		{"a:\n  test.nop:\n    - require:\n      - a: b\n        c: d\n", "exactly one target"},
		{"a:\n  test.nop:\n    - require:\n      - test: 1\n", "must be a string"},
		{"a:\n  test.nop:\n    - require:\n      - 1\n", "must be a state ID"},
	}
	for _, c := range cases {
		out := compile(t, map[string]string{"base|web": c.src}, "web")
		if !strings.Contains(errText(out), c.want) {
			t.Errorf("%q should report %q, got:\n%s", c.src, c.want, errText(out))
		}
	}
}

func TestRequisiteByNameRatherThanID(t *testing.T) {
	// A requisite may name the resolved `name` when the ID is a path and
	// the name was set, which trees do.
	files := map[string]string{
		"base|web": `
conf_file:
  file.managed:
    - name: /etc/app.conf

dependent:
  test.nop:
    - require:
      - file: /etc/app.conf
`,
	}
	out := mustCompile(t, files, "web")
	if got := runOrder(out); got[0] != "conf_file" {
		t.Errorf("run = %v", got)
	}
}

func TestStaleModuleNameOnRequisiteWarnsAndResolves(t *testing.T) {
	// A requisite whose module name is stale but whose ID is unambiguous
	// resolves with a warning, because refusing would break a tree over a
	// rename that changed nothing.
	files := map[string]string{
		"base|web": `
install:
  pkg.installed:
    - name: nginx

dependent:
  test.nop:
    - require:
      - file: install
`,
	}
	out := mustCompile(t, files, "web")
	found := false
	for _, w := range out.Diags.Warnings() {
		if strings.Contains(w.Msg, "matching by ID") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a warning about the stale module name: %v", out.Diags.Warnings())
	}
	if got := runOrder(out); got[0] != "install" {
		t.Errorf("run = %v; it should still have resolved", got)
	}
}

func TestRetryOptionForms(t *testing.T) {
	files := map[string]string{
		"base|web": `
full:
  test.nop:
    - retry:
        attempts: 4
        interval: 15
        splay: 5
        until: false

bare:
  test.nop:
    - retry: true

retry_disabled:
  test.nop:
    - retry: false
`,
	}
	out := mustCompile(t, files, "web")
	byID := map[string]*Chunk{}
	for _, ch := range out.Low {
		byID[ch.ID] = ch
	}
	full := byID["full"].Opts.Retry
	if full == nil || full.Attempts != 4 || full.Interval != 15*time.Second || full.Splay != 5*time.Second || full.Until {
		t.Errorf("full retry = %+v", full)
	}
	bare := byID["bare"].Opts.Retry
	if bare == nil || bare.Attempts != 2 || !bare.Until {
		t.Errorf("bare retry = %+v", bare)
	}
	if byID["retry_disabled"].Opts.Retry != nil {
		t.Errorf("retry: false should leave no retry, got %+v", byID["retry_disabled"].Opts.Retry)
	}
}

func TestMalformedOptionsAreReported(t *testing.T) {
	cases := []struct{ src, want string }{
		{"a:\n  test.nop:\n    - order: sideways\n", "must be an integer, `first`, or `last`"},
		{"a:\n  test.nop:\n    - order: [1]\n", "must be an integer, `first`, or `last`"},
		{"a:\n  test.nop:\n    - retry:\n        attempts: many\n", "attempts must be an integer"},
		{"a:\n  test.nop:\n    - retry:\n        interval: {}\n", "interval must be a duration"},
		{"a:\n  test.nop:\n    - retry:\n        nope: 1\n", "retry has no option"},
		{"a:\n  test.nop:\n    - retry:\n        attempts: 0\n", "at least 1"},
		{"a:\n  test.nop:\n    - timeout: {}\n", "timeout"},
		{"a:\n  test.nop:\n    - creates: [1]\n", "entries must be strings"},
		{"a:\n  test.nop:\n    - creates: {}\n", "must be a string or a list"},
	}
	for _, c := range cases {
		out := compile(t, map[string]string{"base|web": c.src}, "web")
		if !strings.Contains(errText(out), c.want) {
			t.Errorf("%q should report %q, got:\n%s", c.src, c.want, errText(out))
		}
	}
}

func TestOrderAndDurationForms(t *testing.T) {
	files := map[string]string{
		"base|web": `
numeric_string_order:
  test.nop:
    - order: '3'
    - timeout: 30s

seconds:
  test.nop:
    - timeout: 45

float_seconds:
  test.nop:
    - timeout: 1.5
`,
	}
	out := mustCompile(t, files, "web")
	byID := map[string]*Chunk{}
	for _, ch := range out.Low {
		byID[ch.ID] = ch
	}
	if byID["numeric_string_order"].Opts.OrderValue != 3 {
		t.Errorf("order = %d", byID["numeric_string_order"].Opts.OrderValue)
	}
	if byID["numeric_string_order"].Opts.Timeout != 30*time.Second {
		t.Errorf("duration string = %v", byID["numeric_string_order"].Opts.Timeout)
	}
	if byID["seconds"].Opts.Timeout != 45*time.Second {
		t.Errorf("bare seconds = %v", byID["seconds"].Opts.Timeout)
	}
	if byID["float_seconds"].Opts.Timeout != 1500*time.Millisecond {
		t.Errorf("fractional seconds = %v", byID["float_seconds"].Opts.Timeout)
	}
}

func TestUnlessAndOnlyIfForms(t *testing.T) {
	files := map[string]string{
		"base|web": `
string_form:
  test.nop:
    - unless: test -f /etc/done

list_form:
  test.nop:
    - onlyif:
      - test -d /srv
      - test -x /usr/bin/true

structured_form:
  test.nop:
    - unless:
        fun: test.ping
`,
	}
	out := mustCompile(t, files, "web")
	byID := map[string]*Chunk{}
	for _, ch := range out.Low {
		byID[ch.ID] = ch
	}
	if len(byID["string_form"].Opts.Unless) != 1 {
		t.Errorf("string form = %v", byID["string_form"].Opts.Unless)
	}
	if len(byID["list_form"].Opts.OnlyIf) != 2 {
		t.Errorf("list form = %v", byID["list_form"].Opts.OnlyIf)
	}
	if _, ok := byID["structured_form"].Opts.Unless[0].(*value.Map); !ok {
		t.Errorf("structured form = %#v", byID["structured_form"].Opts.Unless[0])
	}
}

func TestTopFileMatchAliases(t *testing.T) {
	for alias, expr := range map[string]string{
		"glob":       "web*",
		"list":       "web1.prod,other",
		"pcre":       "^web[0-9]",
		"grain":      "os_family:Debian",
		"grain_pcre": "os_family:^Deb",
		"ipcidr":     "10.0.0.0/8",
		"compound":   "web* and G@os_family:Debian",
	} {
		files := map[string]string{
			"base|top":     "base:\n  '" + expr + "':\n    - match: " + alias + "\n    - matched\n",
			"base|matched": "m:\n  test.nop: []\n",
		}
		out := compileWith(t, files, Config{
			NodeID: "web1.prod",
			Grains: value.MapOf("os_family", "Debian", "ipv4", []any{"10.0.1.15"}),
		})
		if err := out.Err(); err != nil {
			t.Errorf("%s: %v", alias, err)
			continue
		}
		if len(out.Low) != 1 {
			t.Errorf("%s with %q matched nothing", alias, expr)
		}
	}
}

func TestTopFileRejections(t *testing.T) {
	cases := []struct {
		files map[string]string
		want  string
	}{
		{map[string]string{"base|top": "- notamapping\n"}, "must hold a mapping of environments"},
		{map[string]string{"base|top": "base: notamapping\n"}, "must hold a mapping of target expressions"},
		{map[string]string{"base|top": "base:\n  '*': notalist\n"}, "must hold a list of SLS names"},
		{map[string]string{"base|top": "base:\n  '*':\n    - 1\n"}, "must be a string"},
		{map[string]string{"base|top": "base:\n  '*':\n    - match: nosuchtype\n"}, "unknown match type"},
		{map[string]string{"base|top": "base:\n  'E@(?=x)':\n    - a\n"}, "lookahead"},
	}
	for _, c := range cases {
		out := compileWith(t, c.files, Config{NodeID: "web1.prod"})
		if !strings.Contains(errText(out), c.want) {
			t.Errorf("expected %q, got:\n%s", c.want, errText(out))
		}
	}
}

func TestTopMergeStrategies(t *testing.T) {
	files := map[string]string{
		"base|top":       "base:\n  '*':\n    - from_base\nprod:\n  '*':\n    - from_base_for_prod\n",
		"prod|top":       "prod:\n  '*':\n    - from_prod\n",
		"base|from_base": "a:\n  test.nop: []\n",
		// An SLS declared for an environment is fetched from that
		// environment, not from the one whose top file named it.
		"prod|from_base_for_prod": "b:\n  test.nop: []\n",
		"prod|from_prod":          "c:\n  test.nop: []\n",
	}
	build := func(strategy string) *Compiled {
		c := &Compiler{
			Loader:   newLoader(files, "base", "prod"),
			Registry: testRegistry(),
			Config: Config{
				NodeID: "n", Grains: value.NewMap(0),
				TopMergeStrategy: strategy,
			},
		}
		return c.CompileHighstate()
	}

	// merge_all takes every environment's declarations.
	out := build("merge_all")
	if err := out.Err(); err != nil {
		t.Fatal(err)
	}
	if len(runOrder(out)) != 3 {
		t.Errorf("merge_all = %v, want all three", runOrder(out))
	}

	// same refuses a cross-environment declaration, because under it that
	// declaration can never take effect.
	out = build("same")
	if !strings.Contains(errText(out), "may not declare states for") {
		t.Errorf("same should refuse the cross-declaration:\n%s", errText(out))
	}
}

func TestPickExtendTargetWithSeveralModules(t *testing.T) {
	// An unqualified `_in` attaches to the first module under the ID,
	// which is what Salt does.
	files := map[string]string{
		"base|web": `
multi:
  pkg.installed:
    - name: nginx
  service.running:
    - name: nginx

dependent:
  test.nop:
    - require_in:
      - multi
`,
	}
	out := mustCompile(t, files, "web")
	var pkgChunk *Chunk
	for _, ch := range out.Low {
		if ch.ID == "multi" && ch.State == "pkg" {
			pkgChunk = ch
		}
	}
	if pkgChunk == nil || len(pkgChunk.Reqs) != 1 {
		t.Fatalf("the requisite did not attach to the first module: %+v", pkgChunk)
	}
}

func TestInverseRequisiteRejections(t *testing.T) {
	cases := []struct{ src, want string }{
		{"a:\n  test.nop:\n    - require_in:\n      - sls: other\n", "cannot name an sls"},
		{"a:\n  test.nop:\n    - require_in:\n      - nope\n", "not declared in this run"},
		{"a:\n  test.nop:\n    - require_in:\n      - pkg: a\n", "does not declare pkg"},
	}
	for _, c := range cases {
		out := compile(t, map[string]string{"base|web": c.src}, "web")
		if !strings.Contains(errText(out), c.want) {
			t.Errorf("%q should report %q, got:\n%s", c.src, c.want, errText(out))
		}
	}
}

func TestExcludeWholeSLSAndUnknownID(t *testing.T) {
	// Excluding an ID that is not there is not an error: a tree that
	// excludes defensively should not break when the target moves.
	files := map[string]string{
		"base|web": "exclude:\n  - id: never_existed\n\na:\n  test.nop: []\n",
	}
	out := mustCompile(t, files, "web")
	if len(out.Low) != 1 {
		t.Errorf("run = %v", runOrder(out))
	}
}

func TestNoRegistryMeansNoValidation(t *testing.T) {
	// `lint` and the migration report compile without a module registry;
	// the structure is still checked, the module names are not.
	c := &Compiler{
		Loader: newLoader(map[string]string{"base|web": "a:\n  nosuchmodule.thing: []\n"}),
		Config: Config{NodeID: "n", Grains: value.NewMap(0)},
	}
	out := c.CompileSLS([]string{"web"})
	if err := out.Err(); err != nil {
		t.Errorf("without a registry the module name should not be judged: %v", err)
	}
}

func TestStateAllowlistPermitsWhatItNames(t *testing.T) {
	files := map[string]string{
		"base|web": "w:\n  test.nop: []\n",
		"base|db":  "d:\n  test.nop: []\n",
	}
	out := compileWith(t, files, Config{NodeID: "n", StateAllowlist: []string{"web*"}}, "web")
	if err := out.Err(); err != nil {
		t.Errorf("the allowlist should permit web: %v", err)
	}
	out = compileWith(t, files, Config{NodeID: "n", StateAllowlist: []string{"web*"}}, "db")
	if !strings.Contains(errText(out), "excluded by state_allowlist") {
		t.Errorf("the allowlist should refuse db:\n%s", errText(out))
	}
}

func TestMissingSLSNamesTheEnvironment(t *testing.T) {
	out := compile(t, map[string]string{}, "nope")
	if !strings.Contains(errText(out), `sls "nope" was not found in environment "base"`) {
		t.Errorf("error = %s", errText(out))
	}
}

// TestResolvedIndicesPointIntoTheOrderedLowState is the invariant the
// runner depends on and that a bug once broke silently.
//
// Requisites are resolved before ordering, so the indices resolution
// produces are positions in the declaration-ordered slice. The runner
// indexes its results by run position. Those agree only while ordering
// moves nothing, so the compiler remaps them; without the remap, prereq —
// the one requisite that puts a chunk before its target — read the wrong
// result and inverted its own decision.
func TestResolvedIndicesPointIntoTheOrderedLowState(t *testing.T) {
	files := map[string]string{
		"base|web": `
declared_first:
  test.nop:
    - require:
      - test: declared_last

middle:
  test.nop: []

declared_last:
  test.nop: []
`,
	}
	out := mustCompile(t, files, "web")

	// The requisite moved the run away from declaration order: the state
	// declared first now runs last, so the resolved indices cannot be the
	// declaration ones.
	got := runOrder(out)
	want := []string{"middle", "declared_last", "declared_first"}
	if !equal(got, want) {
		t.Fatalf("run = %v, want %v", got, want)
	}
	for _, ch := range out.Low {
		for _, req := range ch.Reqs {
			for _, idx := range req.Resolved {
				if idx < 0 || idx >= len(out.Low) {
					t.Fatalf("%s: resolved index %d is outside the low state of %d", ch.ID, idx, len(out.Low))
				}
				target := out.Low[idx]
				// The index must land on a chunk the requisite actually
				// names.
				named := false
				for _, ref := range req.Refs {
					if ref.ID == target.ID || (ref.SLS != "" && ref.SLS == target.SLS) {
						named = true
					}
				}
				if !named {
					t.Errorf("%s: %s resolved to %s, which it does not name",
						ch.ID, req.Kind, target.ID)
				}
			}
		}
	}
}

func TestPrereqInvertsTheRunOrder(t *testing.T) {
	files := map[string]string{
		"base|web": `
target:
  pkg.installed:
    - name: nginx

prep:
  test.nop:
    - prereq:
      - pkg: target
`,
	}
	out := mustCompile(t, files, "web")
	if got := runOrder(out); got[0] != "prep" || got[1] != "target" {
		t.Fatalf("run = %v; a prereq runs before its target", got)
	}
	// And its resolved index still points at the target in the ordered
	// slice.
	prep := out.Low[0]
	if len(prep.Reqs) != 1 || len(prep.Reqs[0].Resolved) != 1 {
		t.Fatalf("prep requisites = %+v", prep.Reqs)
	}
	if out.Low[prep.Reqs[0].Resolved[0]].ID != "target" {
		t.Errorf("resolved index points at %s", out.Low[prep.Reqs[0].Resolved[0]].ID)
	}
}

func TestOneRequisiteArgumentHoldsEveryReference(t *testing.T) {
	// `_any` and `_all` are statements about the whole list, so a list
	// must stay one requisite.
	files := map[string]string{
		"base|web": `
a:
  test.nop: []
b:
  test.nop: []

dependent:
  test.nop:
    - onchanges:
      - test: a
      - test: b
`,
	}
	out := mustCompile(t, files, "web")
	var dep *Chunk
	for _, ch := range out.Low {
		if ch.ID == "dependent" {
			dep = ch
		}
	}
	if len(dep.Reqs) != 1 {
		t.Fatalf("requisites = %d, want one onchanges argument", len(dep.Reqs))
	}
	if len(dep.Reqs[0].Refs) != 2 || len(dep.Reqs[0].Resolved) != 2 {
		t.Errorf("refs=%d resolved=%d, want both", len(dep.Reqs[0].Refs), len(dep.Reqs[0].Resolved))
	}
	if !strings.Contains(dep.Reqs[0].Describe(), "a") || !strings.Contains(dep.Reqs[0].Describe(), "b") {
		t.Errorf("Describe = %q", dep.Reqs[0].Describe())
	}
}

func TestOrderFirstIsAccepted(t *testing.T) {
	// Salt gives `order: first` the order 0, ahead of every unnumbered
	// state. Refusing it stopped a tree that Salt compiles, which the
	// differential gate found.
	compiled := mustCompile(t, map[string]string{"base|web": `
last_one:
  test.nop:
    - order: last

plain:
  test.nop: []

first_one:
  test.nop:
    - order: first
`}, "web")
	got := runOrder(compiled)
	want := []string{"first_one", "plain", "last_one"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestShortDeclarationForm(t *testing.T) {
	// `apache24:\n  pkg.latest` is Salt's short declaration: the
	// function with no arguments. It is how a tree spells "just install
	// it", and it appeared in four files of the first real tree.
	compiled := mustCompile(t, map[string]string{"base|web": `
short_form:
  test.nop

long_form:
  test.nop: []
`}, "web")
	if len(compiled.Low) != 2 {
		t.Fatalf("got %d chunks", len(compiled.Low))
	}
	short, long := compiled.Low[0], compiled.Low[1]
	if short.State != long.State || short.Fun != long.Fun {
		t.Errorf("the short form gave %s.%s, the long form %s.%s",
			short.State, short.Fun, long.State, long.Fun)
	}
	// The name defaults to the ID in both, as it does in Salt.
	if short.Name != "short_form" {
		t.Errorf("name = %q, want the state ID", short.Name)
	}
}

func TestIgnoreMissingIsAcceptedInAStateTop(t *testing.T) {
	// Salt honours the directive only in a pillar top, but it accepts it
	// in a state top rather than reading it as an SLS name. Reporting it
	// stopped a tree Salt compiles.
	compiled := mustCompile(t, map[string]string{
		"base|top": "base:\n  '*':\n    - web\n    - ignore_missing: True\n",
		"base|web": "a:\n  test.nop: []\n",
	})
	if len(compiled.Low) != 1 {
		t.Fatalf("got %d chunks: %v", len(compiled.Low), runOrder(compiled))
	}
}

func TestAnIneffectiveArgumentWarnsRatherThanFailing(t *testing.T) {
	// Salt has arguments that mean nothing against a different
	// implementation. Refusing one stops a tree Salt runs; accepting it
	// silently is the accept-but-ignore defect this project keeps
	// finding in itself. Declaring it warns once, at the line that wrote
	// it.
	compiled := compile(t, map[string]string{"base|web": `
a:
  test.ineffective:
    - pointless: 1
`}, "web")
	if err := compiled.Err(); err != nil {
		t.Fatalf("an ineffective argument should not stop compilation: %v", err)
	}

	var warned bool
	for _, d := range compiled.Diags.Warnings() {
		if strings.Contains(d.Msg, "has no effect here") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("no warning was reported: %v", compiled.Diags.Warnings())
	}

	// A state that does not use it says nothing.
	quiet := compile(t, map[string]string{"base|web": "a:\n  test.ineffective: []\n"}, "web")
	for _, d := range quiet.Diags {
		if strings.Contains(d.Msg, "has no effect here") {
			t.Errorf("an unused ineffective argument warned: %v", d.Msg)
		}
	}
}
