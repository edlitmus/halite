package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edlitmus/halite/internal/signature"
)

// writeTree lays out a small but realistic Salt tree: the constructs that
// actually cost time in a migration, one of each.
func writeTree(t *testing.T) (stateRoot, pillarRoot, configFile string) {
	t.Helper()
	root := t.TempDir()
	stateRoot = filepath.Join(root, "salt")
	pillarRoot = filepath.Join(root, "pillar")

	files := map[string]string{
		// A file that compiles as written.
		"webserver/init.sls": `include:
  - webserver.common

nginx_installed:
  pkg.installed:
    - name: {{ pillar['nginx']['package'] }}

/etc/nginx/nginx.conf:
  file.managed:
    - source: salt://webserver/files/nginx.conf
    - mode: '0644'
    - require:
      - pkg: nginx_installed
`,
		// The renderer that requires migration.
		"legacy/build.sls": `#!py

def run():
    return {}
`,
		// A regex construct RE2 cannot express.
		"audit/check.sls": `{% set found = grains['osfullname'] | regex_search('Ubuntu(?= 22)') %}
audit_note:
  test.succeed_without_changes:
    - name: {{ found }}
`,
		// The YAML hazards.
		"hazards/init.sls": `enable_thing: yes
mode_unquoted: 0644
dup_key: one
dup_key: two
`,
		// A module that does not ship.
		"cloudy/init.sls": `{% set zones = salt['boto_ec2.get_zones']() %}
note:
  test.nop:
    - name: {{ zones }}
`,
		// A Python execution module, which cannot be loaded at all.
		"_modules/nginx_helper.py": "def helper():\n    return True\n",
	}
	for rel, body := range files {
		p := filepath.Join(stateRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Pillar targeting on a grain a node controls.
	pillarTop := `base:
  '*':
    - common
  'G@custom_role:database':
    - secrets.database
  'G@os_family:Debian':
    - apt
`
	if err := os.MkdirAll(pillarRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pillarRoot, "top.sls"), []byte(pillarTop), 0o644); err != nil {
		t.Fatal(err)
	}

	// The fixture is a Salt configuration file, so it is deliberately
	// written in Salt's own vocabulary: translating it is the point.
	configFile = filepath.Join(root, "salt-node.conf")
	cfg := strings.Join([]string{
		"master: salt.example", // lexicon:allow
		"id: web1.prod",
		"state_whitelist:", // lexicon:allow
		"  - webserver.*",
		"auto_accept: True",
		"obsolete_setting: 1",
		"",
	}, "\n")
	if err := os.WriteFile(configFile, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return stateRoot, pillarRoot, configFile
}

func runAudit(t *testing.T) *Report {
	t.Helper()
	stateRoot, pillarRoot, cfg := writeTree(t)
	reg := signature.NewRegistry()
	reg.Add(
		signature.Signature{Module: "pkg", Function: "installed"},
		signature.Signature{Module: "file", Function: "managed"},
		signature.Signature{Module: "test", Function: "nop"},
	)
	rep, err := Run(Options{
		Root:        stateRoot,
		PillarRoot:  pillarRoot,
		ConfigFiles: []string{cfg},
		Registry:    reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// findingsFor returns the findings of one category.
func findingsFor(rep *Report, cat Category) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		if f.Category == cat {
			out = append(out, f)
		}
	}
	return out
}

func TestReportCountsFiles(t *testing.T) {
	rep := runAudit(t)
	if rep.SLSFiles != 5 {
		t.Errorf("state files = %d, want 5", rep.SLSFiles)
	}
	if rep.PillarFiles != 1 {
		t.Errorf("pillar files = %d, want 1", rep.PillarFiles)
	}
}

func TestRendererInventoryNamesUnsupportedFiles(t *testing.T) {
	rep := runAudit(t)
	if rep.Renderers["jinja|yaml"] < 4 {
		t.Errorf("renderer inventory = %v", rep.Renderers)
	}
	found := findingsFor(rep, CatRenderer)
	if len(found) != 1 {
		t.Fatalf("renderer findings = %v, want one for the py file", found)
	}
	if found[0].Severity != Blocking || !strings.Contains(found[0].File, "legacy/build.sls") {
		t.Errorf("finding = %+v", found[0])
	}
}

func TestRegexConstructsAreReportedWithPosition(t *testing.T) {
	// The single most likely source of migration work in a mature tree,
	// made measurable before the migration starts.
	rep := runAudit(t)
	found := findingsFor(rep, CatRegex)
	if len(found) != 1 {
		t.Fatalf("regex findings = %v, want one lookahead", found)
	}
	f := found[0]
	if f.Subject != "(?=" || f.Severity != Blocking {
		t.Errorf("finding = %+v", f)
	}
	if f.Line != 1 || !strings.Contains(f.File, "audit/check.sls") {
		t.Errorf("finding should name the file and line: %+v", f)
	}
	if !strings.Contains(f.Action, "10.4") {
		t.Errorf("finding should point at the specification section: %+v", f)
	}
}

func TestYAMLHazardsAreReported(t *testing.T) {
	rep := runAudit(t)
	found := findingsFor(rep, CatYAML)
	var sawBool, sawOctal, sawDup bool
	for _, f := range found {
		switch f.Subject {
		case "yaml_1_1_boolean":
			sawBool = true
		case "octal":
			sawOctal = true
		case "duplicate_key":
			sawDup = true
			if f.Severity != Blocking {
				t.Errorf("a duplicate key is blocking, got %s", f.Severity)
			}
		}
	}
	if !sawBool || !sawOctal || !sawDup {
		t.Errorf("missing hazards: bool=%v octal=%v dup=%v in %v", sawBool, sawOctal, sawDup, found)
	}
}

func TestModuleUsageIsRecordedAndJudged(t *testing.T) {
	rep := runAudit(t)
	if rep.Modules["boto_ec2.get_zones"] != 1 {
		t.Errorf("module usage = %v", rep.Modules)
	}
	found := findingsFor(rep, CatModule)
	if len(found) != 1 || found[0].Subject != "boto_ec2.get_zones" {
		t.Fatalf("module findings = %v", found)
	}
	if found[0].Severity != Blocking {
		t.Errorf("a module that does not ship is blocking, got %s", found[0].Severity)
	}
}

func TestPythonExtensionDirectoriesAreBlocking(t *testing.T) {
	rep := runAudit(t)
	found := findingsFor(rep, CatCustomModule)
	if len(found) != 1 {
		t.Fatalf("custom module findings = %v", found)
	}
	if found[0].Severity != Blocking || !strings.Contains(found[0].Action, "24") {
		t.Errorf("finding = %+v", found[0])
	}
	if len(rep.CustomModules) != 1 || rep.CustomModules[0] != "_modules" {
		t.Errorf("custom module dirs = %v", rep.CustomModules)
	}
}

// TestPillarGrainTargetingIsFlagged covers SPEC section 12.4: a tree that
// targets pillar on a custom grain keeps working, and the act of trusting
// that grain becomes a recorded decision instead of an accident.
func TestPillarGrainTargetingIsFlagged(t *testing.T) {
	rep := runAudit(t)
	found := findingsFor(rep, CatPillarGrain)
	if len(found) != 1 {
		t.Fatalf("pillar grain findings = %v, want one for custom_role", found)
	}
	f := found[0]
	if f.Subject != "custom_role" {
		t.Errorf("subject = %q, want custom_role", f.Subject)
	}
	if f.Severity != Review {
		t.Errorf("severity = %s, want review", f.Severity)
	}
	if !strings.Contains(f.Action, "pillar_trusted_grains") {
		t.Errorf("action should name the setting: %q", f.Action)
	}
	// os_family is trusted by default and must not be reported.
	for _, g := range found {
		if g.Subject == "os_family" {
			t.Error("os_family is trusted by default and should not be flagged")
		}
	}
}

func TestConfigurationTranslationAndRefusal(t *testing.T) {
	rep := runAudit(t)
	var sawRename, sawRefusal, sawUnknown bool
	for _, f := range rep.Findings {
		if f.Category != CatConfig {
			continue
		}
		switch f.Subject {
		case "master": // lexicon:allow
			sawRename = true
		case "auto_accept":
			sawRefusal = true
			if f.Severity != Blocking {
				t.Errorf("a refused key is blocking, got %s", f.Severity)
			}
			if !strings.Contains(f.Action, "enrollment_mode") {
				t.Errorf("the refusal should name the supported path: %q", f.Action)
			}
		case "obsolete_setting":
			sawUnknown = true
		}
	}
	if !sawRename || !sawRefusal || !sawUnknown {
		t.Errorf("config findings incomplete: rename=%v refusal=%v unknown=%v", sawRename, sawRefusal, sawUnknown)
	}
}

func TestEffortEstimateAndCleanliness(t *testing.T) {
	rep := runAudit(t)
	counts := rep.Count()
	if counts.Total == 0 {
		t.Fatal("this tree has findings")
	}
	if counts.Blocking == 0 {
		t.Error("this tree has blocking findings")
	}
	if rep.Clean() {
		t.Error("Clean should be false while blocking findings remain")
	}
	// The estimate is per category, so a migration can be scoped.
	for _, cat := range []Category{CatRenderer, CatRegex, CatYAML, CatModule, CatCustomModule, CatPillarGrain} {
		if counts.ByCategory[cat] == 0 {
			t.Errorf("category %s has no count", cat)
		}
	}
}

func TestSummaryAndJSONAreProduced(t *testing.T) {
	rep := runAudit(t)
	summary := rep.Summary()
	for _, want := range []string{"Migration report", "Renderer inventory", "Module usage", "Effort estimate", "BLOCKING"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary is missing %q", want)
		}
	}

	js := rep.JSON()
	if got, _ := js.Get("schema"); got != "halite.migrate/1" {
		t.Errorf("schema = %#v", got)
	}
	if got, _ := js.Get("clean"); got != false {
		t.Errorf("clean = %#v", got)
	}
}

// TestCleanTreeReportsNothing is the shape of a finished migration.
func TestCleanTreeReportsNothing(t *testing.T) {
	root := t.TempDir()
	body := `nginx:
  pkg.installed:
    - name: nginx
    - version: '1.24'

/etc/nginx/nginx.conf:
  file.managed:
    - mode: '0644'
    - require:
      - pkg: nginx
`
	if err := os.WriteFile(filepath.Join(root, "web.sls"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := signature.NewRegistry()
	rep, err := Run(Options{Root: root, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Clean() {
		t.Errorf("a clean tree reported findings: %v", rep.Findings)
	}
	if !strings.Contains(rep.Summary(), "No findings") {
		t.Errorf("summary:\n%s", rep.Summary())
	}
}

// TestStripTemplatingPreservesPositions guards the trick the YAML pass
// depends on: blanking a template tag must not move any later line.
func TestStripTemplatingPreservesPositions(t *testing.T) {
	src := "a: 1\n{% for i in range(3) %}\nb: {{ i }}\n{% endfor %}\nc: 3\n"
	out := stripTemplating(src)
	if strings.Count(out, "\n") != strings.Count(src, "\n") {
		t.Fatalf("line count changed:\n%q\n%q", src, out)
	}
	inLines := strings.Split(src, "\n")
	outLines := strings.Split(out, "\n")
	for i := range inLines {
		if len(inLines[i]) != len(outLines[i]) {
			t.Errorf("line %d changed length: %q became %q", i+1, inLines[i], outLines[i])
		}
	}
	if strings.Contains(out, "{%") || strings.Contains(out, "{{") {
		t.Errorf("templating survived: %q", out)
	}
}

// TestStateDeclarationsAreAudited covers the gap that let a real tree
// with twenty-seven compilation errors be reported clean: the audit was
// not looking at the state declarations at all.
func TestStateDeclarationsAreAudited(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("top.sls", "base:\n  'nodename:host.example':\n    - match: grain\n    - web\n")
	write("web.sls", `/etc/thing:
  file.managed:
    - mode: 640
    - nosucharg: 1
    - require:
      - pkg: something

short_form:
  file.managed

wrong_function:
  file.nosuchfunction:
    - name: x
`)

	states := signature.NewRegistry()
	states.Add(
		signature.Signature{Module: "file", Function: "managed", Params: []signature.Param{
			{Name: "name", Type: signature.Path},
			{Name: "mode", Type: signature.Mode},
		}},
		signature.Signature{Module: "file", Function: "directory"},
	)
	rep, err := Run(Options{Root: root, StateRegistry: states})
	if err != nil {
		t.Fatal(err)
	}

	var msgs []string
	for _, f := range findingsFor(rep, CatState) {
		msgs = append(msgs, f.Msg)
		if f.Severity != Blocking {
			t.Errorf("%q should be blocking", f.Msg)
		}
	}
	joined := strings.Join(msgs, "\n")

	for _, want := range []string{
		`"nosucharg" is not an argument of file.managed`,
		"mode is the integer 640",
		"file.nosuchfunction is not a state function this build ships",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the audit should report %q, got:\n%s", want, joined)
		}
	}

	// The module's own functions are named, so the reader learns what to
	// write instead of only that they were wrong.
	if !strings.Contains(joined, "file provides directory, managed") {
		t.Errorf("the unknown function should name its siblings:\n%s", joined)
	}

	// A requisite is not an argument, the short declaration form is not a
	// finding, and a top file's target expressions are not state IDs.
	for _, unwanted := range []string{"require", "short_form", "nodename"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("%q should not be reported:\n%s", unwanted, joined)
		}
	}
}

// TestTemplatedKeysKeepTheirColumn covers a silence in the audit itself.
// A state ID built from an expression — `{{ sls }} create jail:` — was
// blanked to spaces, which moved the key ten columns to the right, broke
// the file's structure, and made the declaration audit skip the whole
// file without saying so. Two real trees had five such files between
// them.
func TestTemplatedKeysKeepTheirColumn(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("top.sls", "base:\n  '*':\n    - web\n")
	write("web.sls", `plain:
  cmd.run:
    - name: /bin/echo

{%- if grains['os'] == 'FreeBSD' %}
{{ sls }} start jail:
  cmd.run:
    - name: bastille start troupe
{%- endif %}
`)
	states := signature.NewRegistry()
	states.Add(signature.Signature{Module: "cmd", Function: "run", Params: []signature.Param{
		{Name: "name", Type: signature.String},
		{Name: "shell", Type: signature.Bool},
	}})

	rep, err := Run(Options{Root: root, StateRegistry: states})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, f := range findingsFor(rep, CatState) {
		msgs = append(msgs, f.Msg)
	}
	joined := strings.Join(msgs, "\n")

	// The declaration inside the conditional, under a templated ID, is
	// audited like any other.
	if !strings.Contains(joined, "bastille start troupe") {
		t.Errorf("the templated declaration was not audited:\n%s", joined)
	}
	// A program with no arguments in its name is not reported.
	if strings.Contains(joined, "/bin/echo") {
		t.Errorf("a plain program name should not be reported:\n%s", joined)
	}
}

// TestShellLinesAreReported is the most common thing an unconverted tree
// gets wrong, and it fails at run time rather than at compile time — so
// without this the audit calls the tree clean and the operator finds out
// one state at a time, during an apply.
func TestShellLinesAreReported(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "top.sls"), []byte("base:\n  '*':\n    - web\n"), 0o644)
	os.WriteFile(filepath.Join(root, "web.sls"), []byte(`needs_splitting:
  cmd.run:
    - name: systemctl restart nginx

opted_in:
  cmd.run:
    - name: systemctl restart nginx
    - shell: true

already_split:
  cmd.run:
    - name: /usr/bin/systemctl
    - args: [restart, nginx]
`), 0o644)

	states := signature.NewRegistry()
	states.Add(signature.Signature{Module: "cmd", Function: "run", Params: []signature.Param{
		{Name: "name", Type: signature.String},
		{Name: "args", Type: signature.List},
		{Name: "shell", Type: signature.Bool},
	}})
	rep, err := Run(Options{Root: root, StateRegistry: states})
	if err != nil {
		t.Fatal(err)
	}

	var reported []string
	for _, f := range findingsFor(rep, CatState) {
		if strings.Contains(f.Msg, "names a program with arguments") {
			reported = append(reported, f.Subject)
			if f.Severity != Review {
				t.Errorf("%s should be a review finding, not %s", f.Subject, f.Severity)
			}
		}
	}
	if len(reported) != 1 {
		t.Errorf("reported %d shell lines, want 1 (the one that did not opt in): %v", len(reported), reported)
	}
}
