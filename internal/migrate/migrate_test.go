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

// mkdirAll and writeFile are the two lines every tree-building test
// would otherwise repeat.
func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
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

// TestTemplatedKeysAreDistinct guards the placeholder itself. Two state
// IDs built from different expressions are different keys, and giving
// them the same placeholder reported a duplicate key the file does not
// have — a blocking finding invented by the audit.
func TestTemplatedKeysAreDistinct(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "top.sls"), []byte("base:\n  '*':\n    - web\n"), 0o644)
	os.WriteFile(filepath.Join(root, "web.sls"), []byte(`{{ service }}:
  test.nop: []

{{ other }}:
  test.nop: []
`), 0o644)

	rep, err := Run(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Findings {
		if strings.Contains(f.Msg, "duplicate mapping key") {
			t.Errorf("the audit invented a duplicate key: %s", f.Msg)
		}
	}

	// A file that really does repeat a key is still reported.
	os.WriteFile(filepath.Join(root, "web.sls"), []byte("a: 1\na: 2\n"), 0o644)
	rep, err = Run(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range rep.Findings {
		if strings.Contains(f.Msg, "duplicate mapping key") {
			found = true
		}
	}
	if !found {
		t.Error("a real duplicate key should still be reported")
	}
}

// TestCmdDefaultShellChangesWhatIsWork. A tree whose cmd states carry
// shell lines has six problems or none, depending on one setting. The
// audit exists to say how much work a migration is, so getting this
// wrong in either direction is the failing it was built to correct:
// without the setting the states fail one at a time mid-apply, and with
// it they run as they stand.
func TestCmdDefaultShellChangesWhatIsWork(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "top.sls"), []byte("base:\n  '*':\n    - web\n"), 0o644)
	os.WriteFile(filepath.Join(root, "web.sls"), []byte(`one:
  cmd.run:
    - name: systemctl restart nginx

two:
  cmd.run:
    - name: systemctl restart sshd

converted:
  cmd.run:
    - name: /usr/bin/systemctl
    - args: [restart, cron]
`), 0o644)

	states := signature.NewRegistry()
	states.Add(signature.Signature{Module: "cmd", Function: "run", Params: []signature.Param{
		{Name: "name", Type: signature.String},
		{Name: "args", Type: signature.List},
		{Name: "shell", Type: signature.Bool},
	}})

	report := func(defaultShell bool) *Report {
		rep, err := Run(Options{Root: root, StateRegistry: states, DefaultShell: defaultShell})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}

	off := report(false)
	shellFindings := 0
	for _, f := range findingsFor(off, CatState) {
		if strings.Contains(f.Msg, "names a program with arguments") {
			shellFindings++
		}
	}
	if shellFindings != 2 {
		t.Errorf("with the setting off, %d shell lines were reported; want 2", shellFindings)
	}
	if off.ShellLines != 2 {
		t.Errorf("ShellLines = %d, want 2", off.ShellLines)
	}

	on := report(true)
	for _, f := range findingsFor(on, CatState) {
		if strings.Contains(f.Msg, "names a program with arguments") {
			t.Errorf("with the setting on, a shell line was reported as work: %s", f.Msg)
		}
	}
	// Counted either way, because the tree has acquired a dependency on
	// the setting and a report that said only "no work" would hide it.
	if on.ShellLines != 2 {
		t.Errorf("ShellLines = %d with the setting on, want 2", on.ShellLines)
	}
	if !strings.Contains(on.Summary(), "cmd_default_shell was assumed") {
		t.Errorf("the summary should say the tree depends on the setting:\n%s", on.Summary())
	}
}

// A Salt estate that keeps one repository puts its pillar in `pillar/`
// beside its states, and `halite-hub migrate <repo>` is the first thing
// anyone runs.
//
// The state walk used to recurse into it and read every pillar file as
// a state, so a mapping of hostname to values came back as
// "beastie.example is not a state function this build ships", marked
// BLOCKING. A migration tool that reports work which does not exist is
// worse than one that reports none: the next real finding is the one
// nobody believes.
func TestPillarBesideTheStatesIsAuditedAsPillar(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "state"))
	mkdirAll(t, filepath.Join(root, "pillar"))
	writeFile(t, filepath.Join(root, "state", "web.sls"), `
nginx:
  pkg.installed: []
`)
	// Plain pillar data: a mapping whose keys are hostnames, which is
	// not a state function and must not be read as one.
	writeFile(t, filepath.Join(root, "pillar", "samba.sls"), `
samba:
  beastie.example.com:
    shares:
      - name: main
        path: /chunk
`)

	rep, err := Run(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if rep.PillarFiles != 1 {
		t.Errorf("pillar files = %d, want 1; the pillar tree was not found", rep.PillarFiles)
	}
	if rep.SLSFiles != 1 {
		t.Errorf("state files = %d, want 1; pillar was counted as state", rep.SLSFiles)
	}
	if rep.PillarRoot == "" {
		t.Error("the report does not say where pillar was read from")
	}
	for _, f := range rep.Findings {
		if f.Severity == Blocking {
			t.Errorf("a clean tree reported a blocking finding: %s %s: %s",
				f.File, f.Subject, f.Msg)
		}
	}
}

// `- match: grain` names the grain in the expression rather than with a
// G@ sigil, and is the spelling a real Salt tree uses.
//
// The audit looked only for the sigil, so it called such a tree clean
// and the first run then failed to compile pillar at all — the compiler
// enforces SPEC 12.4 whatever the audit said. The audit has to predict
// the refusal, or it is worse than nothing.
func TestAPillarTopMatchingOnAnUntrustedGrainIsReported(t *testing.T) {
	root := t.TempDir()
	mkdirAll(t, filepath.Join(root, "state"))
	mkdirAll(t, filepath.Join(root, "pillar"))
	writeFile(t, filepath.Join(root, "pillar", "top.sls"), `
base:
  '*':
    - common
  'nodename:beastie.example.com':
    - match: grain
    - samba
  'os_family:FreeBSD':
    - match: grain
    - bsd
`)

	rep, err := Run(Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	found := findingsFor(rep, CatPillarGrain)
	if len(found) != 1 {
		t.Fatalf("pillar grain findings = %d, want 1: %+v", len(found), found)
	}
	if found[0].Subject != "nodename" {
		t.Errorf("the finding names %q, not the untrusted grain", found[0].Subject)
	}
	// `os_family` is in the default allowlist and must not be reported,
	// or the audit cries wolf on every tree that targets on the OS.
	for _, f := range found {
		if f.Subject == "os_family" {
			t.Error("a trusted grain was reported as untrusted")
		}
	}
}

// TestGatedBranchesAreNotDuplicateKeys is the report an operator cannot
// use: a grain-gated pillar file defines the same key once per branch,
// and the audit called every one of them a blocking duplicate. Only one
// branch of a conditional is ever live, so the file has one definition.
func TestGatedBranchesAreNotDuplicateKeys(t *testing.T) {
	root := t.TempDir()
	pillar := filepath.Join(root, "pillar")
	mkdirAll(t, pillar)
	writeFile(t, filepath.Join(root, "top.sls"), "base:\n  '*':\n    - web\n")
	writeFile(t, filepath.Join(root, "web.sls"), "nop:\n  test.nop: []\n")
	writeFile(t, filepath.Join(pillar, "top.sls"), "base:\n  '*':\n    - common\n")
	writeFile(t, filepath.Join(pillar, "common.sls"), `{% if grains['os_family'] == 'Debian' %}
apache:
  pkg: apache2
{% elif grains['os_family'] == 'RedHat' %}
apache:
  pkg: httpd
{% else %}
apache:
  pkg: unknown
{% endif %}
`)

	rep, err := Run(Options{Root: root, PillarRoot: pillar})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rep.Findings {
		if strings.Contains(f.Msg, "duplicate mapping key") {
			t.Errorf("a gated branch was reported as a duplicate: %s", f)
		}
	}
}

// TestDuplicateInOneArmIsStillReported is the other half: the fix must
// not buy quiet by stopping looking. A key repeated inside a single
// branch, or outside every branch, is a real duplicate — and is reported
// once, not once per branch audited.
func TestDuplicateInOneArmIsStillReported(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"outside the conditional", `{% if grains['os'] == 'Ubuntu' %}
apache:
  pkg: apache2
{% else %}
apache:
  pkg: httpd
{% endif %}
shared: 1
shared: 2
`},
		{"inside the if arm", `{% if grains['os'] == 'Ubuntu' %}
dup: 1
dup: 2
{% else %}
apache:
  pkg: httpd
{% endif %}
`},
		{"inside the else arm", `{% if grains['os'] == 'Ubuntu' %}
apache:
  pkg: apache2
{% else %}
dup: 1
dup: 2
{% endif %}
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			pillar := filepath.Join(root, "pillar")
			mkdirAll(t, pillar)
			writeFile(t, filepath.Join(root, "top.sls"), "base:\n  '*':\n    - web\n")
			writeFile(t, filepath.Join(root, "web.sls"), "nop:\n  test.nop: []\n")
			writeFile(t, filepath.Join(pillar, "top.sls"), "base:\n  '*':\n    - common\n")
			writeFile(t, filepath.Join(pillar, "common.sls"), tc.body)

			rep, err := Run(Options{Root: root, PillarRoot: pillar})
			if err != nil {
				t.Fatal(err)
			}
			var dups []Finding
			for _, f := range rep.Findings {
				if strings.Contains(f.Msg, "duplicate mapping key") {
					dups = append(dups, f)
				}
			}
			if len(dups) != 1 {
				t.Errorf("duplicate findings = %d, want exactly 1: %+v", len(dups), dups)
			}
		})
	}
}

// TestGatedPillarTopReportsEveryGrainTarget covers the finding the gating
// used to take with it. A top file that opens `base:` in each arm of a
// conditional read as a duplicate key, the parse failed, and the audit
// reported no grain targets at all — a tree called clean whose first run
// would not compile pillar.
func TestGatedPillarTopReportsEveryGrainTarget(t *testing.T) {
	root := t.TempDir()
	pillar := filepath.Join(root, "pillar")
	mkdirAll(t, pillar)
	writeFile(t, filepath.Join(root, "top.sls"), "base:\n  '*':\n    - web\n")
	writeFile(t, filepath.Join(root, "web.sls"), "nop:\n  test.nop: []\n")
	writeFile(t, filepath.Join(pillar, "common.sls"), "key: value\n")
	writeFile(t, filepath.Join(pillar, "top.sls"), `{% if grains['os_family'] == 'Debian' %}
base:
  'roles:web':
    - match: grain
    - common
{% else %}
base:
  'datacentre:iad':
    - match: grain
    - common
{% endif %}
`)

	rep, err := Run(Options{Root: root, PillarRoot: pillar})
	if err != nil {
		t.Fatal(err)
	}
	found := findingsFor(rep, CatPillarGrain)
	subjects := map[string]int{}
	for _, f := range found {
		subjects[f.Subject]++
	}
	if subjects["roles"] != 1 || subjects["datacentre"] != 1 || len(found) != 2 {
		t.Errorf("grain findings = %+v, want one each for roles and datacentre", found)
	}
}

// TestStrippedVariantsKeepPositions guards the property every finding's
// position depends on: blanking the arms this rendering does not audit
// must not move a single later line or column.
func TestStrippedVariantsKeepPositions(t *testing.T) {
	src := `a: 1
{% if x %}
b: {{ two }}
{% elif y %}
b: 3
{% else %}
b: 4
{% endif %}
c: 5
`
	variants := strippedVariants(src)
	if len(variants) != 3 {
		t.Fatalf("renderings = %d, want one per arm of the conditional", len(variants))
	}
	srcLines := strings.Split(src, "\n")
	for n, v := range variants {
		lines := strings.Split(v, "\n")
		if len(lines) != len(srcLines) {
			t.Fatalf("rendering %d changed the line count: %q", n, v)
		}
		for i := range srcLines {
			if len(lines[i]) != len(srcLines[i]) {
				t.Errorf("rendering %d line %d changed length: %q became %q",
					n, i+1, srcLines[i], lines[i])
			}
		}
		if strings.Contains(v, "{%") || strings.Contains(v, "{{") {
			t.Errorf("rendering %d kept templating: %q", n, v)
		}
	}
	// Each arm is audited by some rendering: the three bodies appear
	// across the three renderings, one at a time.
	for _, want := range []string{"b: 3", "b: 4"} {
		seen := 0
		for _, v := range variants {
			if strings.Contains(v, want) {
				seen++
			}
		}
		if seen != 1 {
			t.Errorf("%q appears in %d renderings, want exactly 1", want, seen)
		}
	}
}

// TestForElseIsNotAPairOfAlternatives. Jinja's {% for %} takes an
// {% else %} too, and a loop body is not an alternative to it: reading
// them as arms would stop the audit from looking at a loop body at all.
func TestForElseIsNotAPairOfAlternatives(t *testing.T) {
	src := "{% for i in items %}\na: {{ i }}\n{% else %}\nb: none\n{% endfor %}\n"
	if arms := conditionalArms(src); len(arms) != 0 {
		t.Errorf("conditionalArms found %d conditionals in a for-else: %+v", len(arms), arms)
	}
	variants := strippedVariants(src)
	if len(variants) != 1 {
		t.Fatalf("renderings = %d, want 1 for a file with no conditional", len(variants))
	}
	for _, want := range []string{"a:", "b: none"} {
		if !strings.Contains(variants[0], want) {
			t.Errorf("the rendering dropped %q: %q", want, variants[0])
		}
	}
}
