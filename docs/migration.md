# Migrating a Salt tree to halite

`halite parse` reads an existing Salt state and pillar tree and reports
what halite can use as written, what has to be translated, and what it
does not support at all. It changes nothing: it renders, parses, and
compiles each file exactly as `halite apply` would, and prints where the
pipeline stops.

```sh
halite parse                                   # /srv/salt and /srv/pillar
halite parse -root ./states                    # pillar defaults to ./pillar
halite parse -root ./states -pillar-root ./secrets
halite parse ./states/web/init.sls             # one file
halite parse -root ./states -json | jq .summary
halite parse -errors                           # only what blocks halite
```

Without `-pillar-root`, halite looks for a `pillar` directory beside the
state root — the convention `halite apply` uses, and Salt's own layout. A
pillar tree that is not there is skipped, not reported as a failure.

The exit status is 1 when the report holds any error, so it works as a CI
gate while a tree is being converted.

## What the report says

Each finding carries a severity:

| Severity | Meaning |
|---|---|
| `ERROR` | halite will not run this as written: it fails to load, or it loads and does something other than what Salt does. |
| `WARN` | halite loads the file and ignores the construct. |
| `INFO` | supported, with a caveat worth knowing. |

Files are rendered with **the grains of the host you run `parse` on** and
with the pillar that halite itself can read from the pillar root. A top
file that targets other hosts is still checked, but the SLS names it
selects for this host are reported as well — run `parse` on a host of each
kind to see the whole tree exercised.

A file marked *read with its templates stripped* could not be rendered.
halite removed its template constructs and read the YAML underneath, so
the findings below that line describe the file as written rather than as
it would render. The template errors above the line come first for a
reason: fix those, and the rest of the report becomes exact.

## Findings, and the translation each one needs

### Templating

halite renders SLS files with Go `text/template`, not Jinja. The
delimiters are the same, so `{{ ... }}` is only a problem when what is
inside it is Jinja.

| Code | Salt | halite |
|---|---|---|
| `jinja-block` | `{% if %}` / `{% for %}` / `{% set %}` | `{{ if }}`, `{{ range }}`, `{{ $x := }}`, each closed by `{{ end }}` |
| `jinja-comment` | `{# ... #}` | `{{/* ... */}}`, or a YAML `#` comment |
| `jinja-expr` | `{{ grains['os'] }}`, `{{ pillar.get('x', 'y') }}`, `{{ salt['cmd.run']('x') }}` | `{{ .Grains.os }}`, `{{ .Pillar.x \| default "y" }}`; execution modules cannot be called from a template |
| `jinja-filter` | `{{ x \| default('y') }}` | `{{ .X \| default "y" }}` — Go pipelines take positional arguments and no parentheses |
| `renderer` | `#!py`, `#!jinja\|yaml\|gpg` | one renderer: Go `text/template` over YAML. A `#!jinja\|yaml` line is ignored; `#!py` means the file has to be rewritten as data |
| `template-error` | — | the file renders as a Go template and the error is exact: fix it where it points |
| `template-renderer` | `- template: jinja` | `- template: true` renders the source with Go `text/template`. Any other value renders nothing at all, so the file is copied with its markup intact |
| `jinja-template-file` | `files/nginx.conf.j2` | translate the file; `file.managed` renders it under `template: true` |

The template functions halite provides are `default`, `contains`,
`hasPrefix`, `hasSuffix`, `split`, `join`, `lower`, and `upper`, plus the
`text/template` builtins. Anything else — `selectattr`, `regex_replace`,
`to_json` — has to move out of the template, usually into pillar.

A loop translates directly:

```jinja
{% for user in pillar['admins'] %}
{{ user }}_home:
  file.directory:
    - name: /home/{{ user }}
{% endfor %}
```

```gotemplate
{{ range $user := .Pillar.admins }}
{{ $user }}_home:
  file.directory:
    - name: /home/{{ $user }}
{{ end }}
```

### YAML

halite parses the YAML subset SLS files normally use — ordered mappings,
block lists, scalars, comments — in about 300 lines of stdlib Go. The
features it leaves out are reported rather than mis-parsed.

| Code | Not supported | Instead |
|---|---|---|
| `yaml-block-scalar` | `contents: \|` and `>` | ship the text as a file beside the SLS and use `- source: motd`, or put it on one line in double quotes with `\n` |
| `yaml-anchor`, `yaml-alias`, `yaml-merge-key` | `&name`, `*name`, `<<:` | repeat the keys, or move the shared data into pillar |
| `yaml-flow` | `ports: [80, 443]` | a block list: `- 80` and `- 443` on their own lines. Only the empty forms `[]` and `{}` are accepted; quote the value to keep a string that starts with a bracket |
| `yaml-multi-doc` | `---` between documents | one document per file |
| `yaml-tag` | `!!str`, `!!python/object` | every scalar is a string; modules coerce what they need |
| `yaml-complex-key` | `? key` | plain scalar keys |
| `yaml-tab` | tabs in indentation | spaces, as YAML requires anyway |
| `yaml-error` | — | the parser's own message, with the line. Duplicate keys in one mapping land here: Salt keeps the last, halite refuses the file |

### States

| Code | Meaning | Instead |
|---|---|---|
| `short-declaration` | `pkg:` with `- installed` as the first argument | the dotted form, `pkg.installed:` — halite reads nothing else |
| `unsupported-module` | the state function is not compiled into halite | see the hint on the finding; usually `cmd.run` with a `creates:`/`unless:` gate, or an executable in `_modules/` ([external modules](external-modules.md)) |
| `unsupported-requisite` | `onfail`, `listen`, `order`, `retry`, `use`, the `_any` variants, … | halite has `require`, `watch`, `onchanges`, `prereq`, their `_in` forms, and `names:` expansion. What is left has no equivalent — see the hint on the finding |
| `ignored-argument` | the module does not read this argument | the hint says what halite does instead. A few — `file.managed: source_hash`, `file.managed: replace`, `cmd.run: runas` — are errors rather than warnings, because ignoring them changes the result rather than doing less |
| `salt-uri` | `source: salt://web/nginx.conf` | drop the prefix: the whole tree ships, and sources resolve relative to the SLS file |
| `remote-source` | `file.managed` with an `http(s)://` source | fetch it with `archive.extracted` (which requires a `source_hash` for remote sources) or `cmd.run` |
| `extend`, `exclude` | Salt's cross-file overrides | declare the override in the state itself, or leave the SLS name out of the top file |
| `missing-include`, `relative-include` | an `include:` name that does not resolve | include names are dotted paths from the tree root; `.sibling` is not resolved |
| `sls-shape`, `top-shape` | the file is not a mapping of state IDs, or the top file is not a mapping of environments and targets | usually a symptom of a template that did not render |
| `unreadable` | the file could not be opened | permissions, or a broken symlink |

### Top files and targeting

halite has one target language, shared by top files and `halite run`:
`*`, a glob on the host id, `grain:valueglob` (`os_family:FreeBSD`,
`osrelease:14.*`), the `G@`/`L@`/`E@`/`P@` matchers, and `and`/`or`/`not`
combinations of those. See
[writing-states.md](writing-states.md#targeting).

| Code | Meaning |
|---|---|
| `unsupported-target` | the target does not parse. `G@`, `L@`, `E@`, `P@` and `and`/`or`/`not` are implemented; `I@` (pillar), `S@` (subnet), `N@` (nodegroup) and `R@` (range) are not — move the distinction into a grain |
| `top-match-directive` | a `- match:` entry under a target: the matcher is inferred from the pattern instead |
| `missing-sls` | the top file names an SLS that does not resolve under the root |
| `top-environment` | halite has no environment selection: every environment in a top file is applied |
| `top-no-match` | nothing in the top file matches the host running `parse` |
| `no-top` | no `top.sls` at the tree root, so there is no highstate — only named SLS files |

### Extensions

| Code | Meaning |
|---|---|
| `salt-extension-dir` | `_states/`, `_grains/`, `_renderers/`, `_returners/` and the rest hold Python. halite loads no Python |
| `python-module` | a `.py` file in `_modules/`. halite runs `_modules/<name>` as an executable that reads a JSON request on stdin and writes a JSON result on stdout — a Python file works only with a `#!` line and the executable bit |
| `module-not-executable` | a file in `_modules/` without the executable bit |

## What the report cannot tell you

* Whether a `cmd.run` command behaves the same on the target platform.
* Whether an argument halite accepts means the same thing it does in
  Salt beyond the name — read [docs/states.md](states.md) for the
  arguments each module implements.
* What a Jinja file would have rendered to. When a file is marked
  approximate, its state inventory is what the text declares, and a
  `{% for %}` loop over pillar may stand for many more states than the
  one the report counted.

Run `halite apply -test -root <tree>` once the errors are gone: a dry run
is the check the report cannot make.
