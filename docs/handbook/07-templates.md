# Templates

<!-- abstract: Jinja inside SLS files and delivered files: the syntax, the names available, loops, macros, imports, filters, strict undefined, and debugging. -->
<!-- kind: concept -->
<!-- index: template; Jinja; renderer; shebang; undefined; strict; permissive; macro; import; filter; loop; namespace; map.jinja; filter_by; tojson; default filter; context; slspath; lint --rendered; render sandbox -->

Plain states describe one machine. **Templates** let one description fit
many. A template is a file with small programs embedded in it, written in
the **Jinja** language. halite evaluates the programs first, producing
plain text, and only then reads that text as YAML (for an SLS file) or
delivers it as it is (for a file on a node).

halite's template engine is its own, written for this project; it
implements the parts of Jinja that real state trees use, and it renders an
existing Jinja tree unchanged with one deliberate exception, described
under [Undefined names](#undefined-names-are-an-error).

## The three kinds of brackets

| Syntax | Meaning | Example |
|---|---|---|
| `{{ ... }}` | **Output** the value of an expression | `{{ grains['id'] }}` |
| `{% ... %}` | **Do** something: set a variable, loop, branch | `{% for u in users %}` |
| `{# ... #}` | A **comment**, produced as nothing | `{# explain the hack #}` |

A minus sign inside the brackets removes whitespace on that side:
`{%- ... -%}`. This matters in YAML, where stray blank lines and
indentation change meaning; the first two examples in this chapter use it.

## Where templates apply

| In | Rendered | With |
|---|---|---|
| Every `.sls` file (state, pillar and top files) | Before it is read as YAML | grains, pillar, and the `salt` function dispatcher on a node |
| A file delivered by `file.managed` with `template: jinja` | On the node, before the file is written | the same names, plus `context` you pass |
| Reactor and orchestration SLS files | On the hub | event data and a restricted set of functions (chapters [20](20-reactors.md), [21](21-orchestration.md)) |

The pipeline that an SLS file goes through is named by an optional first
line. The default is `#!jinja|yaml`: Jinja first, then YAML. Others:

| First line | Pipeline |
|---|---|
| (none), or `#!jinja\|yaml` | Jinja, then YAML. The default. |
| `#!yaml` | YAML only; `{{ }}` is literal text. Useful when a value contains braces. |
| `#!json` | A JSON document. |
| `#!jinja\|yaml\|gpg` | Jinja, YAML, then decrypt inline GPG values (pillar only; [chapter 9](09-pillar.md)). |

A file that asks for a renderer this build does not have is refused by
name:

```
the py renderer is not supported: requires Python; SPEC section 28.4
```

## The names you can use

Every template can see these:

| Name | Holds | Example |
|---|---|---|
| `grains` | This node's grains ([chapter 8](08-grains.md)) | `{{ grains['os_family'] }}` |
| `pillar` | This node's pillar ([chapter 9](09-pillar.md)) | `{{ pillar['webserver']['port'] }}` |
| `salt` | Execution functions, as a callable dictionary | `{{ salt['grains.get']('os') }}` |
| `opts` | The effective configuration, with secrets removed | `{{ opts.get('node_id') }}` |
| `id` | This node's ID | |
| `env`, `saltenv` | The environment name (both spellings work) | `base` |
| `pillarenv` | The pillar environment | |
| `sls` | The dotted name of the file being rendered | `webserver.nginx` |
| `slspath`, `tpldir`, `tplfile`, `slsdotpath` | Path helpers for building relative `salt://` URLs | |
| `haliteversion`, `saltversion` | The build's version; `saltversion` reports the compatibility level targeted | |

The same names, in an SLS file rendered for a node called
`system76-pc`, evaluated:

```json
{"slspath": "", "tpldir": ".", "saltenv": "base", "env": "base",
 "pillarenv": "base", "sls": "tpl", "id": "system76-pc",
 "opts_id": "system76-pc", "kernel": "Linux"}
```

`salt['module.function'](args)` calls an **execution function** at render
time and uses its return value. It is how a template asks the machine a
question. It is allowed on nodes and not on the hub when the hub renders
pillar or orchestration, for a reason described in
[chapter 28](28-hardening.md): a pillar template that could run a command
on the hub would turn "can edit the pillar tree" into "root on the hub".

## Variables, conditions and loops

```jinja
{% set root = pillar['playground']['dir'] %}
{% set is_bsd = grains['os_family'] == 'FreeBSD' %}

{% if is_bsd %}
packages: [curl, vim-tiny]
{% elif grains['os_family'] == 'Debian' %}
packages: [curl, vim-tiny, apt-transport-https]
{% else %}
packages: [curl]
{% endif %}
```

A **loop** is how a list in pillar becomes many states. This is a real
fragment of the example tree (`common/init.sls`):

<!-- example: handbook/webserver/states/common/init.sls lines=7-13 -->
```yaml
{% for name, user in pillar.get('admins', {}).items() %}
admin_{{ name }}:
  user.present:
    - name: {{ name }}
    - shell: {{ user.get('shell', '/bin/sh') }}
    - groups: {{ common.admin_groups | tojson }}
{% endfor %}
```

With the example pillar (`alice` and `bob`), this produces two
independent states, `admin_alice` and `admin_bob`. Add a name to pillar,
get an account. Remove it from pillar and the loop produces nothing; it
does **not** delete the account. To remove accounts, you list them in an
`absent` state. A declarative tree describes what *should* exist; it
does not infer deletions from omissions.

Inside a loop `loop` is available: `loop.index` (from 1), `loop.index0`,
`loop.first`, `loop.last`, `loop.length`, `loop.cycle('a','b')`.

> **Warning:** `{% set %}` inside a loop does not survive the loop
> iteration. To carry a value out, use a namespace:
>
> ```jinja
> {% set ns = namespace(total=0) %}
> {% for p in pillar['ports'].values() %}
>   {%- set ns.total = ns.total + p %}
> {% endfor %}
> total is {{ ns.total }}
> ```

## Undefined names are an error

This is halite's most visible departure from its predecessors. Suppose a
template says `{{ pillar['webserver']['prot'] }}`, a typo. In the older
tool this renders as an empty string, and your state quietly writes
`listen ;` into a configuration file. In halite:

```
states/playground.sls:18:39: ownr is undefined (mapping has no key ownr);
use `| default(...)`, `is defined`, or `.get(name, fallback)` to make it optional
```

The error names the file, the line and column, and the identifier. Nothing
runs. When a value really is optional, **say so**:

| You write | Meaning |
|---|---|
| `{{ pillar.get('a:b', 'fallback') }}` | A pillar lookup with a default; the colon walks nested keys. |
| `{{ x \| default('fallback') }}` | Use the fallback if `x` is **undefined**. |
| `{{ x \| default('fallback', true) }}` | ...or if `x` is false, empty or `None`. |
| `{% if x is defined %}...{% endif %}` | Branch on whether `x` exists. |
| `{{ d.get('key', 'fallback') }}` | A dictionary lookup with a default. |

Note the difference in the second row. `pillar.get('nope') | default('x')`
yields `None`, because `pillar.get` returned a value (`None`), so the name
is defined. Use `pillar.get('nope', 'x')`.

**Migrating a tree that relies on empty strings:** run it once with
`--permissive` (or `undefined: permissive` in the configuration). Every
undefined name then resolves to an empty string *and is logged with its
position*:

```
tpl.sls:3:16: nothing_here is undefined and resolved to the empty string under permissive mode
```

Fix each, then turn strict back on. Strict is the default and the right
setting.

## Expressions, filters and tests

Expressions work like a small, safe subset of Python: arithmetic
(`+ - * / // % **`), comparison, `and` / `or` / `not`, `in`, string
concatenation with `~`, conditional expressions
(`'a' if cond else 'b'`), indexing `x[0]`, slicing `x[1:3]`, and
attribute or key access `x.y` and `x['y']`.

A **filter** transforms a value and is written `value | filter(args)`.
These were evaluated to confirm what they return:

| Expression | Result |
|---|---|
| `['a','b'] \| join('-')` | `a-b` |
| `'abc' \| upper` | `ABC` |
| `[1,2,3] \| length` | `3` |
| `[3,1,2] \| sort` | `[1, 2, 3]` |
| `[1,1,2] \| unique \| list` | `[1, 2]` |
| `'web01' \| regex_replace('[0-9]+', 'NN')` | `webNN` |
| `'hi' \| base64_encode` | `aGk=` |
| `'x' \| sha256` | the hex digest |
| `{'a': 1} \| yaml_encode` | `{a: 1}` |
| `'yes' \| to_bool` | `true` |
| `'42' \| int + 1` | `43` |
| `'10.0.0.0/30' \| network_size` | `4` |
| `'10.0.0.1' \| is_ip` | `true` |
| `['/etc','x'] \| path_join` | `/etc/x` |
| `{'b':1,'a':2} \| dictsort` | `[['a', 2], ['b', 1]]` |
| `x \| tojson` | JSON text; the safe way to put a list or mapping into YAML |

The complete set — the standard Jinja filters, plus the additional ones
real trees use for addresses, hashes, dates and data structures — is
listed in SPEC section 10.2.4; if a filter or test in an existing tree is
missing, `lint` and the compiler name it.

`tojson` deserves a mention. When you need a list or a map inside a
state, emit it as JSON, which is also valid YAML, and never try to
assemble YAML by concatenating strings:

```yaml
    - pkgs: {{ common.packages | tojson }}
```

## Reusing code: macros, imports and includes

A **macro** is a function in a template:

```jinja
{% macro line(key, value) -%}
{{ key }} = {{ value | quote }}
{%- endmacro %}
```

Put macros in a file of their own, conventionally named `.jinja`, and
**import** them:

```jinja
{% from "macros.jinja" import line %}
{{ line('motd', pillar['motd']) }}
```

Other imports: `{% import "x.jinja" as x %}` (call `x.macro()`),
`{% include "frag.jinja" %}` (paste another template's output here), and
`{% import_yaml "defaults.yaml" as defaults %}` (read a YAML file into a
variable). Paths are relative to the root of the tree, not to the
importing file.

`{% from ... import ... with context %}` makes the imported template see
the current `grains`, `pillar` and so on. You need it for any imported
template that refers to them, as `map.jinja` does.

## The `map.jinja` pattern

The single most useful idiom in a real tree is a **map**: a file that
collects everything that differs per platform in one place, so the states
that use it have no `if` in them.

<!-- example: handbook/webserver/states/webserver/map.jinja -->
```jinja
{% set nginx = salt['grains.filter_by']({
    'Debian':  {'conf_dir': '/etc/nginx',            'user': 'www-data'},
    'RedHat':  {'conf_dir': '/etc/nginx',            'user': 'nginx'},
    'FreeBSD': {'conf_dir': '/usr/local/etc/nginx',  'user': 'www'},
}, grain='os_family', merge=salt['pillar.get']('nginx:lookup', {})) %}
```

`grains.filter_by` picks the dictionary whose key matches the node's
`os_family` grain, then merges anything in `pillar['nginx']['lookup']` on
top of it, so one machine can override a single value without copying
the table. The states then read:

<!-- example: handbook/webserver/states/webserver/nginx.sls lines=1-14 -->
```yaml
{% from "webserver/map.jinja" import nginx with context %}

nginx:
  pkg.installed: []

nginx_conf:
  file.managed:
    - name: {{ nginx.conf_dir }}/nginx.conf
    - source: salt://webserver/files/nginx.conf.jinja
    - template: jinja
    - user: root
    - mode: '0644'
    - context:
        worker_processes: {{ grains['num_cpus'] }}
```

Compiled for a FreeBSD web server, `conf_dir` becomes
`/usr/local/etc/nginx` and `run_as` becomes `www`; for Debian
`/etc/nginx` and `www-data`. The same tree, two platforms, no branches
in the states.

## Templated files

A configuration file that varies per machine is a **template file**. The
state says so, passes values in `context`, and the file is rendered on the
node before it is compared and written:

<!-- example: handbook/webserver/states/webserver/files/nginx.conf.jinja -->
```jinja
user {{ run_as }};
worker_processes {{ worker_processes }};

events {
    worker_connections 1024;
}

http {
    include {{ salt['pillar.get']('nginx:lookup:conf_dir', '/etc/nginx') }}/sites/*.conf;
    server {
        listen {{ port }};
        location /healthz { return 200 "ok\n"; }
    }
}
```

Inside the file, the names are `grains`, `pillar`, `salt`, `opts`, and
whatever `context` provided (plus `defaults`, values used when `context`
does not set them; `context` wins). A few things to know:

- halite **compares the rendered output** with the file on disk, so a
  template that renders to the same text every time converges.
- Only `jinja` is a supported `template:` engine; others are refused by
  name.
- If you use `source_hash`, it is checked against the file as fetched,
  **before** rendering.
- Anything in a templated file that must contain a literal `{{` has to be
  wrapped in `{% raw %}...{% endraw %}`.

## Debugging templates

**Compile and look.** The compiled low state shows what a template
produced, after loops and conditions have run:

```sh
halite-node state show_lowstate --local --file-root states --pillar-root pillar --out yaml
```

**See the rendered text.** `lint --rendered` prints a file after
templating, numbered, with the source line each rendered line came from:

```sh
halite-node lint states/webserver/sites.sls --rendered --local \
  --file-root states --pillar-root pillar
```

> **Note:** `lint` renders without running anything, and a call such as
> `salt['pillar.get'](...)` would run a module. So under `lint`, `salt` is
> deliberately absent and such a template reports `salt[pillar.get] is
> undefined`. This is a limit of `lint`, not an error in the template: use
> `state show_lowstate` (which has the dispatcher), or `apply --test`, for
> files that call `salt[...]`. Templates that use only `grains` and
> `pillar` lint cleanly, imports included, once you give `lint` the roots.

**Errors point at your source.** When a template error occurs in a file
that was itself produced by an include or an import, the message gives the
line in the file *you wrote*, with the surrounding source:

```
/…/webserver/map.jinja:5:33: salt[pillar.get] is undefined; …
  template source around the error:
    > 1 | {% from "webserver/map.jinja" import nginx with context %}
```

**Turn up the log.** `--log-level debug --log-fmt console` shows each
file as it renders.

## Limits

Templates are input, and in a tree served from git, input from whoever can
push. So rendering is bounded: output at most 64 MiB, loop iterations at
most 10 million per render, recursion at most 100 deep, includes at most
25 deep, and 60 seconds of wall clock. Exceeding any gives a named error
with a position, not a hang. All parsing and rendering also happens in an
unprivileged subprocess of the node, with no network and read access to
the cached tree only ([chapter 28](28-hardening.md)).

## Key ideas

- `{{ }}` outputs, `{% %}` does, `{# #}` comments; `-` trims whitespace.
- Templates see `grains`, `pillar`, `salt`, `opts`, `id`, `env`, `sls`
  and path helpers.
- **An undefined name is an error.** Make optionals explicit with
  `pillar.get(key, default)`, `| default()`, `is defined`.
- Use **loops** to turn pillar lists into states, **macros and imports**
  to share code, and a **`map.jinja`** with `grains.filter_by` to hold
  platform differences in one place.
- Emit structures with `| tojson`. Put a template on a file with
  `template: jinja` and `context`.
- Debug with `show_lowstate`; `lint --rendered` cannot call `salt[...]`.

Next: [grains](08-grains.md), the facts these templates ask for.
