package perf

import (
	"fmt"
	"strings"

	"github.com/edlitmus/halite/internal/state"
	"github.com/edlitmus/halite/internal/template"
	"github.com/edlitmus/halite/internal/value"
)

// The representative trees the SPEC section 30 benchmarks compile.
//
// SPEC names the shape rather than a corpus -- "500 states, 50 SLS files,
// heavy Jinja" and "200 pillar SLS" -- so the trees are generated to that
// shape here instead of vendored from the estate. Three reasons, and the
// third is the one that decided it:
//
//   - a benchmark has to run on every platform CI builds for, and the
//     estate's tree is not in this repository and never will be;
//   - a number is only comparable between two runs if the input is the
//     same, and a real tree changes under the people who own it;
//   - a generated tree can be *asserted*. `TestTheTreesHaveTheShapeSpecNames`
//     holds these to the counts SPEC names, so a benchmark cannot quietly
//     end up measuring five states and a rounding error.
//
// What a generated tree is not is representative of every real one. It is
// written from what this estate's SLS actually does -- a macro import, a
// map.jinja-style pillar lookup, loops that emit declarations, filters,
// a block scalar for `contents`, and requisites both inside a file and
// across two -- and a tree that leans on something else will compile at a
// different speed. That is a limit of the measurement, not a defect in it,
// and it is why the numbers are recorded with the tree they came from.

const (
	// SPEC section 30, the highstate row: 500 states over 50 SLS files.
	//
	// One of the fifty is the file the other forty-nine include, so that
	// the compilation resolves an include graph and a cross-file
	// requisite rather than fifty unrelated files. It carries its ten
	// declarations like the rest, which is what keeps both counts the
	// ones SPEC names.
	slsFiles          = 50
	appFiles          = slsFiles - 1
	declsPerFile      = 10
	highstateDecls    = slsFiles * declsPerFile
	confPerSLS        = declsPerFile - 2 // the rest are the directory and the service
	pillarFiles       = 200              // SPEC section 30, the pillar row
	pillarKeysPerFile = 12
)

// mapLoader serves an SLS tree from memory.
type mapLoader struct {
	files map[string]string
	envs  []string
}

func (m *mapLoader) Source(env, sls string) ([]byte, string, error) {
	if src, ok := m.files[env+"|"+sls]; ok {
		return []byte(src), env + "/" + strings.ReplaceAll(sls, ".", "/") + ".sls", nil
	}
	return nil, "", fmt.Errorf("%w: %s", state.ErrNotFound, sls)
}

func (m *mapLoader) Envs() []string { return m.envs }

func (m *mapLoader) Templates(env string) template.Loader { return templateFiles{m, env} }

type templateFiles struct {
	m   *mapLoader
	env string
}

// Load resolves a template by path, which is how `import` and `from`
// address a file. The generated tree keeps its macros at the top of the
// environment, so the name is the path.
func (t templateFiles) Load(name string) (string, string, error) {
	if src, ok := t.m.files[t.env+"|"+name]; ok {
		return src, name, nil
	}
	return "", "", template.ErrNotFound
}

// highstateTree builds the state tree of SPEC 30's highstate row.
func highstateTree() *mapLoader {
	files := map[string]string{
		"base|macros.jinja": macrosJinja,
		"base|common":       commonSLS(),
	}

	var top strings.Builder
	top.WriteString("base:\n  '*':\n")
	for i := 0; i < appFiles; i++ {
		name := fmt.Sprintf("app%02d", i)
		top.WriteString("    - " + name + "\n")
		files["base|"+name] = appSLS(i)
	}
	files["base|top"] = top.String()

	return &mapLoader{files: files, envs: []string{"base"}}
}

// macrosJinja is the shared macro file every generated SLS imports, which
// is what a real tree does with `map.jinja`.
const macrosJinja = `{% macro conf(app, index, owner) -%}
- name: /etc/{{ app }}/{{ '%02d' | format(index) }}.conf
- mode: '0644'
- user: {{ owner | default('root', true) }}
{%- endmacro %}

{% macro tag(app, tier) -%}
{{ app | upper }}-{{ tier | replace('-', '_') }}
{%- endmacro %}
`

// commonSLS is the file every generated SLS includes, so that the
// compilation resolves an include graph rather than fifty unrelated
// files. It carries its own ten declarations like every other file.
func commonSLS() string {
	return fmt.Sprintf(`{%% set tier = pillar.get('tier', 'base') %%}

common-dir:
  file.directory:
    - name: /etc/common
    - mode: '0755'

{%% for index in range(%d) %%}
common-marker-{{ index }}:
  file.managed:
    - name: /etc/common/marker{{ index }}
    - mode: '0644'
    - contents: |
        {{ grains.get('os', 'unknown') }}
        {{ tier }}
    - require:
      - file: common-dir
{%% endfor %%}
`, declsPerFile-1)
}

// appSLS is one generated SLS: a macro import, pillar and grain lookups,
// a loop that emits most of the declarations, a block scalar built from
// pillar data, and requisites pointing inside the file and at another one.
func appSLS(n int) string {
	return fmt.Sprintf(`{%% from 'macros.jinja' import conf, tag with context %%}
{%% set app = 'app%02d' %%}
{%% set tier = pillar.get('tier', 'base') %%}
{%% set packages = pillar.get('packages', ['base']) %%}
{%% set owner = pillar.get('owner', 'root') %%}
{%% set enabled = grains.get('os_family', '') in ['Debian', 'FreeBSD'] %%}

include:
  - common

{{ app }}-dir:
  file.directory:
    - name: /etc/{{ app }}
    - mode: '0755'
    - require:
      - file: common-dir

{%% for index in range(%d) %%}
{{ app }}-conf-{{ index }}:
  file.managed:
    {{ conf(app, index, owner) | indent(4) }}
    - contents: |
        # {{ tag(app, tier) }}
        {%% for name in packages %%}
        package {{ name }} {{ loop.index }}
        {%% endfor %%}
        owner={{ owner }} os={{ grains.get('os', 'unknown') }}
    - require:
      - file: {{ app }}-dir
{%% endfor %%}

{{ app }}-service:
  service.running:
    - name: {{ app }}
    - enable: {{ enabled }}
    - watch:
{%% for index in range(%d) %%}
      - file: {{ app }}-conf-{{ index }}
{%% endfor %%}
`, n, confPerSLS, confPerSLS)
}

// pillarTree builds the pillar tree of SPEC 30's pillar row: 200 SLS, all
// matched by the top file, merged into one pillar.
func pillarTree() *mapLoader {
	files := map[string]string{}

	var top strings.Builder
	top.WriteString("base:\n  '*':\n")
	for i := 0; i < pillarFiles; i++ {
		name := fmt.Sprintf("group%03d", i)
		top.WriteString("    - " + name + "\n")
		files["base|"+name] = pillarSLS(i)
	}
	files["base|top"] = top.String()

	return &mapLoader{files: files, envs: []string{"base"}}
}

// pillarSLS is one generated pillar file. Pillar is flatter than state
// and its cost is in the merge, so each file contributes a nested map
// under its own key and writes to two keys every other file also writes
// to. `shared:counters` is where the recursive merge does its work --
// all 200 files land in one map -- and `owners` is a list, which the
// default strategy replaces rather than concatenates, so it stays at one
// entry and is here to keep that visible rather than by oversight.
func pillarSLS(n int) string {
	return fmt.Sprintf(`{%% set group = 'group%03d' %%}
{%% set region = ['east', 'west', 'north'] | first %%}

{{ group }}:
{%% for i in range(%d) %%}
  key{{ i }}:
    name: {{ group }}-{{ i }}
    region: {{ region }}
    enabled: {{ 'true' if i %% 2 == 0 else 'false' }}
    tags:
      - {{ group }}
      - tier-{{ i %% 4 }}
{%% endfor %%}

shared:
  counters:
    {{ group }}: {{ %d }}

owners:
  - {{ group }}-owner
`, n, pillarKeysPerFile, n)
}

// nodeGrains are the grains both trees render against.
func nodeGrains() *value.Map {
	return value.MapOf(
		"id", "web01.prod",
		"os", "FreeBSD",
		"os_family", "FreeBSD",
		"osrelease", "15.1",
		"kernel", "FreeBSD",
		"cpuarch", "amd64",
	)
}

// nodePillar is what the state tree renders against, standing in for the
// pillar a real highstate would have been handed.
func nodePillar() *value.Map {
	return value.MapOf(
		"tier", "production",
		"owner", "www",
		"packages", []any{"nginx", "openssl", "curl"},
	)
}
