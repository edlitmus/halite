package rendersandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/edlitmus/halite/internal/render"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// The sandbox is tested through a real child process, because the thing
// under test *is* the process boundary. A fake that called Serve in a
// goroutine would exercise the protocol and prove nothing about the
// arrangement: that the render happens somewhere else, that a value
// survives the crossing intact, and that the callbacks come back.
//
// The child is this test binary. `start` gives the child an environment
// holding exactly one variable, so TestMain has everything it needs to
// know which half of the test it is.

func TestMain(m *testing.M) {
	if os.Getenv("HALITE_RENDER_SANDBOX") == "1" {
		if err := Serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// newTestSandbox returns a sandbox whose child is this test binary.
func newTestSandbox(t *testing.T) *Sandbox {
	t.Helper()
	s := New(Config{Exe: os.Args[0], Timeout: 30 * time.Second, Stderr: os.Stderr})
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestARenderHappensInAnotherProcess(t *testing.T) {
	s := newTestSandbox(t)

	src := []byte("{% set who = 'world' %}\ngreeting: hello {{ who }}\ncount: 3\n")
	res, err := s.Render(src, render.Options{File: "greet.sls", SLS: "greet"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if s.PID() == 0 {
		t.Fatal("no child process is running after a render")
	}
	if s.PID() == os.Getpid() {
		t.Fatal("the render sandbox rendered in this process, which is the one thing it exists not to do")
	}

	m, ok := res.Value.(*value.Map)
	if !ok {
		t.Fatalf("the result is a %T, not a mapping", res.Value)
	}
	if got, _ := m.Get("greeting"); got != "hello world" {
		t.Errorf("greeting is %#v; the template did not render", got)
	}
	if got, _ := m.Get("count"); got != int64(3) {
		t.Errorf("count is %#v (%T), want the integer 3", got, got)
	}
}

// The compiler's diagnostics anchor on positions, so a boundary that
// dropped them would turn "line 3 of web.sls" into "somewhere in
// web.sls" for every error the state compiler reports.
func TestPositionsSurviveTheCrossing(t *testing.T) {
	s := newTestSandbox(t)
	src := []byte("first: 1\nsecond:\n  inner: 2\n")

	sandboxed, err := s.Render(src, render.Options{File: "pos.sls", SLS: "pos"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	direct, err := render.Render(src, render.Options{File: "pos.sls", SLS: "pos"})
	if err != nil {
		t.Fatalf("in-process render: %v", err)
	}

	want := positionsOf(t, direct.Value)
	got := positionsOf(t, sandboxed.Value)
	if len(want) == 0 {
		t.Fatal("the in-process render produced no positions, so this test is checking nothing")
	}
	if len(got) != len(want) {
		t.Fatalf("the sandbox returned %d positions and the in-process render %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d is %s, want %s", i, got[i], want[i])
		}
	}
}

// positionsOf walks a value and collects every position it carries, in
// order, as text.
func positionsOf(t *testing.T, v any) []string {
	t.Helper()
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch n := v.(type) {
		case *value.Map:
			out = append(out, "map@"+n.Pos.String())
			for _, e := range n.Entries() {
				out = append(out, value.KeyString(e.Key)+" key@"+e.KeyPos.String()+" val@"+e.ValPos.String())
				walk(e.Val)
			}
		case []any:
			for _, item := range n {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

// recordingDispatcher is the parent's execution module registry, stood
// in for. What matters is that the call arrives here at all: the child
// has no registry and no privilege, so a template calling a module is
// the parent doing the work.
type recordingDispatcher struct {
	calls []string
	ret   any
}

func (d *recordingDispatcher) CallModule(name string, args []any, kwargs map[string]any) (any, error) {
	d.calls = append(d.calls, fmt.Sprintf("%s(%v,%v)", name, args, kwargs))
	if name == "test.fail" {
		return nil, fmt.Errorf("this module always fails")
	}
	return d.ret, nil
}

func (d *recordingDispatcher) HasModule(name string) bool { return name != "no.such" }

func TestTheChildDispatchesModulesThroughTheParent(t *testing.T) {
	s := newTestSandbox(t)
	d := &recordingDispatcher{ret: "1.2.3"}

	src := []byte("version: {{ salt['pkg.version']('nginx') }}\n" +
		"known: {{ salt['pkg.version'] is defined }}\n" +
		"missing: {{ salt['no.such'] is defined }}\n")
	res, err := s.Render(src, render.Options{File: "call.sls", Salt: d})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if len(d.calls) != 1 {
		t.Fatalf("the parent saw %d dispatches, want 1: %v", len(d.calls), d.calls)
	}
	if !strings.HasPrefix(d.calls[0], "pkg.version([nginx]") {
		t.Errorf("the dispatch arrived as %q", d.calls[0])
	}
	m := res.Value.(*value.Map)
	if got, _ := m.Get("version"); got != "1.2.3" {
		t.Errorf("the return value came back as %#v", got)
	}
	if got, _ := m.Get("known"); got != true {
		t.Errorf("`salt['pkg.version'] is defined` answered %#v", got)
	}

	// `is defined` over an unknown module is compared against the
	// in-process renderer rather than against a constant. What the
	// sandbox owes is the same answer; what the right answer is belongs
	// to the template engine, which currently says true for every name
	// because nothing calls HasModule. That is recorded in plan.md's
	// conformance tail, and this assertion holds either way.
	direct, err := render.Render(src, render.Options{File: "call.sls", Salt: &recordingDispatcher{ret: "1.2.3"}})
	if err != nil {
		t.Fatalf("in-process render: %v", err)
	}
	mine, _ := m.Get("missing")
	theirs, _ := direct.Value.(*value.Map).Get("missing")
	if mine != theirs {
		t.Errorf("`salt['no.such'] is defined` answered %#v in the sandbox and %#v in this process", mine, theirs)
	}
}

func TestAFailingModuleFailsTheRenderWithItsOwnMessage(t *testing.T) {
	s := newTestSandbox(t)
	d := &recordingDispatcher{}

	src := []byte("x: {{ salt['test.fail']() }}\n")
	_, err := s.Render(src, render.Options{File: "fail.sls", Salt: d})
	if err == nil {
		t.Fatal("a module that failed rendered successfully")
	}

	// Compared with the in-process message rather than searched for a
	// substring. A `Contains` check passed for a week against an error
	// that read "fail.sls: render failed: fail.sls:1:6: ...", because
	// the sandbox had wrapped it and the compiler prefixes a diagnostic
	// with the file unless the message already names it. A lab run
	// found that and this assertion is what would have.
	_, direct := render.Render(src, render.Options{File: "fail.sls", Salt: &recordingDispatcher{}})
	if direct == nil {
		t.Fatal("the in-process render of the same document succeeded")
	}
	if err.Error() != direct.Error() {
		t.Errorf("the sandboxed error and the in-process one differ:\n  sandboxed:  %v\n  in-process: %v", err, direct)
	}
}

// mapLoader is the parent's template loader.
type mapLoader struct {
	files map[string]string
	asked []string
}

func (l *mapLoader) Load(name string) (string, string, error) {
	l.asked = append(l.asked, name)
	if src, ok := l.files[name]; ok {
		return src, name, nil
	}
	return "", "", template.ErrNotFound
}

func TestTheChildLoadsTemplatesThroughTheParent(t *testing.T) {
	s := newTestSandbox(t)
	l := &mapLoader{files: map[string]string{
		"macros.jinja": "{% macro greet(who) %}hello {{ who }}{% endmacro %}",
	}}

	src := []byte("{% from 'macros.jinja' import greet %}\ngreeting: {{ greet('world') }}\n")
	res, err := s.Render(src, render.Options{File: "inc.sls", Loader: l})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(l.asked) != 1 || l.asked[0] != "macros.jinja" {
		t.Fatalf("the parent was asked for %v", l.asked)
	}
	m := res.Value.(*value.Map)
	if got, _ := m.Get("greeting"); got != "hello world" {
		t.Errorf("the imported macro produced %#v", got)
	}
}

// A missing template is not a failing one: `ignore missing` turns on
// exactly that difference, and it has to survive the boundary.
func TestAMissingTemplateIsStillMissingAcrossTheBoundary(t *testing.T) {
	s := newTestSandbox(t)
	l := &mapLoader{files: map[string]string{}}

	res, err := s.Render([]byte("{% include 'nope.jinja' ignore missing %}\nk: 1\n"),
		render.Options{File: "opt.sls", Loader: l})
	if err != nil {
		t.Fatalf("an ignorable missing include failed the render: %v", err)
	}
	m := res.Value.(*value.Map)
	if got, _ := m.Get("k"); got != int64(1) {
		t.Errorf("the document after the include is %#v", got)
	}
}

func TestAPermissiveUndefinedIsReportedToTheParent(t *testing.T) {
	s := newTestSandbox(t)
	var seen []string

	opts := render.Options{
		File:      "undef.sls",
		Undefined: template.Permissive,
		OnUndefined: func(name string, pos template.Pos) {
			seen = append(seen, fmt.Sprintf("%s at %s", name, pos))
		},
	}
	if _, err := s.Render([]byte("k: {{ nowhere }}\n"), opts); err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("the parent was told of %d undefined names, want 1: %v", len(seen), seen)
	}
	if !strings.HasPrefix(seen[0], "nowhere at ") {
		t.Errorf("the report reads %q", seen[0])
	}
	if !strings.Contains(seen[0], "undef.sls") {
		t.Errorf("the report has lost its position: %q", seen[0])
	}
}

// A broken document is the child working correctly, so the child stays
// and the next render uses it. Restarting a process per bad SLS file
// would make a tree with one mistake in it cost fifty forks.
func TestABrokenDocumentDoesNotKillTheChild(t *testing.T) {
	s := newTestSandbox(t)

	if _, err := s.Render([]byte("k: {{ 1 +\n"), render.Options{File: "broken.sls"}); err == nil {
		t.Fatal("an unterminated expression rendered successfully")
	}
	first := s.PID()
	if first == 0 {
		t.Fatal("the child went away after a render error")
	}

	res, err := s.Render([]byte("k: 1\n"), render.Options{File: "fine.sls"})
	if err != nil {
		t.Fatalf("the render after a failure: %v", err)
	}
	if s.PID() != first {
		t.Errorf("the child was replaced after a render error: %d then %d", first, s.PID())
	}
	m := res.Value.(*value.Map)
	if got, _ := m.Get("k"); got != int64(1) {
		t.Errorf("the second render produced %#v", got)
	}
}

// The keyring is the parent's. SPEC 25.4 gives the child "read access to
// the cached tree and nothing else", and the gpg settings are the one
// part of render.Options that names something outside it.
func TestTheChildIsNeverToldWhereTheKeyringIs(t *testing.T) {
	opts := render.Options{
		File: "secret.sls",
		GPG:  render.GPGOptions{Binary: "/usr/local/bin/gpg", Home: "/root/.gnupg"},
	}
	wire, err := encodeOptions(opts, newFiles())
	if err != nil {
		t.Fatalf("encoding the options: %v", err)
	}
	body, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"gnupg", "gpg"} {
		if strings.Contains(strings.ToLower(string(body)), secret) {
			t.Errorf("the render request carries %q:\n%s", secret, body)
		}
	}
}

// The split is what keeps decrypted pillar out of the unprivileged
// process, so it is asserted rather than left to the comment that
// explains it.
func TestTheDataStagesStayInTheParent(t *testing.T) {
	for _, tc := range []struct {
		pipeline   string
		head, tail []string
	}{
		{"jinja|yaml", []string{"jinja", "yaml"}, nil},
		{"yaml|gpg", []string{"yaml"}, []string{"gpg"}},
		{"jinja|yaml|gpg", []string{"jinja", "yaml"}, []string{"gpg"}},
		{"jinja", []string{"jinja"}, nil},
	} {
		head, tail := render.SplitPipeline(strings.Split(tc.pipeline, "|"))
		if strings.Join(head, "|") != strings.Join(tc.head, "|") {
			t.Errorf("%s: head is %v, want %v", tc.pipeline, head, tc.head)
		}
		if strings.Join(tail, "|") != strings.Join(tc.tail, "|") {
			t.Errorf("%s: tail is %v, want %v", tc.pipeline, tail, tc.tail)
		}
	}
}

func TestDescribeSaysWhatIsActuallyApplied(t *testing.T) {
	lines := strings.Join(New(Config{}).Describe(), "\n")
	for _, must := range []string{"process boundary", "identity:", "network:"} {
		if !strings.Contains(lines, must) {
			t.Errorf("Describe does not mention %q:\n%s", must, lines)
		}
	}
	// An operator must never read this as more than it is.
	if !strings.Contains(lines, "still runs it in this process") {
		t.Errorf("Describe does not say that a module call is still the parent's:\n%s", lines)
	}
}

// The engine seam has to be the same rendering for the same input,
// otherwise turning the setting on changes what a tree means.
func TestTheSandboxAgreesWithTheInProcessRenderer(t *testing.T) {
	s := newTestSandbox(t)

	docs := []string{
		"a: 1\nb: '2'\nc: [1, 2, 3]\n",
		"{% for i in range(3) %}\nkey{{ i }}: {{ i * 2 }}\n{% endfor %}\n",
		"text: |\n  one\n  two\n",
		"when: 2001-12-14\nmode: 0644\nyes_no: yes\n",
		"#!json\n{\"a\": [1, 2], \"b\": {\"c\": null}}\n",
		"#!text\nnot parsed at all\n",
		"deep:\n  - name: one\n    on: true\n  - name: two\n    on: false\n",
	}
	for _, doc := range docs {
		opts := render.Options{File: "same.sls", SLS: "same", Grains: value.MapOf("os", "FreeBSD")}
		mine, err := s.Render([]byte(doc), opts)
		if err != nil {
			t.Errorf("%q: sandboxed: %v", doc, err)
			continue
		}
		theirs, err := render.Render([]byte(doc), opts)
		if err != nil {
			t.Errorf("%q: in-process: %v", doc, err)
			continue
		}
		if got, want := describeValue(mine.Value), describeValue(theirs.Value); got != want {
			t.Errorf("%q\n  sandboxed:  %s\n  in-process: %s", doc, got, want)
		}
		if mine.Text != theirs.Text {
			t.Errorf("%q: the rendered text differs:\n  %q\n  %q", doc, mine.Text, theirs.Text)
		}
		if strings.Join(mine.Pipeline, "|") != strings.Join(theirs.Pipeline, "|") {
			t.Errorf("%q: pipeline %v vs %v", doc, mine.Pipeline, theirs.Pipeline)
		}
	}
}

// `file.managed` with `template: jinja` renders bytes that came off the
// file server, so it crosses the same boundary. What it must not do is
// read its own first line as a renderer pipeline: the file being managed
// is not an SLS file, and a script starting `#!/bin/sh` would otherwise
// be delivered as something else entirely.
func TestAManagedFileTemplateRendersInTheChild(t *testing.T) {
	s := newTestSandbox(t)
	opts := render.Options{
		File:  "salt://web/run.sh",
		Extra: map[string]any{"who": "world"},
	}

	src := []byte("#!/bin/sh\necho hello {{ who }}\n")
	out, warnings, err := s.Template(src, opts)
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: %v", warnings)
	}
	if out != "#!/bin/sh\necho hello world\n" {
		t.Errorf("the rendered file is %q", out)
	}

	direct, _, err := render.Template(src, opts)
	if err != nil {
		t.Fatalf("in-process template: %v", err)
	}
	if out != direct {
		t.Errorf("the sandbox rendered %q and this process %q", out, direct)
	}
	if s.PID() == os.Getpid() {
		t.Fatal("the file template rendered in this process")
	}
}
