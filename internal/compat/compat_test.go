package compat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree writes files under a fresh directory: "path" -> contents.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "_modules/") {
			mode = 0o755 // external modules are run, not read
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func scanTree(t *testing.T, files map[string]string, kind Kind) TreeReport {
	t.Helper()
	root := writeTree(t, files)
	s := &Scanner{Grains: map[string]any{"id": "web1", "os": "FreeBSD", "os_family": "FreeBSD"}}
	report, err := s.ScanTree(root, kind)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	return report
}

func fileCodes(file FileReport) []string {
	var out []string
	for _, f := range file.Findings {
		out = append(out, f.Code)
	}
	return out
}

// codes collects every finding code in a tree report, file findings first.
func codes(tr TreeReport) []string {
	var out []string
	for _, f := range tr.Findings {
		out = append(out, f.Code)
	}
	for _, file := range tr.Files {
		for _, f := range file.Findings {
			out = append(out, f.Code)
		}
	}
	return out
}

func hasCode(tr TreeReport, want string) bool {
	for _, code := range codes(tr) {
		if code == want {
			return true
		}
	}
	return false
}

func findingFor(t *testing.T, tr TreeReport, code string) Finding {
	t.Helper()
	for _, file := range tr.Files {
		for _, f := range file.Findings {
			if f.Code == code {
				return f
			}
		}
	}
	for _, f := range tr.Findings {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no %q finding in %v", code, codes(tr))
	return Finding{}
}

func TestTemplateConstructsAreReported(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"jinja statement", "{% if grains['os'] == 'FreeBSD' %}\nx:\n  file.absent: []\n{% endif %}\n", "jinja-block"},
		{"jinja comment", "{# a note #}\nx:\n  file.absent: []\n", "jinja-comment"},
		{"jinja global", "x:\n  file.managed:\n    - name: {{ grains['host'] }}\n", "jinja-expr"},
		{"jinja getter", "x:\n  file.managed:\n    - name: {{ pillar.get('f', '/tmp/f') }}\n", "jinja-expr"},
		{"jinja filter call", "x:\n  file.managed:\n    - name: {{ .Grains.host | default('h') }}\n", "jinja-filter"},
		{"unknown filter", "x:\n  file.managed:\n    - name: {{ .Grains.host | title }}\n", "jinja-filter"},
		{"python renderer", "#!py\n\ndef run():\n    return {}\n", "renderer"},
		{"gpg renderer", "#!yaml|gpg\nsecret: x\n", "renderer"},
		{"go template error", "x:\n  file.managed:\n    - name: {{ .Grains.host }\n", "template-error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := scanTree(t, map[string]string{"a.sls": tc.body}, KindState)
			if !hasCode(tr, tc.want) {
				t.Fatalf("want %q, got %v", tc.want, codes(tr))
			}
		})
	}
}

func TestUnsupportedYAMLIsReported(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"block scalar", "/etc/motd:\n  file.managed:\n    - contents: |\n        hello\n        there\n", "yaml-block-scalar"},
		{"anchor", "defaults: &def\n  a: b\n", "yaml-anchor"},
		{"merge key", "web:\n  <<: *def\n  port: \"80\"\n", "yaml-merge-key"},
		{"flow list", "web:\n  ports: [80, 443]\n", "yaml-flow"},
		{"multiple documents", "a: b\n---\nc: d\n", "yaml-multi-doc"},
		{"type tag", "a: !!str 3\n", "yaml-tag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := scanTree(t, map[string]string{"a.sls": tc.body}, KindPillar)
			if !hasCode(tr, tc.want) {
				t.Fatalf("want %q, got %v", tc.want, codes(tr))
			}
		})
	}
}

func TestBlockScalarBodyIsNotScannedAsYAML(t *testing.T) {
	tr := scanTree(t, map[string]string{"a.sls": "" +
		"/etc/rc.conf:\n" +
		"  file.managed:\n" +
		"    - contents: |\n" +
		"        ports: [80, 443]\n" +
		"        anchor: &notreally\n",
	}, KindState)
	if got := fileCodes(tr.Files[0]); len(got) != 1 || got[0] != "yaml-block-scalar" {
		t.Fatalf("only the block scalar itself should be reported, got %v", got)
	}
}

func TestUnsupportedStateDeclarationsAreReported(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"short form", "nginx:\n  pkg:\n    - installed\n", "short-declaration"},
		{"unknown module", "venv:\n  virtualenv.managed:\n    - name: /opt/app\n", "unsupported-module"},
		{"unimplemented requisite", "p:\n  pkg.installed:\n    - onfail:\n      - service: s\n", "unsupported-requisite"},
		{"salt uri", "f:\n  file.managed:\n    - source: salt://web/nginx.conf\n", "salt-uri"},
		{"remote source", "f:\n  file.managed:\n    - source: https://example.com/x\n", "remote-source"},
		{"jinja template arg", "f:\n  file.managed:\n    - source: x.conf\n    - template: jinja\n", "template-renderer"},
		{"package files", "p:\n  pkg.installed:\n    - sources:\n      - /tmp/nginx.deb\n", "ignored-argument"},
		{"unknown argument", "p:\n  pkg.installed:\n    - skip_verify: true\n", "ignored-argument"},
		{"extend", "extend:\n  nginx:\n    pkg.installed: []\n", "extend"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := scanTree(t, map[string]string{"a.sls": tc.body}, KindState)
			if !hasCode(tr, tc.want) {
				t.Fatalf("want %q, got %v", tc.want, codes(tr))
			}
		})
	}
}

func TestStatesHaliteImplementsAreNotReported(t *testing.T) {
	tr := scanTree(t, map[string]string{"a.sls": "" +
		"/etc/nginx/conf.d:\n  file.recurse:\n    - source: files/conf.d\n    - clean: true\n" +
		"ed:\n  ssh_auth.present:\n    - user: ed\n    - enc: ssh-ed25519\n" +
		"nginx-upstream:\n  pkgrepo.managed:\n    - url: https://nginx.org/packages/debian\n    - dist: bookworm\n" +
		"nginx:\n  pkg.installed:\n    - version: 1.24.0\n    - hold: true\n" +
		"tools:\n  pkg.installed:\n    - names:\n      - vim\n      - curl\n" +
		"conf:\n  file.managed:\n    - name: /tmp/x\n    - contents: y\n    - require_in:\n      - pkg: nginx\n",
	}, KindState)
	if got := fileCodes(tr.Files[0]); len(got) != 0 {
		t.Fatalf("these states are implemented, got %v", got)
	}
}

func TestShortDeclarationNamesTheDottedForm(t *testing.T) {
	tr := scanTree(t, map[string]string{"a.sls": "nginx:\n  pkg:\n    - installed\n    - name: nginx\n"}, KindState)
	if hint := findingFor(t, tr, "short-declaration").Hint; !strings.Contains(hint, "pkg.installed") {
		t.Fatalf("hint should name pkg.installed, got %q", hint)
	}
}

func TestUnsupportedArgumentIsOnlyReportedForKnownModules(t *testing.T) {
	tr := scanTree(t, map[string]string{
		"_modules/nginx": "#!/bin/sh\n",
		"a.sls":          "site:\n  nginx.vhost:\n    - server_name: example.com\n",
	}, KindState)
	if got := fileCodes(tr.Files[0]); len(got) != 0 {
		t.Fatalf("an external module takes any argument, got %v", got)
	}
	if uses := tr.Files[0].Uses; len(uses) != 1 || !uses[0].External {
		t.Fatalf("nginx.vhost should resolve to the external module, got %+v", uses)
	}
}

func TestTopFileTargetsAndNames(t *testing.T) {
	tr := scanTree(t, map[string]string{
		"top.sls": "" +
			"base:\n" +
			"  '*':\n" +
			"    - common\n" +
			"  'I@role:web':\n" +
			"    - web\n" +
			"  'web*':\n" +
			"    - gone\n" +
			"dev:\n" +
			"  '*':\n" +
			"    - common\n",
		"common.sls": "p:\n  pkg.installed:\n    - name: vim\n",
		"web.sls":    "p2:\n  pkg.installed:\n    - name: curl\n",
	}, KindState)
	for _, want := range []string{"unsupported-target", "missing-sls", "top-environment"} {
		if !hasCode(tr, want) {
			t.Fatalf("want %q, got %v", want, codes(tr))
		}
	}
}

func TestTopFileRecordsWhatMatchesTheHost(t *testing.T) {
	tr := scanTree(t, map[string]string{
		"top.sls":    "base:\n  'os_family:FreeBSD':\n    - common\n",
		"common.sls": "p:\n  pkg.installed:\n    - name: vim\n",
	}, KindState)
	for _, file := range tr.Files {
		if file.Path == "top.sls" {
			if len(file.Matched) != 1 || file.Matched[0] != "common" {
				t.Fatalf("top.sls should match common, got %v", file.Matched)
			}
			return
		}
	}
	t.Fatal("no report for top.sls")
}

func TestIncludesMustResolveUnderTheRoot(t *testing.T) {
	tr := scanTree(t, map[string]string{
		"web/init.sls": "include:\n  - common\n  - gone\n  - .sibling\n",
		"common.sls":   "p:\n  pkg.installed:\n    - name: vim\n",
	}, KindState)
	for _, want := range []string{"missing-include", "relative-include"} {
		if !hasCode(tr, want) {
			t.Fatalf("want %q, got %v", want, codes(tr))
		}
	}
}

func TestSaltPythonExtensionsAreReported(t *testing.T) {
	tr := scanTree(t, map[string]string{
		"_states/custom.py": "def run():\n    pass\n",
		"_modules/thing.py": "def run():\n    pass\n",
		"files/app.py":      "print('payload, not an extension')\n",
		"web/nginx.conf.j2": "server_name {{ x }};\n",
		"top.sls":           "base:\n  '*': []\n",
	}, KindState)
	for _, want := range []string{"salt-extension-dir", "python-module", "jinja-template-file"} {
		if !hasCode(tr, want) {
			t.Fatalf("want %q, got %v", want, codes(tr))
		}
	}
	for _, f := range tr.Findings {
		if strings.HasPrefix(f.File, "files"+string(filepath.Separator)) {
			t.Fatalf("payload files are not extensions: %+v", f)
		}
	}
}

func TestJinjaFileStillYieldsItsStateInventory(t *testing.T) {
	tr := scanTree(t, map[string]string{"a.sls": "" +
		"{% set pkgs = ['vim'] %}\n" +
		"{% for p in pkgs %}\n" +
		"{{ p }}:\n" +
		"  pkg.installed:\n" +
		"    - name: {{ p }}\n" +
		"{% endfor %}\n" +
		"l:\n" +
		"  file.symlink:\n" +
		"    - target: /srv\n",
	}, KindState)
	file := tr.Files[0]
	if !file.Approximate {
		t.Fatal("a file that does not render should be marked approximate")
	}
	var names []string
	for _, u := range file.Uses {
		names = append(names, u.Name)
	}
	if len(names) != 2 || names[1] != "file.symlink" {
		t.Fatalf("want pkg.installed and file.symlink, got %v", names)
	}
}

func TestConditionalBranchesDeclaringOneStateStillParse(t *testing.T) {
	tr := scanTree(t, map[string]string{"a.sls": "" +
		"{% if grains['os_family'] == 'Debian' %}\n" +
		"nginx_conf:\n" +
		"  file.managed:\n" +
		"    - name: /etc/nginx/nginx.conf\n" +
		"{% else %}\n" +
		"nginx_conf:\n" +
		"  file.symlink:\n" +
		"    - name: /usr/local/etc/nginx/nginx.conf\n" +
		"{% endif %}\n",
	}, KindState)
	// yamlite rejects the duplicate ID the stripped conditional leaves
	// behind, so the first block stands for both.
	uses := tr.Files[0].Uses
	if len(uses) != 1 || uses[0].Name != "file.managed" {
		t.Fatalf("want the first branch's state, got %+v", uses)
	}
}

func TestSupportedTreeHasNoFindings(t *testing.T) {
	s := &Scanner{Grains: map[string]any{"id": "web1", "os": "FreeBSD", "os_family": "FreeBSD"}}
	for _, tc := range []struct {
		root string
		kind Kind
	}{
		{"../../examples/tree", KindState},
		{"../../examples/pillar", KindPillar},
	} {
		tr, err := s.ScanTree(tc.root, tc.kind)
		if err != nil {
			t.Fatalf("ScanTree(%s): %v", tc.root, err)
		}
		if got := codes(tr); len(got) != 0 {
			t.Fatalf("%s should be clean, got %v", tc.root, got)
		}
		if len(tr.Files) == 0 {
			t.Fatalf("%s: no files scanned", tc.root)
		}
	}
}

func TestReportTotalsAndModuleUsage(t *testing.T) {
	tr := scanTree(t, map[string]string{
		"a.sls": "p:\n  pkg.installed:\n    - name: vim\nvenv:\n  virtualenv.managed:\n    - name: /opt/app\n",
		"b.sls": "p2:\n  pkg.installed:\n    - name: curl\n",
	}, KindState)
	report := Report{Trees: []TreeReport{tr}}

	totals := report.Totals()
	if totals.Files != 2 || totals.WithFinding != 1 {
		t.Fatalf("unexpected totals: %+v", totals)
	}
	if totals.Errors != 1 {
		t.Fatalf("want one error (virtualenv.managed) plus a missing top warning, got %+v", totals)
	}

	usage := report.ModuleUsage()
	if usage[0].Name != "virtualenv.managed" || usage[0].Supported {
		t.Fatalf("unsupported modules come first, got %+v", usage)
	}
	if usage[1].Name != "pkg.installed" || usage[1].Count != 2 {
		t.Fatalf("want pkg.installed used twice, got %+v", usage)
	}
}

func TestMissingTopFileIsAWarning(t *testing.T) {
	tr := scanTree(t, map[string]string{"a.sls": "p:\n  pkg.installed:\n    - name: vim\n"}, KindState)
	if !hasCode(tr, "no-top") {
		t.Fatalf("want no-top, got %v", codes(tr))
	}
}

func TestPillarTreeIsReadAsData(t *testing.T) {
	tr := scanTree(t, map[string]string{
		"top.sls":    "base:\n  '*':\n    - common\n",
		"common.sls": "nginx:\n  port: \"8080\"\nadmins:\n  - ed\n",
	}, KindPillar)
	if got := codes(tr); len(got) != 0 {
		t.Fatalf("a plain pillar tree should be clean, got %v", got)
	}
}

func TestMissingRootIsAnError(t *testing.T) {
	s := &Scanner{}
	if _, err := s.ScanTree(filepath.Join(t.TempDir(), "nope"), KindState); err == nil {
		t.Fatal("a missing root should be reported to the caller")
	}
}
