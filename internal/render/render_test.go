package render

import (
	"fmt"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/value"
)

func renderSLS(t *testing.T, src string, opts Options) Result {
	t.Helper()
	if opts.File == "" {
		opts.File = "webserver.sls"
	}
	res, err := Render([]byte(src), opts)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	return res
}

func TestParsePipeline(t *testing.T) {
	cases := []struct {
		src  string
		want []string
		body string
	}{
		{"a: 1\n", []string{"jinja", "yaml"}, "a: 1\n"},
		{"#!jinja|yaml\na: 1\n", []string{"jinja", "yaml"}, "\na: 1\n"},
		{"#!yaml\na: 1\n", []string{"yaml"}, "\na: 1\n"},
		{"#!  jinja | json \nx", []string{"jinja", "json"}, "\nx"},
		{"#!\na: 1\n", []string{"jinja", "yaml"}, "a: 1\n"},
	}
	for _, c := range cases {
		stages, body := ParsePipeline(c.src)
		if strings.Join(stages, "|") != strings.Join(c.want, "|") {
			t.Errorf("%q -> %v, want %v", c.src, stages, c.want)
		}
		if body != c.body {
			t.Errorf("%q body = %q, want %q", c.src, body, c.body)
		}
	}
}

func TestShebangKeepsLineNumbersAligned(t *testing.T) {
	// A diagnostic that is off by one because of the shebang is exactly
	// the paper cut this package exists to remove.
	src := "#!jinja|yaml\nkey: value\nbad: [unterminated\n"
	_, err := Render([]byte(src), Options{File: "t.sls"})
	if err == nil {
		t.Fatal("expected a YAML error")
	}
	if !strings.Contains(err.Error(), ":3") {
		t.Errorf("error should name line 3 of the file on disk, got %v", err)
	}
}

func TestRenderStateFile(t *testing.T) {
	src := `nginx_installed:
  pkg.installed:
    - name: {{ pillar['nginx']['package'] }}

/etc/nginx/nginx.conf:
  file.managed:
    - mode: '0644'
    - context:
        workers: {{ grains['num_cpus'] }}
    - require:
      - pkg: nginx_installed
`
	res := renderSLS(t, src, Options{
		Grains: value.MapOf("num_cpus", int64(4)),
		Pillar: value.MapOf("nginx", value.MapOf("package", "nginx-full")),
	})
	if got, _ := value.Traverse(res.Value, "nginx_installed:pkg.installed:0:name", ":"); got != "nginx-full" {
		t.Errorf("package name = %#v", got)
	}
	if got, _ := value.Traverse(res.Value, "/etc/nginx/nginx.conf:file.managed:1:context:workers", ":"); got != int64(4) {
		t.Errorf("workers = %#v", got)
	}
}

func TestTemplateContextNames(t *testing.T) {
	src := `id: {{ id }}
env: {{ env }}
saltenv: {{ saltenv }}
sls: {{ sls }}
slspath: {{ slspath }}
slscolonpath: {{ slscolonpath }}
dunder_env: {{ __env__ }}
`
	res := renderSLS(t, src, Options{
		File:   "webserver/nginx.sls",
		SLS:    "webserver.nginx",
		Env:    "prod",
		NodeID: "web1.prod",
	})
	m := res.Value.(*value.Map)
	want := map[string]string{
		"id":           "web1.prod",
		"env":          "prod",
		"saltenv":      "prod",
		"sls":          "webserver.nginx",
		"slspath":      "webserver/nginx",
		"slscolonpath": "webserver:nginx",
		"dunder_env":   "prod",
	}
	for k, v := range want {
		got, ok := m.Get(k)
		if !ok || got != v {
			t.Errorf("%s = %#v, want %q", k, got, v)
		}
	}
}

// TestYAMLErrorReportsTheTemplateLine is the headline of SPEC section
// 10.1.4: a YAML error in a heavily templated file must name the line the
// operator wrote, not the line the renderer produced.
func TestYAMLErrorReportsTheTemplateLine(t *testing.T) {
	src := `first: ok
{% for i in range(20) %}
item{{ i }}: value
{% endfor %}
broken: [unterminated
`
	_, err := Render([]byte(src), Options{File: "big.sls"})
	if err == nil {
		t.Fatal("expected a YAML error")
	}
	re, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected a render error carrying both positions, got %T: %v", err, err)
	}
	if re.SourceLine != 5 {
		t.Errorf("source line = %d, want 5 (the line in big.sls)", re.SourceLine)
	}
	if re.RenderedLine <= 5 {
		t.Errorf("rendered line = %d; the loop should have pushed the error further down the output", re.RenderedLine)
	}
	msg := err.Error()
	if !strings.Contains(msg, "big.sls:5") {
		t.Errorf("message should point at big.sls:5, got %q", msg)
	}
	if !strings.Contains(msg, "unterminated") {
		t.Errorf("message should quote the rendered line, got %q", msg)
	}
}

func TestUnsupportedRenderersAreNamed(t *testing.T) {
	for _, stage := range []string{"py", "mako", "pydsl", "yamlex", "stateconf"} {
		_, err := Render([]byte("#!"+stage+"\nx\n"), Options{File: "t.sls"})
		if err == nil {
			t.Errorf("%s should be refused", stage)
			continue
		}
		if !strings.Contains(err.Error(), "not supported") {
			t.Errorf("%s: %v", stage, err)
		}
	}
	_, err := Render([]byte("#!nosuchrenderer\nx\n"), Options{File: "t.sls"})
	if err == nil || !strings.Contains(err.Error(), "unknown renderer") {
		t.Errorf("unknown renderer: %v", err)
	}
}

func TestJSONAndTextPipelines(t *testing.T) {
	res := renderSLS(t, "#!jinja|json\n{\"a\": {{ n }}}", Options{Extra: map[string]any{"n": int64(3)}})
	if got, _ := value.Traverse(res.Value, "a", ":"); got != int64(3) {
		t.Errorf("json pipeline = %#v", got)
	}
	res = renderSLS(t, "#!text\nliteral {{ not_rendered }}\n", Options{})
	if !strings.Contains(res.Value.(string), "{{ not_rendered }}") {
		t.Errorf("text pipeline rendered its input: %q", res.Value)
	}
}

func TestYAML11BooleanWarningsSurface(t *testing.T) {
	res := renderSLS(t, "enabled: yes\nmode: 0644\n", Options{})
	if len(res.Warnings) != 2 {
		t.Fatalf("warnings = %v, want one for the boolean and one for the octal", res.Warnings)
	}
	joined := res.Warnings[0].String() + res.Warnings[1].String()
	if !strings.Contains(joined, "YAML 1.1") || !strings.Contains(joined, "octal") {
		t.Errorf("warnings = %v", res.Warnings)
	}
}

func TestStrictUndefinedIsTheDefault(t *testing.T) {
	_, err := Render([]byte("name: {{ pillar['absent'] }}\n"), Options{File: "t.sls", Pillar: value.NewMap(0)})
	if err == nil {
		t.Fatal("a missing pillar value must be an error by default")
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Errorf("error should name the key: %v", err)
	}
}

func TestDeterministicRandomAcrossRuns(t *testing.T) {
	src := "choice: {{ ['a','b','c','d','e','f'] | random }}\n"
	opts := Options{File: "t.sls", NodeID: "web1.prod", JobID: "20260819T142211123456"}
	a := renderSLS(t, src, opts)
	b := renderSLS(t, src, opts)
	av, _ := value.Traverse(a.Value, "choice", ":")
	bv, _ := value.Traverse(b.Value, "choice", ":")
	if av != bv {
		t.Errorf("a test run and the real run disagreed: %v then %v", av, bv)
	}
}

func TestAStageAfterTheSerializerIsNotDropped(t *testing.T) {
	// A serializer ends the pipeline, so a stage named after one used to
	// be skipped without a word. `#!yaml|gpg` is the common spelling for
	// an encrypted pillar file: it rendered as plain yaml and delivered
	// ciphertext as the value, which a state would then have written
	// somewhere as if it were the secret. gpg is implemented now, so the
	// stages that stand in for it here are the ones still unavailable.
	for _, src := range []string{
		"#!yaml|crypt\nk: v\n",  // bridged, no build ships the bridge
		"#!yaml|mako\nk: v\n",   // unsupported
		"#!yaml|nosuch\nk: v\n", // not a renderer at all
	} {
		if _, err := Render([]byte(src), Options{File: "p.sls"}); err == nil {
			t.Errorf("%q should be refused", src)
		}
	}

	// A template stage after a serializer has nothing to transform, and
	// a data stage before one has nothing either. Both used to be
	// accepted and silently dropped.
	for _, src := range []string{
		"#!yaml|jinja\nk: v\n",
		"#!gpg|yaml\nk: v\n",
		"#!jinja|yaml|json\nk: v\n",
	} {
		if _, err := Render([]byte(src), Options{File: "p.sls"}); err == nil {
			t.Errorf("%q names its stages in an order that cannot run, and should be refused", src)
		}
	}

	// A pipeline this build can run is unaffected.
	res, err := Render([]byte("#!jinja|yaml\nk: {{ 1 + 1 }}\n"), Options{File: "p.sls"})
	if err != nil {
		t.Fatalf("a supported pipeline should render: %v", err)
	}
	if m, ok := res.Value.(*value.Map); !ok {
		t.Errorf("value = %T", res.Value)
	} else if v, _ := m.Get("k"); v != int64(2) {
		t.Errorf("k = %v", v)
	}
}

// TestRandomSeedSurvivesTheJob. SPEC 10.2.4 asks that a `test=True` run
// and the real run that follows give the same answer, and specifies a
// seed derived from the node ID and the job ID. Those are two jobs, so
// that seed guarantees they differ: the mechanism defeats the purpose.
// The seed is the node and the template instead.
func TestRandomSeedSurvivesTheJob(t *testing.T) {
	render := func(file, node, job string) string {
		res, err := Render([]byte("{{ range(100000) | random }}"), Options{
			File: file, NodeID: node, JobID: job,
		})
		if err != nil {
			t.Fatal(err)
		}
		// The default pipeline parses the rendered text, so the value
		// arrives as the number it looks like.
		return fmt.Sprint(res.Value)
	}

	// Two jobs, one node, one template: the same answer, which is the
	// whole point.
	first := render("web.sls", "node1", "20260101T000000.000000")
	second := render("web.sls", "node1", "20260101T000001.000000")
	if first != second {
		t.Errorf("a second job gave %q where the first gave %q; a --test run and the "+
			"real run after it would show a phantom diff", second, first)
	}

	// A different node gets a different answer, which is what `random`
	// per node means.
	if other := render("web.sls", "node2", "20260101T000000.000000"); other == first {
		t.Errorf("two nodes drew the same value %q", other)
	}

	// So does a different template, so two files do not draw in step.
	if other := render("db.sls", "node1", "20260101T000000.000000"); other == first {
		t.Errorf("two templates drew the same value %q", other)
	}

	// And the switch still restores the churn.
	a := func() string {
		res, _ := Render([]byte("{{ range(100000) | random }}"), Options{
			File: "web.sls", NodeID: "node1", Nondeterministic: true,
		})
		return fmt.Sprint(res.Value)
	}
	differed := false
	for i := 0; i < 20 && !differed; i++ {
		if a() != first {
			differed = true
		}
	}
	if !differed {
		t.Error("random_seed: nondeterministic produced the seeded value twenty times running")
	}
}
