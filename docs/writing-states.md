# Writing states

## File format

A state file (`.sls`) is a mapping of state IDs to module function calls,
in the same shape as Salt:

```yaml
<state_id>:
  <module>.<function>:
    - <arg>: <value>
    - <arg>: <value>
```

The ID doubles as the default `name` argument, so this is idiomatic:

```yaml
/usr/local/etc/motd:
  file.managed:
    - contents: welcome
```

States run in declaration order unless requisites reorder them.

## The YAML subset

halite parses the subset of YAML that SLS files use. Supported: nested
block mappings, block lists, scalars, single/double quotes (with the `''`
escape inside single quotes), `#` comments (full-line and trailing), `[]`
and `{}` empties. Not supported (parse error, never a silent misparse):
tabs in indentation, anchors/aliases, non-empty flow collections,
multi-line (`|` / `>`) scalars, duplicate keys in one mapping.

All scalars are strings. `enable: true` and `enable: "true"` are
identical; modules interpret booleans (`true/yes/1/on`) and numbers.
Quote file modes (`mode: "0644"`) as you would in Salt.

Multi-line file content: put it in a real file and use `- source:` —
better practice than inline blobs anyway.

To check an existing tree against all of this at once, run
`halite parse` ([migration.md](migration.md)).

## Templating

Files are rendered with Go `text/template` before parsing. Grains are
available under `.Grains`:

```yaml
{{ if eq .Grains.os_family "FreeBSD" }}
nginx_conf:
  file.managed:
    - name: /usr/local/etc/nginx/nginx.conf
    - source: files/nginx-freebsd.conf
{{ else }}
nginx_conf:
  file.managed:
    - name: /etc/nginx/nginx.conf
    - source: files/nginx-linux.conf
{{ end }}
```

Jinja → template cheat sheet:

| Jinja (Salt) | text/template (halite) |
|---|---|
| `{{ grains['os'] }}` | `{{ .Grains.os }}` |
| `{% if grains['os_family'] == 'FreeBSD' %}` | `{{ if eq .Grains.os_family "FreeBSD" }}` |
| `{% else %}` / `{% endif %}` | `{{ else }}` / `{{ end }}` |
| `{% for x in list %}` | `{{ range $x := ... }}` ... `{{ end }}` |
| `{{ a | default('b') }}` | `{{ .X | default "b" }}` |
| `{{ pillar['nginx']['port'] }}` | `{{ .Pillar.nginx.port }}` |
| `{{ salt['mine.get'](...) }}` | `{{ .Mine.network_interfaces }}` |

Missing grains render as empty rather than erroring.

## Pillar

Pillar is per-host data kept out of the state files: ports, paths, user
lists, anything that varies between hosts. It lives in its own tree with
its own top file, and is exposed to every state template under `.Pillar`.

```
/usr/local/etc/halite/
  states/
    top.sls
    web.sls
  pillar/
    top.sls
    common.sls
    web.sls
```

The pillar tree root defaults to a `pillar` directory beside the state
tree; override it with `-pillar-root` or `HALITE_PILLAR_ROOT`. The pillar
`top.sls` uses exactly the same targeting as a state top file:

```yaml
# pillar/top.sls
base:
  '*':
    - common
  'os_family:FreeBSD':
    - bsd
  'web*':
    - web
```

Pillar SLS files are plain data — no state IDs, no module functions:

```yaml
# pillar/web.sls
nginx:
  port: "8080"
  root: /var/www
  workers: "4"
```

```yaml
# states/web.sls
nginx_conf:
  file.managed:
    - name: /usr/local/etc/nginx/nginx.conf
    - source: files/nginx.conf.tmpl
    - template: true
```

```
# files/nginx.conf.tmpl
worker_processes {{ .Pillar.nginx.workers }};
server { listen {{ .Pillar.nginx.port }}; root {{ .Pillar.nginx.root }}; }
```

Rules that matter:

* Matched files are deep-merged in top-file order — later files win on
  conflicting leaves, sibling keys survive, and lists are replaced whole
  rather than concatenated.
* Pillar files may `include:` other pillar files. Includes merge first, so
  the including file overrides what it includes.
* Grains are in scope while rendering pillar files (`{{ .Grains.host }}`),
  but pillar is not in scope for the pillar tree itself.
* A missing pillar tree is not an error — `.Pillar` is simply empty.

Inspect what a host resolves to with `halite pillar` (or `-json`).

Pillar is where credentials end up, and halite does not encrypt it — the
pillar tree should be mode `0700`, and anything encrypted in version
control gets decrypted into the tree at deploy time. See
[pillar-security.md](pillar-security.md).

## The mine

Under a control plane, `.Mine` holds facts other hosts published about
themselves — `function -> agent -> data` — so one host's states can be
built from another's reality:

```
upstream backends {
{{- range $agent, $data := .Mine.grains }}
{{- if hasPrefix $agent "web" }}
    server {{ $agent }}.internal;
{{- end }}
{{- end }}
}
```

It is empty masterless and under `halite ssh`, so a tree that iterates
over it produces nothing rather than failing. See
[events.md](events.md#the-mine).

## The state tree, top files, and includes

States can live in a tree (default `/usr/local/etc/halite/states` on
FreeBSD, `/etc/halite/states` on Linux; override with `-root` or
`HALITE_ROOT`). Dotted names map to files: `web.nginx` is
`<root>/web/nginx.sls` or `<root>/web/nginx/init.sls`.

`halite apply` with no target performs a highstate: it reads
`<root>/top.sls`, matches targets against this host, and applies the
matched SLS names.

```yaml
# top.sls
base:
  '*':
    - common
  'os_family:FreeBSD':
    - freebsd.tuning
  'web*':
    - webserver
```

All environments in the file are applied (masterless has no environment
selection yet).

## Targeting

One target language serves top files, `halite run`, `halite ssh`, the
mine, and the reactor:

| Pattern | Selects |
|---|---|
| `*` | every host |
| `web*` | glob on the host id |
| `os_family:FreeBSD` | glob on a grain's value |
| `G@os_family:FreeBSD` | the same, spelled Salt's way |
| `L@web1,web2` | one of these ids |
| `E@^web[0-9]+$` | regular expression on the id |
| `P@osrelease:^14\.` | regular expression on a grain's value |

The `id` grain is the hostname masterless and the enrolled identity under
a control plane. A host without the named grain never matches, so
`'role:*'` selects the hosts that have a role, not the fleet. A grain
holding a list matches when any entry does, so `roles:web` selects a host
whose `roles` are `[web, cache]`.

Patterns combine with `and`, `or`, `not`, and parentheses:

```yaml
base:
  'web* and not L@web9':
    - webserver
  '(db* or cache*) and os_family:FreeBSD':
    - freebsd.tuning
```

```sh
halite run 'web* and not L@web9' state.highstate -test
```

Two patterns side by side are an error rather than an implied `and`, and
a matcher halite does not implement (`I@` pillar, `S@` subnet, `N@`
nodegroup, `R@` range) is reported where it is written — a target that
does not parse must not look like a fleet that happens to be empty.

## Custom grains

halite detects a fixed set of facts. Everything else a site targets on —
role, datacentre, tier — comes from a static grains file, which is plain
YAML merged over the detected grains:

```yaml
# /usr/local/etc/halite/grains  (Linux: /etc/halite/grains)
role: web
datacenter: lax1
roles:
  - web
  - cache
```

```sh
halite grains                      # detected facts plus the file
halite grains set role=web         # write the file (Salt: grains.setval)
halite grains unset role
halite grains -file ./grains       # read one somewhere else
```

Override the path with `-file` or `$HALITE_GRAINS`. Custom grains win over
detected ones, so a site that sets `os_family` by hand means it. A grains
file that does not parse is reported on stderr and skipped rather than
stopping the run — a typo in it must not stop a host converging.

The file is ordinary YAML, so the fleet-wide way to set a grain is
`file.managed` on the grains file itself. An agent sends its grains when
it connects, so a grain that changes mid-run reaches the control plane on
the agent's next reconnection.

An SLS file can pull in others with `include:`; included states run
before the including file's own states, each file loads at most once, and
include cycles are tolerated:

```yaml
include:
  - common
  - web.tls
```

Applying a single file (`halite apply ./site.sls`) resolves includes
relative to the file's directory unless `-root` is given.

## Requisites

`require` gates execution order and success; `watch` is `require` plus
change propagation:

```yaml
install_nginx:
  pkg.installed:
    - name: nginx

nginx_conf:
  file.managed:
    - name: /usr/local/etc/nginx/nginx.conf
    - source: files/nginx.conf
    - require:
      - pkg: install_nginx

nginx:
  service.running:
    - enable: true
    - watch:
      - file: nginx_conf
```

References are `- <module>: <state_id>` or a bare `- <state_id>` (matches
any module). If a required state fails, dependents are skipped and
reported as failed with the blocking requisite named. If a watched state
reports changes, `service.running` restarts the service and `cmd.wait`
fires its command.

Two more requisites round out the Salt set:

`onchanges` runs a state only when a referenced state actually changed
(unlike `watch`, which always runs the state and additionally reacts):

```yaml
reload_app:
  cmd.run:
    - name: service app reload
    - onchanges:
      - file: app_config
```

`prereq` runs a state *before* its target, and only if the target would
make changes (determined by an automatic dry run) — the classic use is
draining a load balancer before a config deploy:

```yaml
drain:
  cmd.run:
    - name: lb-drain web1
    - prereq:
      - file: deploy_config
```

### The `_in` forms

Every requisite has an `_in` twin that writes the same edge from the other
end: `require_in` is "that state requires me". It is the only way to
attach a requisite to a state another SLS file declares, which is why Salt
trees lean on it.

```yaml
# in web/tls.sls — nginx is declared in web/init.sls
tls_cert:
  file.managed:
    - name: /usr/local/etc/ssl/site.pem
    - source: files/site.pem
    - watch_in:
      - service: nginx
```

`require_in`, `watch_in`, `onchanges_in`, and `prereq_in` are resolved
onto their targets before ordering, so the result is exactly what writing
`watch: - file: tls_cert` on `nginx` would have produced. An `_in` naming
a state that no loaded file declares is a compile-time error.

### `names`

`names` declares the same state once per name — a loop written in the
state itself:

```yaml
install_tools:
  pkg.installed:
    - names:
      - vim
      - curl
      - htop
```

Each expansion becomes its own state, with `name` set and an ID of
`install_tools (vim)`, so the run output names which one did what. A
requisite pointing at `install_tools` reaches every expansion: it runs
after all of them, and `watch` fires if any of them changed.

Cycles, dangling references, and duplicate state declarations (same ID
and function across all loaded files) are compile-time errors before
anything runs.

## Modules halite does not have

An executable in the state tree's `_modules/` directory provides state
functions of its own, in whatever language you like:

```yaml
banner:
  motd.set:
    - text: welcome to the fleet
```

It ships to agents with the tree and is called with JSON on stdin. See
[external-modules.md](external-modules.md).

## Dry runs

`halite apply -test file.sls` reports exactly what would change and
changes nothing. Every module honors this. Use it in CI and before any
production apply, same discipline as `test=True`.

## Seeing the plan

`halite show` prints what the tree compiles to — the states, in the order
they would run, with their arguments, requisites, and the file each came
from. It takes the same targets `apply` does:

```sh
halite show                       # the highstate for this host
halite show web.nginx             # one dotted sls name
halite show ./site.sls            # one file
halite show -json | jq '.[].id'   # for a script
```

```
1. install_nginx
     pkg.installed  (web/init.sls)
       name: nginx
2. nginx_conf
     file.managed  (web/init.sls)
       name: /usr/local/etc/nginx/nginx.conf
       source: files/nginx.conf
       require: pkg:install_nginx

2 states from 1 sls file, in the order they would run
```

This is Salt's `state.show_sls` and `state.show_highstate`. It is not
`-test`: a dry run calls every module to ask what it would change, which
reads the host and takes as long as the run does. `show` stops after the
compile, so it answers the question you have when a highstate does
something surprising — what did my templates, includes, `_in` requisites,
and `names:` expansions actually produce, and in what order?

It runs locally, against this host's grains and pillar. There is no
fleet-wide plan: `halite run '*' show` has no job kind behind it.

## Ad hoc calls

Any state function can run directly:

```sh
halite call pkg.installed name=nginx
halite call service.running name=nginx enable=true
halite call file.absent name=/tmp/stale
halite call cmd.run name='zfs list' 
```
