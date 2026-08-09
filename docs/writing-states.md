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
block mappings, block lists, scalars, single/double quotes, `#` comments
(full-line and trailing), `[]` and `{}` empties. Not supported (parse
error, never a silent misparse): tabs in indentation, anchors/aliases,
flow collections, multi-line (`|` / `>`) scalars.

All scalars are strings. `enable: true` and `enable: "true"` are
identical; modules interpret booleans (`true/yes/1/on`) and numbers.
Quote file modes (`mode: "0644"`) as you would in Salt.

Multi-line file content: put it in a real file and use `- source:` —
better practice than inline blobs anyway.

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

Target patterns: `'*'` matches every host; `grain:valueglob` matches a
grain with a glob on the value (`os_family:FreeBSD`, `osrelease:14.*`);
anything else globs the `id` grain, which is the hostname masterless and
the enrolled identity under a control plane. All environments in the file
are applied (masterless has no environment selection yet).

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

Cycles, dangling references, and duplicate state declarations (same ID
and function across all loaded files) are compile-time errors before
anything runs.

## Dry runs

`halite apply -test file.sls` reports exactly what would change and
changes nothing. Every module honors this. Use it in CI and before any
production apply, same discipline as `test=True`.

## Ad hoc calls

Any state function can run directly:

```sh
halite call pkg.installed name=nginx
halite call service.running name=nginx enable=true
halite call file.absent name=/tmp/stale
halite call cmd.run name='zfs list' 
```
