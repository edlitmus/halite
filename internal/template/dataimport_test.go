package template

import "testing"

// Salt's `import_yaml`, which every formula in the wild uses to carry
// its defaults: a `defaults.yaml` beside `map.jinja`, read and merged
// with pillar.
//
// Without the tag a tree does not compile at all. It is a parse error,
// so the whole file fails, and with it everything importing that file —
// on the estate's tree three errors in the report were one missing tag.
//
// The file is data, not a template: it is parsed, never rendered, so
// nothing inside it executes and a `{%` in a value stays a `{%`.
func TestImportYAMLReadsAFileAsData(t *testing.T) {
	loader := mapLoader{
		"defaults.yaml": "qualys:\n  enabled: true\n  port: 443\n",
	}
	got := renderWith(t, `{% import_yaml "defaults.yaml" as d %}{{ d.qualys.port }}/{{ d.qualys.enabled }}`, nil, loader, nil)
	if got != "443/True" && got != "443/true" {
		t.Errorf("import_yaml gave %q, want the parsed values", got)
	}
}

func TestImportJSONAndText(t *testing.T) {
	loader := mapLoader{
		"d.json":  `{"a": {"b": 7}}`,
		"raw.txt": "{% this is not a tag %}",
	}
	if got := renderWith(t, `{% import_json "d.json" as d %}{{ d.a.b }}`, nil, loader, nil); got != "7" {
		t.Errorf("import_json gave %q, want 7", got)
	}
	// Text is the file verbatim. A tag inside it is not executed, which
	// is the property that makes importing data safe.
	if got := renderWith(t, `{% import_text "raw.txt" as s %}{{ s }}`, nil, loader, nil); got != "{% this is not a tag %}" {
		t.Errorf("import_text gave %q, want the file verbatim", got)
	}
}

// A child template in an `extends` chain contributes only the statements
// that establish names — its own output is discarded — so a data import
// has to be one of them. Everything else it sets would be visible to the
// parent's blocks while the imported data was not, which is the confusing
// half of a missing case rather than a clean failure.
func TestExtendingTemplateCanUseImportYAML(t *testing.T) {
	loader := mapLoader{
		"defaults.yaml": "users:\n  shell: /bin/zsh\n",
		"parent.jinja":  `[{% block body %}{% endblock %}]`,
	}
	child := `{% extends "parent.jinja" %}` +
		`{% import_yaml "defaults.yaml" as d %}` +
		`{% set shell = d.users.shell %}` +
		`{% block body %}{{ shell }}{% endblock %}`
	if got := renderWith(t, child, nil, loader, nil); got != "[/bin/zsh]" {
		t.Errorf("a child template using import_yaml gave %q, want [/bin/zsh]", got)
	}
}

// A `map.jinja` that imports its defaults and then derives a name from
// them has to work when *that* file is itself imported.
func TestImportedTemplateCanUseImportYAML(t *testing.T) {
	loader := mapLoader{
		"defaults.yaml": "users:\n  shell: /bin/bash\n",
		"map.jinja":     `{% import_yaml "defaults.yaml" as d %}{% set users = d.users %}`,
	}
	if got := renderWith(t, `{% from "map.jinja" import users with context %}{{ users.shell }}`, nil, loader, nil); got != "/bin/bash" {
		t.Errorf("an imported map.jinja using import_yaml gave %q, want /bin/bash", got)
	}
}

// A file that is not there is named, rather than reported as a parse
// failure somewhere else.
func TestImportYAMLNamesAMissingFile(t *testing.T) {
	env := NewEnvironment(mapLoader{}, DefaultOptions())
	if _, err := env.RenderString(`{% import_yaml "absent.yaml" as d %}`, "t.sls", nil); err == nil {
		t.Fatal("a missing data file was accepted")
	}
}
